package runtimes

import (
	"context"

	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/ports"
	"github.com/weka/weka-operator/internal/runtime/weka"
)

// clientRuntime runs MODE=client: a weka container that mounts the cluster from the client
// side, with its own port subrange and a frontend-disconnect wait before startup.
type clientRuntime struct {
	agentLaunched bool // set before the agent launch attempt; gates the Weka stop
	cfg           *config.ClientConfig
	deps          *Deps
}

func newClient(cfg *config.ClientConfig, deps *Deps) *clientRuntime {
	return &clientRuntime{cfg: cfg, deps: deps}
}

func (r *clientRuntime) Start(ctx context.Context) error {
	id := r.cfg.Identity

	allocatedPorts, err := ports.AllocateClient(ctx, r.deps.Paths, r.cfg.ClientPorts, r.cfg.Ports)
	if err != nil {
		return err
	}

	state, err := acquirePersistentState(ctx, r.deps, "client", &r.cfg.ContainerConfig, allocatedPorts)
	if err != nil {
		return err
	}

	err = weka.WaitFrontendDisconnect(ctx, r.deps.Clock, &r.deps.Paths, id.Name)
	if err != nil {
		return err
	}

	err = startAgent(ctx, r.deps, "client", &r.cfg.ContainerConfig, state.Ports, true, &r.agentLaunched)
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

	containerIn, err := buildContainerInput(r.deps, "client", id.Name, &r.cfg.ContainerConfig, &state, features)
	if err != nil {
		return err
	}
	in := &weka.ClientInput{ContainerInput: containerIn, ImageName: r.cfg.Agent.ImageName}
	if err := weka.EnsureClientContainer(ctx, in); err != nil {
		return err
	}

	if err := weka.ConfigureTraces(ctx, r.deps.Runner, weka.TracesInput{Mode: r.cfg.Traces, Features: features}, id.Name); err != nil {
		return err
	}
	if err := startAndPublish(ctx, r.deps, id.Name, features); err != nil {
		return err
	}
	registerCPUAffinity(r.deps, features)

	return nil
}

func (r *clientRuntime) Shutdown(ctx context.Context) error {
	id := r.cfg.Identity
	return runWekaShutdown(ctx, r.deps, "client", id.Name, id.PodID, r.agentLaunched)
}
