package runtimes

import (
	"context"
	"fmt"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/weka"
)

// ssdProxyRuntime brings up the ssdproxy sidecar container: persistence, network, generation
// lock, agent, weka version, IOMMU check, container setup, and trace config.
// Mirrors Python ssdproxy mode at weka_runtime.py.
type ssdProxyRuntime struct {
	agentLaunched bool // set before the agent launch attempt; gates the Weka stop
	cfg           *config.ContainerConfig
	deps          *Deps
}

// newSSDProxy builds the ModeRuntime for the ssdproxy mode.
func newSSDProxy(cfg *config.ContainerConfig, deps *Deps) *ssdProxyRuntime {
	return &ssdProxyRuntime{cfg: cfg, deps: deps}
}

func (r *ssdProxyRuntime) Start(ctx context.Context) error {
	cfg, deps := r.cfg, r.deps
	ctx, logger := instrumentation.CreateLogSpan(ctx, "runtimes.ssdProxyRuntime.Start")
	defer logger.End()

	if cfg.Memory.Request == "" {
		return fmt.Errorf("ssdproxy: MEMORY environment variable must be set for ssdproxy")
	}

	if _, err := acquirePersistentState(ctx, deps, "ssdproxy", cfg, cfg.Ports); err != nil {
		return err
	}

	if err := startAgent(ctx, deps, "ssdproxy", cfg, cfg.Ports, true, &r.agentLaunched); err != nil {
		return err
	}
	features, err := readFeatures(deps)
	if err != nil {
		return err
	}
	err = weka.CheckIOMMUCompatible(&deps.Paths, features)
	if err != nil {
		return err
	}
	err = weka.ForceSetWekaVersion(ctx, deps.Runner, features)
	if err != nil {
		return err
	}

	memBytes, err := weka.ParseSize(cfg.Memory.Request)
	if err != nil {
		return fmt.Errorf("ssdproxy: parse MEMORY %q: %w", cfg.Memory.Request, err)
	}
	if err := weka.SetupSSDProxyContainer(ctx, &weka.SSDProxyInput{
		Runner:      deps.Runner,
		Clock:       deps.Clock,
		Roots:       deps.Paths,
		Features:    features,
		Name:        cfg.Identity.Name,
		MemoryBytes: memBytes,
		MemoryStr:   cfg.Memory.Request,
	}); err != nil {
		return err
	}

	// cfg.Mode == "ssdproxy" triggers the dedicated trace config branch in ConfigureTraces.
	if err := weka.ConfigureTraces(ctx, deps.Runner, weka.TracesInput{
		Mode:     cfg.Traces,
		Features: features,
		SSDProxy: true,
	}, cfg.Identity.Name); err != nil {
		return err
	}

	logger.Info("ssdproxy container ready")
	return nil
}

func (r *ssdProxyRuntime) Shutdown(ctx context.Context) error {
	id := r.cfg.Identity
	return runWekaShutdown(ctx, r.deps, "ssdproxy", id.Name, id.PodID, r.agentLaunched)
}
