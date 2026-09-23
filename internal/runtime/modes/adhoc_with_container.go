package modes

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/internal/runtime/adhoc"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/weka"
)

func init() {
	register("adhoc-op-with-container", runAdhocWithContainer)
}

func runAdhocWithContainer(ctx context.Context, cfg *config.Config) error {
	const containerName = "adhoc"

	if err := weka.EnsureStemContainer(ctx, containerName, cfg.Port); err != nil {
		return fmt.Errorf("ensure stem container: %w", err)
	}
	if err := weka.ConfigureTraces(ctx, cfg, containerName); err != nil {
		return fmt.Errorf("configure traces: %w", err)
	}
	if err := weka.StartStemContainer(ctx); err != nil {
		return fmt.Errorf("start stem container: %w", err)
	}
	if err := weka.EnsureContainerExec(ctx, containerName); err != nil {
		return fmt.Errorf("ensure container exec: %w", err)
	}

	if cfg.Instructions == nil {
		return fmt.Errorf("no instructions provided")
	}
	switch cfg.Instructions.Type {
	case "ensure-nics":
		return adhoc.RunEnsureNICs(ctx, cfg)
	case "feature-flags-update":
		return adhoc.RunFeatureFlagsUpdate(ctx, cfg)
	default:
		return fmt.Errorf("instruction %q not supported in adhoc-op-with-container", cfg.Instructions.Type)
	}
}
