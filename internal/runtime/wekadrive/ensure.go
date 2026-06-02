// ensure.go implements drive verification for the wekadrive package.
// Mirrors ensure_drives, assert_vfio_pci_loaded_if_required, has_iommu_groups
// at weka_runtime.py:3874–3909.
package wekadrive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/osinfo"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
)

// EnsureDrives validates VFIO-PCI is loaded if required, then matches requested drives
// against the system and writes the result to /opt/weka/k8s-runtime/drives.json.
// Mirrors Python ensure_drives() at weka_runtime.py:3890.
func EnsureDrives(ctx context.Context, cfg *config.Config) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "wekadrive.EnsureDrives")
	defer logger.End()

	if err := assertVFIOPCILoaded(ctx, cfg); err != nil {
		return err
	}

	sysDrives, err := FindWekaPartitions(ctx)
	if err != nil {
		return fmt.Errorf("EnsureDrives: find partitions: %w", err)
	}

	reqSet := make(map[string]struct{}, len(cfg.Drives))
	for _, s := range cfg.Drives {
		reqSet[s] = struct{}{}
	}

	// Filter to drives whose serial is in the requested set.
	var matched []interface{}
	for _, d := range sysDrives {
		if _, ok := reqSet[d.SerialId]; ok {
			matched = append(matched, d)
		}
	}

	logger.Info("drive reconciliation", "sys_drives", len(sysDrives), "requested", len(cfg.Drives), "matched", len(matched))

	err = os.MkdirAll("/opt/weka/k8s-runtime", 0o755)
	if err != nil {
		return err
	}
	var data []byte
	data, err = json.Marshal(matched)
	if err != nil {
		return err
	}
	return os.WriteFile("/opt/weka/k8s-runtime/drives.json", data, 0o644)
}

// assertVFIOPCILoaded checks that vfio_pci is loaded when IOMMU groups are present or on COS.
// Mirrors Python assert_vfio_pci_loaded_if_required() at weka_runtime.py:3874.
func assertVFIOPCILoaded(ctx context.Context, cfg *config.Config) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "wekadrive.assertVFIOPCILoaded")
	defer logger.End()

	nodeInfo, err := osinfo.Load()
	isCOS := err == nil && nodeInfo.IsCos()

	hasIOMMU, iommuErr := hasIOMMUGroups(ctx)
	if iommuErr != nil {
		logger.Warn("failed to detect IOMMU groups, assuming none", "err", iommuErr)
	}

	if isCOS || hasIOMMU {
		if err := cmdutil.Run(ctx, "sh", "-c", "lsmod | grep -w vfio_pci"); err != nil {
			return fmt.Errorf("vfio_pci module is required for drives but is not loaded: %w", err)
		}
	}
	return nil
}

// hasIOMMUGroups checks whether /sys/kernel/iommu_groups/ is non-empty.
// Mirrors Python has_iommu_groups() at weka_runtime.py:3855.
func hasIOMMUGroups(_ context.Context) (bool, error) {
	entries, err := os.ReadDir("/sys/kernel/iommu_groups")
	if err != nil {
		return false, nil // directory missing or inaccessible — treat as no IOMMU
	}
	return len(entries) > 0, nil
}
