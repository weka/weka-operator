package runtimes

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/lifecycle"
	"github.com/weka/weka-operator/internal/runtime/paths"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// Deps are the process/clock/filesystem/lifecycle collaborators every mode runtime needs.
// Shared across families so tests can substitute fakes without touching mode-specific code.
type Deps struct {
	Runner    process.CommandRunner
	Processes process.Launcher
	Clock     clock.Clock
	Paths     paths.Roots
	Coord     *lifecycle.Coordinator
}

// New builds the ModeRuntime for the mode named in env's MODE variable.
func New(env config.Env, deps *Deps) (lifecycle.ModeRuntime, error) {
	rt, err := config.ParseRuntimeSection(env)
	if err != nil {
		return nil, err
	}

	switch rt.Mode {
	case "compute", "drive", "s3", "nfs", "smbw", "data-services":
		cfg, err := config.ParseContainer(env)
		if err != nil {
			return nil, err
		}
		return newBackend(rt.Mode, &cfg, deps), nil
	case "client":
		cfg, err := config.ParseClient(env)
		if err != nil {
			return nil, err
		}
		return newClient(&cfg, deps), nil
	case "envoy", "telemetry":
		cfg, err := config.ParseContainer(env)
		if err != nil {
			return nil, err
		}
		return newAuxiliary(rt.Mode, &cfg, deps), nil
	case "ssdproxy":
		cfg, err := config.ParseContainer(env)
		if err != nil {
			return nil, err
		}
		return newSSDProxy(&cfg, deps), nil
	case "drivers-dist":
		cfg, err := config.ParseContainer(env)
		if err != nil {
			return nil, err
		}
		return newDriverDist(&cfg, deps), nil
	case "adhoc-op-with-container":
		cfg, err := config.ParseContainerOp(env)
		if err != nil {
			return nil, err
		}
		return newContainerOp(&cfg, deps), nil
	case "adhoc-op":
		cfg, err := config.ParseAdhoc(env)
		if err != nil {
			return nil, err
		}
		return NewTask("adhoc-op", func(ctx context.Context) error { return runAdhoc(ctx, &cfg, deps) }), nil
	case "drivers-builder":
		cfg, err := config.ParseDriverBuilder(env)
		if err != nil {
			return nil, err
		}
		return NewTask("drivers-builder", func(ctx context.Context) error { return runDriverBuilder(ctx, &cfg, deps) }), nil
	case "discovery":
		cfg, err := config.ParseDiscovery(env)
		if err != nil {
			return nil, err
		}
		return NewTask("discovery", func(ctx context.Context) error { return runDiscovery(ctx, &cfg, deps) }), nil
	case "drivers-loader":
		cfg, err := config.ParseDriverLoader(env)
		if err != nil {
			return nil, err
		}
		return NewTask("drivers-loader", func(ctx context.Context) error { return runDriverLoader(ctx, &cfg, deps) }), nil
	case "dist":
		return nil, fmt.Errorf("runtimes: unknown mode %q (did you mean %q?)", rt.Mode, "drivers-dist")
	default:
		return nil, fmt.Errorf("runtimes: unknown mode %q", rt.Mode)
	}
}
