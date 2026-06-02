package modes

import (
	"context"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/network"
	"github.com/weka/weka-operator/internal/runtime/persistency"
	"github.com/weka/weka-operator/internal/runtime/weka"
)

func init() {
	register("envoy", runEnvoy)
}

func runEnvoy(ctx context.Context, cfg *config.Config) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "modes.runEnvoy")
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

	// EnsureDrivers intentionally skipped — envoy is a sidecar, not a driver container.
	// agent.Configure (called inside runAgent) already appends envoy-data to conditional_mounts_ids
	// and adds skip_envoy_setup=true for s3 cooperating pods (handled per cfg.Mode there).
	if err := runAgent(ctx, cfg); err != nil {
		return err
	}
	if err := weka.EnsureWekaVersion(ctx); err != nil {
		return err
	}

	if err := ensureEnvoyContainer(ctx); err != nil {
		return err
	}

	logger.Info("envoy container ready; exiting — envoy and agent continue independently")
	return nil
}

// ensureEnvoyContainer creates the envoy container if it does not already exist.
// Mirrors Python ensure_envoy_container() at weka_runtime.py.
func ensureEnvoyContainer(ctx context.Context) error {
	return cmdutil.Run(ctx, "sh", "-c",
		"weka local ps | grep -qw envoy || weka local setup envoy")
}
