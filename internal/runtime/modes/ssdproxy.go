package modes

import (
	"context"
	"fmt"
	"os"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/network"
	"github.com/weka/weka-operator/internal/runtime/persistency"
	"github.com/weka/weka-operator/internal/runtime/weka"
)

func init() {
	register("ssdproxy", runSSDProxy)
}

func runSSDProxy(ctx context.Context, cfg *config.Config) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "modes.runSSDProxy")
	defer logger.End()

	if err := persistency.Configure(ctx, cfg); err != nil {
		return err
	}
	if _, err := loadResources(ctx, cfg); err != nil {
		return err
	}
	if err := network.WriteManagementIPs(ctx, cfg); err != nil {
		return err
	}
	lock, err := runGenerationAndLock(ctx, cfg)
	if err != nil {
		return err
	}
	defer lock.Close() //nolint:errcheck // generation lock: close error on exit is not actionable

	// EnsureDrivers intentionally skipped — ssdproxy is a sidecar.
	if err := runAgent(ctx, cfg); err != nil {
		return err
	}
	if err := weka.EnsureWekaVersion(ctx); err != nil {
		return err
	}

	if err := assertIOMMUSupported(); err != nil {
		return err
	}
	if err := ensureSsdproxyContainer(ctx, cfg); err != nil {
		return err
	}
	if err := weka.ForceSetWekaVersion(ctx); err != nil {
		return err
	}
	// cfg.Mode == "ssdproxy" triggers the dedicated trace config branch in ConfigureTraces.
	// Mirror Python: fatal on configure_traces failure at weka_runtime.py:2443/2469.
	if err := weka.ConfigureTraces(ctx, cfg, cfg.Name); err != nil {
		return err
	}

	logger.Info("ssdproxy container ready; exiting — ssdproxy and agent continue independently")
	return nil
}

// assertIOMMUSupported checks that IOMMU groups are present on the host.
// Mirrors Python assert_ssdproxy_iommu_supported() at weka_runtime.py.
func assertIOMMUSupported() error {
	entries, err := os.ReadDir("/sys/kernel/iommu_groups")
	if err != nil {
		return fmt.Errorf("IOMMU not supported: cannot read /sys/kernel/iommu_groups: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("no IOMMU groups found — IOMMU may not be enabled in BIOS or kernel cmdline")
	}
	return nil
}

// ensureSsdproxyContainer creates the ssdproxy container and weka-sign-drive symlink.
// Mirrors Python ensure_ssdproxy_container() at weka_runtime.py.
func ensureSsdproxyContainer(ctx context.Context, cfg *config.Config) error {
	// Mirror Python: assert MEMORY, "MEMORY is not set" at weka_runtime.py:3413-3415.
	if cfg.Memory == "" {
		return fmt.Errorf("ssdproxy: MEMORY is not set")
	}
	script := fmt.Sprintf(`
weka local ps | grep -qw ssdproxy || weka local setup ssdproxy --memory %s --base-port 13000 --enable-ssdproxy-nginx
ln -sf /opt/weka/dist/extracted/weka-sign-drive /usr/bin/weka-sign-drive
`, cfg.Memory)
	return cmdutil.Run(ctx, "sh", "-c", script)
}
