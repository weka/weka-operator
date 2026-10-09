package modes

import (
	"context"
	"fmt"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/adhoc"
	"github.com/weka/weka-operator/internal/runtime/config"
)

func init() {
	register("adhoc-op", runAdhoc)
}

func runAdhoc(ctx context.Context, cfg *config.Config) error {
	if cfg.Instructions == nil {
		return fmt.Errorf("adhoc-op: no instructions provided")
	}

	ctx, logger := instrumentation.CreateLogSpan(ctx, "adhoc-op",
		"instruction_type", cfg.Instructions.Type)
	defer logger.End()

	switch cfg.Instructions.Type {
	case "discover-drives":
		return adhoc.RunDiscoverDrives(ctx, cfg)
	case "sign-drives":
		return adhoc.RunSignDrives(ctx, cfg)
	case "force-resign-drives":
		return adhoc.RunForceResignDrives(ctx, cfg)
	case "umount":
		return adhoc.RunUmount(ctx, cfg)
	case "debug":
		return adhoc.RunDebug(ctx, cfg)
	default:
		return fmt.Errorf("instruction %q not yet implemented", cfg.Instructions.Type)
	}
}
