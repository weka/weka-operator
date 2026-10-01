package runtimes

import (
	"context"

	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/weka"
)

// auxiliaryRuntime runs envoy and telemetry: managed containers started via
// weka.StartManagedContainer, but never agent-verified or feature-flagged like the backend
// families.
type auxiliaryRuntime struct {
	agentLaunched bool // set before the agent launch attempt; gates the Weka stop
	mode          string
	cfg           *config.ContainerConfig
	deps          *Deps
}

func newAuxiliary(mode string, cfg *config.ContainerConfig, deps *Deps) *auxiliaryRuntime {
	return &auxiliaryRuntime{mode: mode, cfg: cfg, deps: deps}
}

func (r *auxiliaryRuntime) Start(ctx context.Context) error {
	cc := r.cfg

	state, err := acquirePersistentState(ctx, r.deps, r.mode, cc, cc.Ports)
	if err != nil {
		return err
	}

	err = startAgent(ctx, r.deps, r.mode, cc, state.Ports, false, &r.agentLaunched)
	if err != nil {
		return err
	}
	features, err := readFeatures(r.deps)
	if err != nil {
		return err
	}
	err = weka.EnsureWekaVersion(ctx, r.deps.Runner, features)
	if err != nil {
		return err
	}

	switch r.mode {
	case "envoy":
		if err := weka.EnsureManagedContainer(ctx, r.deps.Runner, "envoy", "--no-start", "--disable"); err != nil {
			return err
		}
		if err := weka.StartManagedContainer(ctx, r.deps.Runner, "envoy"); err != nil {
			return err
		}
	case "telemetry":
		if err := weka.EnsureManagedContainer(ctx, r.deps.Runner, "telemetry", "--not-dependent", "--no-start", "--disable"); err != nil {
			return err
		}
		if err := weka.StartManagedContainer(ctx, r.deps.Runner, "telemetry"); err != nil {
			return err
		}
		if err := weka.WriteTelemetryOverride(ctx, &r.deps.Paths); err != nil {
			return err
		}
	}

	return nil
}

func (r *auxiliaryRuntime) Shutdown(ctx context.Context) error {
	id := r.cfg.Identity
	return runWekaShutdown(ctx, r.deps, r.mode, id.Name, id.PodID, r.agentLaunched)
}
