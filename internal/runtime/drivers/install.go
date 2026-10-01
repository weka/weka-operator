package drivers

import (
	"context"
	"fmt"
	"os"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/osinfo"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// LoadInput narrows the driver-loader config to what Load needs for one load attempt.
type LoadInput struct {
	ImageName       string // IMAGE_NAME
	TargetImageName string // TARGET_IMAGE_NAME
	DistService     string // DIST_SERVICE
	BuildID         string // DRIVERS_BUILD_ID
	OptWeka         string // weka install root
}

// Load installs and loads weka kernel drivers for one attempt; callers retry on error.
func Load(ctx context.Context, runner process.CommandRunner, in *LoadInput) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "drivers.Load")
	defer logger.End()

	// RHCOS ships kernel modules separately; copy them into the overlay first.
	if _, err := os.Stat("/hostpath/lib/modules"); err == nil {
		if _, err := runner.Run(ctx, process.Shell("cp -r /hostpath/lib/modules/* /lib/modules/")); err != nil {
			logger.Warn("failed to copy RHCOS kernel modules (non-fatal)", "err", err)
		}
	}

	if !WekaDriversHandling(in.ImageName) {
		return loadDriversLegacy(ctx, runner, in)
	}
	return loadDriversNew(ctx, runner, in)
}

func loadDriversLegacy(ctx context.Context, runner process.CommandRunner, in *LoadInput) error {
	driversDir := in.OptWeka + "/dist/drivers"
	if err := os.MkdirAll(driversDir, 0o755); err != nil {
		return err
	}

	nodeInfo, err := osinfo.Load()
	isCOS := err == nil && nodeInfo != nil && nodeInfo.IsCos()

	// Mirror Python should_skip_uio_pci_generic() at weka_runtime.py:1418:
	//   return version_params.get('uio_pci_generic') is False or should_skip_uio()
	// should_skip_uio() = is_google_cos()
	skipUIO := ResolveVersionParams(in.ImageName).ShouldSkipUioPciGeneric() || isCOS

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
		url := fmt.Sprintf("%s/dist/v1/drivers/%s", in.DistService, df)
		dst := fmt.Sprintf("%s/%s", driversDir, df)
		if _, err := runner.Run(ctx, process.Shell(fmt.Sprintf("curl -kfo %s %s", dst, url))); err != nil {
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
		if _, err := runner.Run(ctx, process.Shell(fmt.Sprintf("lsmod | grep -w %s", dp.name))); err == nil {
			continue // already loaded
		}
		if _, err := runner.Run(ctx, process.Shell(
			fmt.Sprintf("insmod %s/%s", driversDir, dp.pattern))); err != nil {
			return fmt.Errorf("insmod %s: %w", dp.name, err)
		}
	}

	LoadModules(ctx, runner, skipUIO)
	return nil
}

func loadDriversNew(ctx context.Context, runner process.CommandRunner, in *LoadInput) error {
	version, err := GetWekaVersion(in.OptWeka)
	if err != nil {
		return err
	}

	kernelBuildID, err := KernelBuildID(in.BuildID, in.DistService)
	if err != nil {
		return err
	}

	// When the runtime image differs from the version image, weka binaries live on a shared volume.
	fromPath := ""
	if in.TargetImageName != "" && in.TargetImageName != in.ImageName {
		fromPath = "file://shared-weka-version/opt-weka"
	}

	if fromPath != "" {
		versionGetCmd := fmt.Sprintf(
			"weka version get --without-agent --driver-only --from %s %s",
			fromPath, version,
		)
		if _, err = runner.Run(ctx, process.Shell(versionGetCmd)); err != nil {
			return fmt.Errorf("loadDriversNew: weka version get: %w", err)
		}
	}

	downloadArgs := buildWekaDriverArgs("download", in.DistService, version, kernelBuildID)
	if _, err = runner.Run(ctx, process.Shell(downloadArgs)); err != nil {
		return fmt.Errorf("loadDriversNew: weka driver download: %w", err)
	}

	// Unload any previously installed weka drivers — ignore errors if not loaded.
	_, _ = runner.Run(ctx, process.Command{Path: "rmmod", Args: []string{"wekafsio"}}) //nolint:errcheck // best-effort: error expected when module is not loaded
	_, _ = runner.Run(ctx, process.Command{Path: "rmmod", Args: []string{"wekafsgw"}}) //nolint:errcheck // best-effort: error expected when module is not loaded

	installArgs := buildWekaDriverArgs("install", "", version, kernelBuildID)
	if _, err = runner.Run(ctx, process.Shell(installArgs)); err != nil {
		return fmt.Errorf("loadDriversNew: weka driver install: %w", err)
	}

	// The rmmod unload steps above are non-fatal on purpose (needed for the normal
	// same-version force-reload path), so "weka driver install" can report success
	// while the old modules are still resident. Confirm the requested version is
	// actually ready before declaring victory.
	readyCmd := fmt.Sprintf("weka driver ready --without-agent --version %s", version)
	if _, err = runner.Run(ctx, process.Shell(readyCmd)); err != nil {
		return fmt.Errorf("loadDriversNew: drivers for version %s did not become ready after install "+
			"(this usually means the previous driver failed to unload, most often because remnant "+
			"wekafs mounts on the host are still holding the old module resident): %w", version, err)
	}

	nodeInfo, err := osinfo.Load()
	isCOS := err == nil && nodeInfo != nil && nodeInfo.IsCos()
	// Mirror Python should_skip_uio_pci_generic() at weka_runtime.py:1418.
	skipUIO := ResolveVersionParams(in.ImageName).ShouldSkipUioPciGeneric() || isCOS
	LoadModules(ctx, runner, skipUIO)
	return nil
}

// buildWekaDriverArgs returns a shell command string for "weka driver <subcmd>".
// distService is only used for "download"; empty means it is omitted.
func buildWekaDriverArgs(subcmd, distService, version, kernelBuildID string) string {
	var fromPart, kernelBuildIDPart string
	if distService != "" {
		fromPart = fmt.Sprintf(" --from '%s'", distService)
	}
	if kernelBuildID != "" {
		kernelBuildIDPart = " --kernel-build-id " + kernelBuildID
	}
	return fmt.Sprintf("weka driver %s%s --without-agent --version %s%s", subcmd, fromPart, version, kernelBuildIDPart)
}
