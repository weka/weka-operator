package modes

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/network"
	"github.com/weka/weka-operator/internal/runtime/persistency"
	"github.com/weka/weka-operator/internal/runtime/weka"
)

func init() {
	register("drivers-dist", runDriversDist)
}

func runDriversDist(ctx context.Context, cfg *config.Config) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "modes.runDriversDist")
	defer logger.End()

	if err := persistency.Configure(ctx, cfg); err != nil {
		return err
	}
	if _, err := loadResources(ctx, cfg); err != nil {
		return err
	}
	if err := network.WriteManagementIPs(ctx, cfg); err != nil {
		return err
	}
	lock, err := runGenerationAndLock(ctx, cfg)
	if err != nil {
		return err
	}
	defer lock.Close() //nolint:errcheck // generation lock: close error on exit is not actionable

	// EnsureDrivers is intentionally skipped: drivers-dist is on the special_modes list.
	if err := runAgent(ctx, cfg); err != nil {
		return err
	}
	if err := weka.EnsureWekaVersion(ctx); err != nil {
		return err
	}

	if err := weka.EnsureStemContainer(ctx, "dist", cfg.Port); err != nil {
		return err
	}

	// Mirror Python: fatal on configure_traces failure (weka_runtime.py).
	if err := weka.ConfigureTraces(ctx, cfg, "dist"); err != nil {
		return err
	}

	if err := weka.StartStemContainer(ctx); err != nil {
		return err
	}

	cleanupTracesAndStopDumper(ctx)

	// Python process exits here; "dist" container continues independently.
	return nil
}

// cleanupTracesAndStopDumper waits for supervisorctl to start inside the dist container,
// stops the trace dumper, and removes stale shard files.
// Mirrors Python cleanup_traces_and_stop_dumper() at weka_runtime.py:3072.
// All errors are non-fatal: logged and execution continues.
func cleanupTracesAndStopDumper(ctx context.Context) {
	_, logger := instrumentation.CreateLogSpan(ctx, "modes.cleanupTracesAndStopDumper")
	defer logger.End()

	for {
		out, err := cmdutil.Output(ctx, "sh", "-c", "weka local exec --container dist supervisorctl status 2>/dev/null")
		if err != nil {
			logger.Warn("supervisorctl status check failed, will retry", "err", err)
		} else if strings.Contains(string(out), "RUNNING") {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}

	if err := cmdutil.Run(ctx, "sh", "-c", "weka local exec --container dist supervisorctl stop weka-trace-dumper"); err != nil {
		logger.Warn("stop weka-trace-dumper failed (non-fatal)", "err", err)
	}

	shards, err := filepath.Glob("/opt/weka/traces/*.shard")
	if err != nil {
		logger.Warn("failed to glob shard files (non-fatal)", "err", err)
	}
	for _, s := range shards {
		if err := cmdutil.Run(ctx, "rm", "-f", s); err != nil {
			logger.Warn("failed to remove shard (non-fatal)", "path", s, "err", err)
		}
	}
}
