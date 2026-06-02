package modes

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/osinfo"
	"github.com/weka/weka-operator/internal/runtime/agent"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/drivers"
	"github.com/weka/weka-operator/internal/runtime/results"
)

func init() {
	register("drivers-loader", runDriversLoader)
}

type loaderResult struct {
	Err           interface{} `json:"err"`
	DriversLoaded bool        `json:"drivers_loaded"`
}

func runDriversLoader(ctx context.Context, cfg *config.Config) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "modes.runDriversLoader")
	defer logger.End()

	if err := agent.OverrideDependenciesFlag(ctx, cfg); err != nil {
		return err
	}

	deadline := time.Now().Add(120 * time.Second)

	if err := drivers.DisableDriverSigning(ctx, cfg.COSAllowDisableDriverSign); err != nil {
		logger.Warn("DisableDriverSigning failed", "err", err)
	}

	if err := drivers.SetupOverlayfsForLibModules(ctx); err != nil {
		logger.Error(err, "failed to set up overlayfs")
		writeLoaderResult(logger, loaderResult{
			Err:           fmt.Sprintf("Failed to set up overlayfs: %v", err),
			DriversLoaded: false,
		})
		return nil
	}

	for time.Now().Before(deadline) {
		if err := loadDrivers(ctx, cfg); err != nil {
			time.Sleep(5 * time.Second)
			if time.Now().After(deadline) {
				writeLoaderResult(logger, loaderResult{Err: err.Error(), DriversLoaded: false})
				return nil
			}
			logger.Warn("failed to load drivers, retrying", "err", err)
			continue
		}
		writeLoaderResult(logger, loaderResult{Err: nil, DriversLoaded: true})
		logger.Info("drivers loaded successfully")
		return nil
	}

	writeLoaderResult(logger, loaderResult{Err: "Failed to load drivers within timeout", DriversLoaded: false})
	return nil
}

// writeLoaderResult writes the loader result.json and logs (rather than swallows) any write error.
// The result is the controller's only signal of loader outcome, so a write failure must be visible.
func writeLoaderResult(logger *instrumentation.SpanLogger, res loaderResult) {
	if err := results.Write(res); err != nil {
		logger.Warn("failed to write loader result.json", "err", err)
	}
}

func loadDrivers(ctx context.Context, cfg *config.Config) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "modes.loadDrivers")
	defer logger.End()

	// RHCOS ships kernel modules separately; copy them into the overlay first.
	if _, err := os.Stat("/hostpath/lib/modules"); err == nil {
		if err := cmdutil.Run(ctx, "sh", "-c", "cp -r /hostpath/lib/modules/* /lib/modules/"); err != nil {
			logger.Warn("failed to copy RHCOS kernel modules (non-fatal)", "err", err)
		}
	}

	wekaDriversHandling := drivers.WekaDriversHandling(cfg.ImageName)

	if !wekaDriversHandling {
		return loadDriversLegacy(ctx, cfg)
	}
	return loadDriversNew(ctx, cfg)
}

func loadDriversLegacy(ctx context.Context, cfg *config.Config) error {
	if err := os.MkdirAll("/opt/weka/dist/drivers", 0o755); err != nil {
		return err
	}

	nodeInfo, err := osinfo.Load()
	isCOS := err == nil && nodeInfo != nil && nodeInfo.IsCos()

	// Mirror Python should_skip_uio_pci_generic() at weka_runtime.py:1418:
	//   return version_params.get('uio_pci_generic') is False or should_skip_uio()
	// should_skip_uio() = is_google_cos()
	skipUIO := drivers.ResolveVersionParams(cfg.ImageName).ShouldSkipUioPciGeneric() || isCOS

	driverFiles := []string{
		"weka_driver-wekafsgw-*.ko",
		"weka_driver-wekafsio-*.ko",
		"mpin_user-*.ko",
	}
	// igb_uio is only available on non-COS systems.
	if !isCOS {
		driverFiles = append(driverFiles, "igb_uio-*.ko")
	}
	// uio_pci_generic is skipped when version params say so OR on COS.
	if !skipUIO {
		driverFiles = append(driverFiles, "uio_pci_generic-*.ko")
	}

	for _, df := range driverFiles {
		url := fmt.Sprintf("%s/dist/v1/drivers/%s", cfg.DistService, df)
		dst := fmt.Sprintf("/opt/weka/dist/drivers/%s", df)
		if err := cmdutil.Run(ctx, "sh", "-c", fmt.Sprintf("curl -kfo %s %s", dst, url)); err != nil {
			return fmt.Errorf("download %s: %w", df, err)
		}
	}

	driverPairs := []struct{ name, pattern string }{
		{"wekafsio", "weka_driver-wekafsio-*.ko"},
		{"wekafsgw", "weka_driver-wekafsgw-*.ko"},
		{"mpin_user", "mpin_user-*.ko"},
	}
	// igb_uio: non-COS only (unrelated to uio_pci_generic gating).
	if !isCOS {
		driverPairs = append(driverPairs,
			struct{ name, pattern string }{"igb_uio", "igb_uio-*.ko"},
		)
	}
	// uio_pci_generic: gated by skipUIO (version params + COS).
	if !skipUIO {
		driverPairs = append(driverPairs,
			struct{ name, pattern string }{"uio_pci_generic", "uio_pci_generic-*.ko"},
		)
	}

	for _, dp := range driverPairs {
		if err := cmdutil.Run(ctx, "sh", "-c", fmt.Sprintf("lsmod | grep -w %s", dp.name)); err == nil {
			continue // already loaded
		}
		if err := cmdutil.Run(ctx, "sh", "-c",
			fmt.Sprintf("insmod /opt/weka/dist/drivers/%s", dp.pattern)); err != nil {
			return fmt.Errorf("insmod %s: %w", dp.name, err)
		}
	}

	drivers.LoadModules(ctx, skipUIO)
	return nil
}

