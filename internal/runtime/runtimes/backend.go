package runtimes

import (
	"context"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/ports"
	"github.com/weka/weka-operator/internal/runtime/shutdown"
	"github.com/weka/weka-operator/internal/runtime/weka"
	"github.com/weka/weka-operator/internal/runtime/wekadrive"
)

// backendRuntime runs the modes that own a long-lived weka local container backed by the
// weka agent: compute, drive, s3, nfs, smbw, data-services.
type backendRuntime struct {
	agentLaunched bool // set before the agent launch attempt; gates the Weka stop
	mode          string
	cfg           *config.ContainerConfig
	deps          *Deps
}

// newBackend builds the ModeRuntime for a backend mode.
func newBackend(mode string, cfg *config.ContainerConfig, deps *Deps) *backendRuntime {
	return &backendRuntime{mode: mode, cfg: cfg, deps: deps}
}

func (r *backendRuntime) Start(ctx context.Context) error {
	id := r.cfg.Identity

	state, err := acquirePersistentState(ctx, r.deps, r.mode, r.cfg, r.cfg.Ports)
	if err != nil {
		return err
	}
	err = ports.SaveBackend(r.deps.Paths, state.Ports)
	if err != nil {
		return err
	}

	err = startAgent(ctx, r.deps, r.mode, r.cfg, state.Ports, true, &r.agentLaunched)
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

	in, err := buildContainerInput(r.deps, r.mode, id.Name, r.cfg, &state, features)
	if err != nil {
		return err
	}
	if err := weka.EnsureBackendContainer(ctx, &in); err != nil {
		return err
	}

	if err := weka.ConfigureTraces(ctx, r.deps.Runner, weka.TracesInput{Mode: r.cfg.Traces, Features: features}, id.Name); err != nil {
		return err
	}
	if r.mode == "compute" {
		if err := weka.WriteTelemetryOverride(ctx, &r.deps.Paths); err != nil {
			return err
		}
	}
	if err := startAndPublish(ctx, r.deps, id.Name, features); err != nil {
		return err
	}
	if r.mode == "compute" || r.mode == "drive" {
		registerCPUAffinity(r.deps, features)
	}

	if r.mode == "drive" {
		if err := wekadrive.EnsureDrives(ctx, r.deps.Runner, r.deps.Paths.Sys, state.Drives, r.deps.Paths.K8sRuntime); err != nil {
			return err
		}
	}

	return nil
}

func (r *backendRuntime) Shutdown(ctx context.Context) error {
	id := r.cfg.Identity
	if err := runWekaShutdown(ctx, r.deps, r.mode, id.Name, id.PodID, r.agentLaunched); err != nil {
		return err
	}
	if r.mode != "drive" {
		return nil
	}
	return shutdown.ReleaseDrives(ctx, shutdown.ReleaseInput{
		Runner: r.deps.Runner, Clock: r.deps.Clock, Paths: r.deps.Paths,
		Discover: func(ctx context.Context) ([]domain.DriveInfo, error) {
			return wekadrive.FindWekaPartitions(ctx, r.deps.Runner, false)
		},
	})
}
