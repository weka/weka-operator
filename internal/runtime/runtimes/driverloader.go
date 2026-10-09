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
		writeLoaderResult(ctx, logger, cfg.Results.Path, loaderResult{
			Err:           fmt.Sprintf("Failed to set up overlayfs: %v", err),
			DriversLoaded: false,
		})
		return nil
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
				writeLoaderResult(ctx, logger, cfg.Results.Path, loaderResult{Err: err.Error(), DriversLoaded: false})
				return nil
			}
			continue
		}
		writeLoaderResult(ctx, logger, cfg.Results.Path, loaderResult{Err: nil, DriversLoaded: true})
		logger.Info("drivers loaded successfully")
		return nil
	}

	writeLoaderResult(ctx, logger, cfg.Results.Path, loaderResult{Err: "Failed to load drivers within timeout", DriversLoaded: false})
	return nil
}

// writeLoaderResult writes the loader result.json and logs (rather than swallows) any write error.
// The result is the controller's only signal of loader outcome, so a write failure must be visible.
func writeLoaderResult(ctx context.Context, logger *instrumentation.SpanLogger, path string, res loaderResult) {
	if err := results.Write(ctx, path, res); err != nil {
		logger.Warn("failed to write loader result.json", "err", err)
	}
}
