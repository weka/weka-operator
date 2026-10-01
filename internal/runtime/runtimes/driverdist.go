package runtimes

import (
	"context"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/weka"
)

// distContainerName is the fixed weka container name for drivers-dist mode. Shutdown must use
// this rather than the pod identity name: shutdown.isContainerRunning filters "weka local ps"
// by this name, and the container is always created as "dist" regardless of the pod/identity name.
const distContainerName = "dist"

// driverDistRuntime brings up the "dist" stem container, which serves the built driver dist
// to other pods; Start returns once the container is up and the process continues supervising
// it. Mirrors Python drivers-dist mode at weka_runtime.py.
type driverDistRuntime struct {
	agentLaunched bool // set before the agent launch attempt; gates the Weka stop
	cfg           *config.ContainerConfig
	deps          *Deps
}

// newDriverDist builds the ModeRuntime for the drivers-dist mode.
func newDriverDist(cfg *config.ContainerConfig, deps *Deps) *driverDistRuntime {
	return &driverDistRuntime{cfg: cfg, deps: deps}
}

func (r *driverDistRuntime) Start(ctx context.Context) error {
	cfg, deps := r.cfg, r.deps
	ctx, logger := instrumentation.CreateLogSpan(ctx, "runtimes.driverDistRuntime.Start")
	defer logger.End()

	if _, err := acquirePersistentState(ctx, deps, "drivers-dist", cfg, cfg.Ports); err != nil {
		return err
	}

	// EnsureDrivers intentionally skipped — drivers-dist is on Python's special_modes list.
	if err := startAgent(ctx, deps, "drivers-dist", cfg, cfg.Ports, false, &r.agentLaunched); err != nil {
		return err
	}
	features, err := readFeatures(deps)
	if err != nil {
		return err
	}
	if err := weka.EnsureWekaVersion(ctx, deps.Runner, features); err != nil {
		return err
	}

	if err := startStem(ctx, deps, distContainerName, cfg.Ports.Weka, cfg.Traces, features); err != nil {
		return err
	}

	weka.CleanupTracesAndStopDumper(ctx, deps.Runner, deps.Clock, distContainerName, deps.Paths.OptWeka+"/traces")

	logger.Info("dist container ready")
	return nil
}

func (r *driverDistRuntime) Shutdown(ctx context.Context) error {
	id := r.cfg.Identity
	return runWekaShutdown(ctx, r.deps, "drivers-dist", distContainerName, id.PodID, r.agentLaunched)
}
