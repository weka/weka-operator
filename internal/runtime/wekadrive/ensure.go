// ensure.go implements drive verification for the wekadrive package.
// Mirrors ensure_drives, assert_vfio_pci_loaded_if_required, has_iommu_groups
// at weka_runtime.py:3874–3909.
package wekadrive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/pkg/osinfo"
	"github.com/weka/weka-operator/internal/runtime/process"
	"github.com/weka/weka-operator/internal/runtime/weka"
)

// EnsureDrives validates VFIO-PCI is loaded if required, then matches requested drives
// against the system and writes the result to <k8sRuntimeDir>/drives.json.
// Mirrors Python ensure_drives() at weka_runtime.py:3890.
func EnsureDrives(ctx context.Context, runner process.CommandRunner, sysRoot string, drives []string, k8sRuntimeDir string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "wekadrive.EnsureDrives")
	defer logger.End()

	if err := assertVFIOPCILoaded(ctx, runner, sysRoot); err != nil {
		return err
	}

	// use_sign_tool=false: the sign tool binary is absent from the weka container image, and
	// drives are matched by serial here, not by type. Mirrors weka_runtime.py:4360.
	sysDrives, err := FindWekaPartitions(ctx, runner, false)
	if err != nil {
		return fmt.Errorf("EnsureDrives: find partitions: %w", err)
	}

	reqSet := make(map[string]struct{}, len(drives))
	for _, s := range drives {
		reqSet[s] = struct{}{}
	}

	// Filter to drives whose serial is in the requested set.
	var matched []domain.DriveInfo
	for _, d := range sysDrives {
		if _, ok := reqSet[d.SerialId]; ok {
			matched = append(matched, d)
		}
	}

	logger.Info("drive reconciliation", "sys_drives", len(sysDrives), "requested", len(drives), "matched", len(matched))

	err = os.MkdirAll(k8sRuntimeDir, 0o755)
	if err != nil {
		return err
	}
	var data []byte
	data, err = json.Marshal(matched)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(k8sRuntimeDir, "drives.json"), data, 0o644)
}

// assertVFIOPCILoaded checks that vfio_pci is loaded when IOMMU groups are present or on COS.
// Mirrors Python assert_vfio_pci_loaded_if_required() at weka_runtime.py:3874.
func assertVFIOPCILoaded(ctx context.Context, runner process.CommandRunner, sysRoot string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "wekadrive.assertVFIOPCILoaded")
	defer logger.End()

	nodeInfo, err := osinfo.Load()
	isCOS := err == nil && nodeInfo.IsCos()

	if isCOS || weka.HasIOMMUGroups(sysRoot) {
		if _, err := runner.Run(ctx, process.Command{Path: "sh", Args: []string{"-c", "lsmod | grep -w vfio_pci"}}); err != nil {
			return fmt.Errorf("vfio_pci module is required for drives but is not loaded: %w", err)
		}
	}
	return nil
}
