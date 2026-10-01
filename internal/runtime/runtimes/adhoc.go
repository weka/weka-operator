package runtimes

import (
	"context"
	"fmt"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/adhoc"
	"github.com/weka/weka-operator/internal/runtime/config"
)

// runAdhoc dispatches a single host operation with no agent and no container involved, so
// there is nothing for Shutdown to stop. Mirrors Python adhoc-op mode at weka_runtime.py.
func runAdhoc(ctx context.Context, cfg *config.AdhocConfig, deps *Deps) error {
	if cfg.Operation.Raw == "" {
		return fmt.Errorf("adhoc-op: no instructions provided")
	}

	ctx, logger := instrumentation.CreateLogSpan(ctx, "adhoc-op",
		"instruction_type", string(cfg.Operation.Type))
	defer logger.End()

	switch cfg.Operation.Type {
	case "discover-drives":
		return adhoc.RunDiscoverDrives(ctx, deps.Runner, cfg.Results.Path)
	case "sign-drives":
		return adhoc.RunSignDrives(ctx, deps.Runner, deps.Clock, cfg.Operation.Raw, cfg.Results.Path)
	case "force-resign-drives":
		return adhoc.RunForceResignDrives(ctx, deps.Runner, cfg.Operation.Raw, cfg.Results.Path)
	case "umount":
		return adhoc.RunUmount(ctx, deps.Runner, cfg.Results.Path)
	case "debug":
		return adhoc.RunDebug(ctx, deps.Runner)
	default:
		return fmt.Errorf("instruction %q not yet implemented", cfg.Operation.Type)
	}
}
