package modes

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"

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
	if err := weka.EnsureWekaVersion(ctx, cfg); err != nil {
		return err
	}

	if err := assertIOMMUSupported(); err != nil {
		return err
	}
	// Force the weka version set before ensureSsdproxyContainer so the dist (including
	// weka-sign-drive under /opt/weka/dist/extracted) is laid down before the
	// symlink + kernelize step below needs it.
	if err := weka.ForceSetWekaVersion(ctx, cfg); err != nil {
		return err
	}
	if err := ensureSsdproxyContainer(ctx, cfg); err != nil {
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

// ensureSsdproxyContainer creates the ssdproxy container if absent (recovering an existing but
// unhealthy one via weka.HandleExistingContainer otherwise), then unconditionally rewrites its
// resources.json (memory + reserve_1g_hugepages=false) through the uuid-staged file + relink
// mechanism and starts it.
// Mirrors current Python ensure_ssdproxy_container() at weka_runtime.py:3838-3885 (post-06d8e375;
// this replaced the earlier memory-compare + "resources apply -f" approach from e5394a67).
func ensureSsdproxyContainer(ctx context.Context, cfg *config.Config) error {
	// Mirror Python: raise Exception("MEMORY environment variable must be set for ssdproxy").
	if cfg.Memory == "" {
		return fmt.Errorf("ssdproxy: MEMORY environment variable must be set for ssdproxy")
	}

	resourcesDir := fmt.Sprintf("/opt/weka/data/%s/container", cfg.Name)
	if err := os.MkdirAll(resourcesDir, 0o755); err != nil {
		return fmt.Errorf("ensureSsdproxyContainer: mkdir %s: %w", resourcesDir, err)
	}

	if err := cmdutil.Run(ctx, "sh", "-c", `
ln -sf /opt/weka/dist/extracted/weka-sign-drive /usr/bin/weka-sign-drive
weka-sign-drive kernelize || echo "weka-sign-drive kernelize failed (continuing)" >&2
`); err != nil {
		return fmt.Errorf("ensureSsdproxyContainer: weka-sign-drive setup: %w", err)
	}

	containers, err := weka.GetContainers(ctx)
	if err != nil {
		return fmt.Errorf("ensureSsdproxyContainer: list containers: %w", err)
	}
	var found map[string]interface{}
	for _, c := range containers {
		if name, ok := c["name"].(string); ok && name == cfg.Name {
			found = c
			break
		}
	}

	if found == nil {
		// --no-start --disable so resources.json can be staged below and applied on first start.
		if setupErr := cmdutil.Run(ctx, "sh", "-c", fmt.Sprintf(
			"weka local setup ssdproxy --memory=%s --base-port 13000 --enable-ssdproxy-nginx --no-start --disable",
			cfg.Memory)); setupErr != nil {
			return fmt.Errorf("ensureSsdproxyContainer: setup: %w", setupErr)
		}
	} else {
		// weka.HandleExistingContainer's deepest fallback (no recoverable resources file at all)
		// calls the generic per-mode createContainer(), which for "ssdproxy" doesn't match Python's
		// create_container() — that raises NotImplementedError for MODE=ssdproxy. This path is
		// unreachable in practice (requires a stopped container in Unknown state with zero
		// recoverable weka-resources.*.json files).
		// ponytail: reusing the generic recovery helper rather than a bespoke ssdproxy one; upgrade
		// if this fallback is ever observed for ssdproxy in the wild.
		if existingErr := weka.HandleExistingContainer(ctx, cfg, found, resourcesDir); existingErr != nil {
			return fmt.Errorf("ensureSsdproxyContainer: handle existing container: %w", existingErr)
		}
	}

	// Reconfigure resources via the versioned-JSON rewrite + relink mechanism, unconditionally,
	// for both new and existing containers (weka_runtime.py:3875-3883).
	res, err := weka.GetWekaLocalResources(ctx, cfg.Name)
	if err != nil {
		return fmt.Errorf("ensureSsdproxyContainer: get resources: %w", err)
	}
	memBytes, err := weka.ConvertToBytes(cfg.Memory)
	if err != nil {
		return fmt.Errorf("ensureSsdproxyContainer: parse MEMORY %q: %w", cfg.Memory, err)
	}
	res["memory"] = memBytes
	res["reserve_1g_hugepages"] = false

	fileName := fmt.Sprintf("weka-resources.%s.json", uuid.NewString())
	data, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("ensureSsdproxyContainer: marshal resources: %w", err)
	}
	if err := os.WriteFile(filepath.Join(resourcesDir, fileName), data, 0o644); err != nil {
		return fmt.Errorf("ensureSsdproxyContainer: write resources file: %w", err)
	}
	if err := weka.LinkResourcesFile(ctx, fileName, resourcesDir); err != nil {
		return fmt.Errorf("ensureSsdproxyContainer: link resources file: %w", err)
	}

	// Start the --disable'd container so the staged resources take effect.
	return weka.StartContainer(ctx, cfg.Name)
}
