package weka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// TracesInput carries ConfigureTraces' configuration.
type TracesInput struct {
	Mode     config.Traces
	Features domain.FeatureFlags
	// SSDProxy enables the extra ssdproxy /traces/config.json branch (Python's MODE == "ssdproxy").
	SSDProxy bool
}

// ConfigureTraces writes or removes the trace dumper config inside the named container.
// Mirrors Python configure_traces() at weka_runtime.py:2379.
func ConfigureTraces(ctx context.Context, runner process.CommandRunner, in TracesInput, name string) error {
	mode := in.Mode.DumperConfigMode
	if mode == "auto" || mode == "" {
		if in.Features.TracesOverridePartialSupport {
			mode = "partial-override"
		} else {
			mode = "cluster"
		}
	}

	const (
		oldFullLocation  = "/data/reserved_space/dumper_config.json.override"
		legacyPartialLoc = "/data/reserved_space/dumper_config_overrides.json"
		newPartialLoc    = "/traces/config_overrides.json"
		stagingPath      = "/opt/weka/k8s-scripts/dumper_config.json.override"
	)

	switch mode {
	case "override":
		data := map[string]interface{}{
			"enabled":                 true,
			"ensure_free_space_bytes": in.Mode.EnsureFreeSpaceGB * 1024 * 1024 * 1024,
			"retention_bytes":         in.Mode.MaxCapacityGB * 1024 * 1024 * 1024,
			"retention_type":          "BYTES",
			"version":                 1,
			"freeze_period": map[string]interface{}{
				"start_time": "0001-01-01T00:00:00+00:00",
				"end_time":   "0001-01-01T00:00:00+00:00",
				"retention":  0,
			},
		}
		if err := writeConfigToContainer(ctx, runner, name, data, stagingPath, oldFullLocation); err != nil {
			return err
		}

	case "partial-override":
		data := map[string]interface{}{
			"ensure_free_space_bytes": in.Mode.EnsureFreeSpaceGB * 1024 * 1024 * 1024,
			"retention_bytes":         in.Mode.MaxCapacityGB * 1024 * 1024 * 1024,
			"retention_type":          "BYTES",
		}
		dest := legacyPartialLoc
		if in.Features.TracesOverrideInSlashTraces {
			dest = newPartialLoc
		}
		if err := writeConfigToContainer(ctx, runner, name, data, stagingPath, dest); err != nil {
			return err
		}

	case "cluster":
		script := fmt.Sprintf("weka local run --container %s rm -f %s %s %s",
			name, oldFullLocation, legacyPartialLoc, newPartialLoc)
		if _, err := runner.Run(ctx, process.Shell(script)); err != nil {
			return fmt.Errorf("configure_traces cluster mode: %w", err)
		}

	default:
		return fmt.Errorf("invalid DUMPER_CONFIG_MODE: %q", mode)
	}

	if !in.SSDProxy {
		return nil
	}

	ensureFreeBytes := 0
	if mode == "partial-override" || mode == "override" {
		ensureFreeBytes = in.Mode.EnsureFreeSpaceGB * 1024 * 1024 * 1024
	}
	ssdCfg := map[string]interface{}{
		"enabled":                 true,
		"ensure_free_space_bytes": ensureFreeBytes,
		"freeze_period": map[string]interface{}{
			"comment":    "",
			"end_time":   "1970-01-01T00:00:00Z",
			"retention":  0,
			"start_time": "1970-01-01T00:00:00Z",
		},
		"retention_type": "DEFAULT",
		"version":        1,
		"weka_iops_rate": map[string]interface{}{},
	}
	if err := writeConfigToContainer(ctx, runner, name, ssdCfg, "/opt/weka/k8s-scripts/config.json", "/traces/config.json"); err != nil {
		return fmt.Errorf("configure_traces ssdproxy config.json: %w", err)
	}
	return nil
}

func writeConfigToContainer(ctx context.Context, runner process.CommandRunner, name string, data map[string]interface{}, staging, dest string) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	script := fmt.Sprintf(`mkdir -p /opt/weka/k8s-scripts
echo %s > %s
weka local run --container %s mv %s %s`, process.ShellQuote(string(b)), staging, name, staging, dest)
	if _, err := runner.Run(ctx, process.Shell(script)); err != nil {
		return fmt.Errorf("configure_traces write to container: %w", err)
	}
	return nil
}

// CleanupTracesAndStopDumper waits for supervisorctl to start inside containerName, stops the
// trace dumper, and removes stale shard files under tracesDir. All errors are non-fatal: logged
// and execution continues. Mirrors Python cleanup_traces_and_stop_dumper() at weka_runtime.py:3072.
func CleanupTracesAndStopDumper(ctx context.Context, runner process.CommandRunner, clk clock.Clock, containerName, tracesDir string) {
	_, logger := instrumentation.CreateLogSpan(ctx, "weka.CleanupTracesAndStopDumper", "container", containerName)
	defer logger.End()

	statusCmd := process.Shell(fmt.Sprintf("weka local exec --container %s supervisorctl status 2>/dev/null", containerName))
	statusCmd.Log = process.LogNone
	err := clock.Poll(ctx, clk, 3*time.Second, func() (bool, error) {
		// supervisorctl status exits 3 while any program is not RUNNING, which in dist is always
		// (weka-io programs never start there); like Python's `| grep RUNNING`, decide on stdout.
		res, runErr := runner.Run(ctx, statusCmd)
		if strings.Contains(string(res.Stdout), "RUNNING") {
			return true, nil
		}
		if runErr != nil {
			logger.Warn("supervisorctl status check failed, will retry", "err", runErr)
		}
		return false, nil
	})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logger.Warn("waiting for supervisorctl failed (non-fatal)", "err", err)
		}
		return
	}

	stopCmd := fmt.Sprintf("weka local exec --container %s supervisorctl stop weka-trace-dumper", containerName)
	if _, err = runner.Run(ctx, process.Shell(stopCmd)); err != nil {
		logger.Warn("stop weka-trace-dumper failed (non-fatal)", "err", err)
	}

	shards, err := filepath.Glob(tracesDir + "/*.shard")
	if err != nil {
		logger.Warn("failed to glob shard files (non-fatal)", "err", err)
	}
	for _, s := range shards {
		if err := os.Remove(s); err != nil {
			logger.Warn("failed to remove shard (non-fatal)", "path", s, "err", err)
		}
	}
}
