package blockdev

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
)

// sysBusPCIDevicesDir is the path to PCI devices in sysfs.
// Overridable in tests to point at a fake sysfs tree.
var sysBusPCIDevicesDir = "/sys/bus/pci/devices"

// nvmeClassCode is the PCI class code for NVMe storage devices.
const nvmeClassCode = "0x010802"

// IsKernelViewComplete returns true iff every NVMe-class PCI slot is bound to the
// kernel "nvme" driver. This gates the operator's missing-drive detection: only
// when the kernel sees every present NVMe can we trust that a serial absent from
// raw_drives means the drive is actually gone. Any unbound slot, DPDK-bound slot,
// or sysfs failure causes the function to return false so the operator defers.
//
// Mirrors Python is_kernel_view_complete() at weka_runtime.py:970 (commit 44c5d512).
func IsKernelViewComplete(ctx context.Context) bool {
	_, logger := instrumentation.CreateLogSpan(ctx, "blockdev.IsKernelViewComplete")
	defer logger.End()

	entries, err := os.ReadDir(sysBusPCIDevicesDir)
	if err != nil {
		logger.Warn("Failed to list PCI devices dir", "dir", sysBusPCIDevicesDir, "err", err)
		return false
	}

	for _, entry := range entries {
		addr := entry.Name()
		classPath := filepath.Join(sysBusPCIDevicesDir, addr, "class")

		classBytes, err := os.ReadFile(classPath)
		if err != nil {
			// Unreadable class file — skip; not our concern.
			continue
		}

		classCode := strings.TrimSpace(string(classBytes))
		if classCode != nvmeClassCode {
			continue
		}

		// This slot is NVMe — verify it is bound to the kernel "nvme" driver.
		driverLink := filepath.Join(sysBusPCIDevicesDir, addr, "driver")
		driverTarget, err := os.Readlink(driverLink)
		if err != nil {
			// No driver symlink → slot is unbound.
			logger.Info("Kernel view incomplete: NVMe slot is unbound", "addr", addr)
			return false
		}

		driverName := filepath.Base(driverTarget)
		if driverName != "nvme" {
			logger.Info("Kernel view incomplete: NVMe slot bound to non-nvme driver",
				"addr", addr, "driver", driverName)
			return false
		}
	}

	return true
}