func loadDriversNew(ctx context.Context, cfg *config.Config) error {
	version, err := drivers.GetWekaVersion()
	if err != nil {
		return err
	}

	kernelBuildID, err := drivers.KernelBuildID(cfg.DriversBuildID, cfg.DistService)
	if err != nil {
		return err
	}

	// When the runtime image differs from the version image, weka binaries live on a shared volume.
	fromPath := ""
	if cfg.TargetImageName != "" && cfg.TargetImageName != cfg.ImageName {
		fromPath = "file://shared-weka-version/opt-weka"
	}

	if fromPath != "" {
		versionGetCmd := fmt.Sprintf(
			"weka version get --without-agent --driver-only --from %s %s",
			fromPath, version,
		)
		err = cmdutil.Run(ctx, "sh", "-c", versionGetCmd)
		if err != nil {
			return fmt.Errorf("loadDriversNew: weka version get: %w", err)
		}
	}

	downloadArgs := buildWekaDriverArgs("download", cfg.DistService, version, kernelBuildID)
	err = cmdutil.Run(ctx, "sh", "-c", downloadArgs)
	if err != nil {
		return fmt.Errorf("loadDriversNew: weka driver download: %w", err)
	}

	// Unload any previously installed weka drivers — ignore errors if not loaded.
	_ = cmdutil.Run(ctx, "rmmod", "wekafsio") //nolint:errcheck // best-effort: error expected when module is not loaded
	_ = cmdutil.Run(ctx, "rmmod", "wekafsgw") //nolint:errcheck // best-effort: error expected when module is not loaded

	installArgs := buildWekaDriverArgs("install", "", version, kernelBuildID)
	err = cmdutil.Run(ctx, "sh", "-c", installArgs)
	if err != nil {
		return fmt.Errorf("loadDriversNew: weka driver install: %w", err)
	}

	nodeInfo, err := osinfo.Load()
	isCOS := err == nil && nodeInfo != nil && nodeInfo.IsCos()
	// Mirror Python should_skip_uio_pci_generic() at weka_runtime.py:1418.
	skipUIO := drivers.ResolveVersionParams(cfg.ImageName).ShouldSkipUioPciGeneric() || isCOS
	drivers.LoadModules(ctx, skipUIO)
	return nil
}

// buildWekaDriverArgs returns a shell command string for "weka driver <subcmd>".
// distService is only used for "download"; empty means it is omitted.
func buildWekaDriverArgs(subcmd, distService, version, kernelBuildID string) string {
	var sb strings.Builder
	sb.WriteString("weka driver ")
	sb.WriteString(subcmd)
	if distService != "" {
		sb.WriteString(" --from '")
		sb.WriteString(distService)
		sb.WriteString("'")
	}
	sb.WriteString(" --without-agent")
	sb.WriteString(" --version ")
	sb.WriteString(version)
	if kernelBuildID != "" {
		sb.WriteString(" --kernel-build-id ")
		sb.WriteString(kernelBuildID)
	}
	return sb.String()
}
