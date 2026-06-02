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
	register("telemetry", runTelemetry)
}

func runTelemetry(ctx context.Context, cfg *config.Config) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "modes.runTelemetry")
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

	// EnsureDrivers intentionally skipped — telemetry is a sidecar.
	if err := runAgent(ctx, cfg); err != nil {
		return err
	}
	if err := weka.EnsureWekaVersion(ctx); err != nil {
		return err
	}

	if err := ensureTelemetryContainer(ctx); err != nil {
		return err
	}
	// Mirror Python: fatal on write_telemetry_config_override failure at weka_runtime.py:3408.
	// compute.go:33 already returns this error; match it here.
	if err := weka.WriteTelemetryConfigOverride(ctx); err != nil {
		return err
	}

	logger.Info("telemetry container ready; exiting — telemetry and agent continue independently")
	return nil
}

// ensureTelemetryContainer creates the telemetry container if it does not already exist.
// --not-dependent allows it to start without waiting for other containers.
// Mirrors Python ensure_telemetry_container() at weka_runtime.py.
func ensureTelemetryContainer(ctx context.Context) error {
	return cmdutil.Run(ctx, "sh", "-c",
		"weka local ps | grep -qw telemetry || weka local setup telemetry --not-dependent")
}
