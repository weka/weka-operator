package modes

import (
	"context"

	"github.com/weka/weka-operator/internal/runtime/agent"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/cpuaffinity"
	"github.com/weka/weka-operator/internal/runtime/network"
	"github.com/weka/weka-operator/internal/runtime/persistency"
	"github.com/weka/weka-operator/internal/runtime/ports"
	"github.com/weka/weka-operator/internal/runtime/weka"
)

func init() {
	register("compute", runCompute)
}

func runCompute(ctx context.Context, cfg *config.Config) error {
	if err := persistency.Configure(ctx, cfg); err != nil {
		return err
	}
	res, loadErr := loadResources(ctx, cfg)
	if loadErr != nil {
		return loadErr
	}
	if err := ports.SavePorts(ctx, cfg); err != nil {
		return err
	}
	if err := network.WriteManagementIPs(ctx, cfg); err != nil {
		return err
	}
	lock, lockErr := runGenerationAndLock(ctx, cfg)
	if lockErr != nil {
		return lockErr
	}
	defer lock.Close() //nolint:errcheck // generation lock: close error on exit is not actionable
	if err := agent.EnsureDrivers(ctx, cfg); err != nil {
		return err
	}
	if err := runAgent(ctx, cfg); err != nil {
		return err
	}
	if err := weka.EnsureWekaVersion(ctx); err != nil {
		return err
	}
	if err := weka.EnsureWekaContainer(ctx, cfg, res); err != nil {
		return err
	}
	if err := weka.ConfigureTraces(ctx, cfg, cfg.Name); err != nil {
		return err
	}
	if err := weka.WriteTelemetryConfigOverride(ctx); err != nil {
		return err
	}
	if err := startAndVerifyContainer(ctx, cfg); err != nil {
		return err
	}
	if err := weka.WriteFeatureFlagsJSON(ctx, cfg); err != nil {
		return err
	}
	go cpuaffinity.NewManager(cfg).RunPeriodic(ctx)
	return runShutdownLoop(ctx, cfg)
}
