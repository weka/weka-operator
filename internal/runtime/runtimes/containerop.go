package runtimes

import (
	"context"
	"fmt"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/adhoc"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/weka"
)

// containerOpContainerName is the fixed weka container name for adhoc-op-with-container mode.
// Shutdown must use this rather than the pod identity name: shutdown.isContainerRunning filters
// "weka local ps" by this name, and the container is always created as "adhoc" regardless of
// the pod/identity name.
const containerOpContainerName = "adhoc"

// containerOpRuntime brings up a minimal "stem" weka container and dispatches a single
// operation against it. EnsureDrivers is intentionally skipped — Python excludes
// adhoc-op-with-container from the ensure_drivers list (weka_runtime.py:4251).
// Mirrors Python adhoc-op-with-container mode at weka_runtime.py.
type containerOpRuntime struct {
	agentLaunched bool // set before the agent launch attempt; gates the Weka stop
	cfg           *config.ContainerOpConfig
	deps          *Deps
}

// newContainerOp builds the ModeRuntime for the adhoc-op-with-container mode.
func newContainerOp(cfg *config.ContainerOpConfig, deps *Deps) *containerOpRuntime {
	return &containerOpRuntime{cfg: cfg, deps: deps}
}

func (r *containerOpRuntime) Start(ctx context.Context) error {
	const containerName = containerOpContainerName
	cfg, deps := r.cfg, r.deps

	ctx, logger := instrumentation.CreateLogSpan(ctx, "runtimes.containerOpRuntime.Start")
	defer logger.End()

	if err := startAgent(ctx, deps, "adhoc-op-with-container", &cfg.ContainerConfig, cfg.Ports, false, &r.agentLaunched); err != nil {
		return err
	}
	features, err := readFeatures(deps)
	if err != nil {
		return err
	}
	if err := weka.EnsureWekaVersion(ctx, deps.Runner, features); err != nil {
		return err
	}

	if err := startStem(ctx, deps, containerName, cfg.Ports.Weka, cfg.Traces, features); err != nil {
		return err
	}
	if err := weka.EnsureContainerExec(ctx, deps.Runner, deps.Clock, containerName); err != nil {
		return fmt.Errorf("ensure container exec: %w", err)
	}

	if cfg.Operation.Type == "" {
		return fmt.Errorf("no instructions provided")
	}
	switch cfg.Operation.Type {
	case "ensure-nics":
		return adhoc.RunEnsureNICs(ctx, deps.Runner, cfg.Operation.Payload, cfg.AWS, cfg.Results.Path)
	case "feature-flags-update":
		return adhoc.RunFeatureFlagsUpdate(ctx, features, cfg.Results.Path)
	default:
		return fmt.Errorf("instruction %q not supported in adhoc-op-with-container", cfg.Operation.Type)
	}
}

func (r *containerOpRuntime) Shutdown(ctx context.Context) error {
	id := r.cfg.Identity
	return runWekaShutdown(ctx, r.deps, "adhoc-op-with-container", containerOpContainerName, id.PodID, r.agentLaunched)
}
