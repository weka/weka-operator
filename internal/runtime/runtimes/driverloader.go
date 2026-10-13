package runtimes

import (
	"context"
	"fmt"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/osinfo"
	"github.com/weka/weka-operator/internal/runtime/agent"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/drivers"
	"github.com/weka/weka-operator/internal/runtime/results"
)

type loaderResult struct {
	Err           interface{} `json:"err"`
	DriversLoaded bool        `json:"drivers_loaded"`
}

// runDriverLoader retries driver loading for up to 2 minutes, then reports the outcome via
// results.json. Mirrors Python drivers_loader mode at weka_runtime.py.
func runDriverLoader(ctx context.Context, cfg *config.DriverLoaderConfig, deps *Deps) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "runtimes.runDriverLoader")
	defer logger.End()

	nodeInfo, err := osinfo.Load()
	if err != nil {
		return fmt.Errorf("drivers-loader: load osinfo: %w", err)
	}
	if nodeInfo.IsNixos() {
		if err := drivers.PrepareNixosHostKernel(ctx, deps.Runner); err != nil {
			return fmt.Errorf("drivers-loader: %w", err)
		}
	}

	if err := agent.OverrideDependenciesFlag(ctx, deps.Runner, cfg.Drivers.ImageName); err != nil {
		return err
	}

	deadline := deps.Clock.Now().Add(120 * time.Second)

	if err := drivers.DisableDriverSigning(ctx, deps.Runner, cfg.Host.AllowDisableDriverSign); err != nil {
		logger.Warn("DisableDriverSigning failed", "err", err)
	}

	if err := drivers.SetupOverlayfsForLibModules(ctx, deps.Runner); err != nil {
		logger.Error(err, "failed to set up overlayfs")
		return writeLoaderResult(ctx, cfg.Results.Path, loaderResult{
			Err:           fmt.Sprintf("Failed to set up overlayfs: %v", err),
			DriversLoaded: false,
		})
	}

	loadInput := &drivers.LoadInput{
		ImageName:       cfg.Drivers.ImageName,
		TargetImageName: cfg.Drivers.TargetImageName,
		DistService:     cfg.Drivers.DistService,
		BuildID:         cfg.Drivers.BuildID,
		OptWeka:         deps.Paths.OptWeka,
	}

	for deps.Clock.Now().Before(deadline) {
		if err := drivers.Load(ctx, deps.Runner, loadInput); err != nil {
			logger.Warn("failed to load drivers, retrying", "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-deps.Clock.After(5 * time.Second):
			}
			if !deps.Clock.Now().Before(deadline) {
				return writeLoaderResult(ctx, cfg.Results.Path, loaderResult{Err: err.Error(), DriversLoaded: false})
			}
			continue
		}
		if err := writeLoaderResult(ctx, cfg.Results.Path, loaderResult{Err: nil, DriversLoaded: true}); err != nil {
			return err
		}
		logger.Info("drivers loaded successfully")
		return nil
	}

	return writeLoaderResult(ctx, cfg.Results.Path, loaderResult{Err: "Failed to load drivers within timeout", DriversLoaded: false})
}

// writeLoaderResult writes the loader result.json. The result is the controller's only signal of
// loader outcome, so a write failure fails the runtime instead of leaving the pod waiting.
func writeLoaderResult(ctx context.Context, path string, res loaderResult) error {
	if err := results.Write(ctx, path, res); err != nil {
		return fmt.Errorf("drivers-loader: write result: %w", err)
	}
	return nil
}
