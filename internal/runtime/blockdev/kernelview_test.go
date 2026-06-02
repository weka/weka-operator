package blockdev

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// makeFakePCIDevice creates a fake PCI device directory under devicesDir with the
// given address and class code. If driverName is non-empty, a "driver" symlink is
// created pointing to a directory named driverName; if empty, no symlink is created
// (simulates unbound device).
func makeFakePCIDevice(t *testing.T, devicesDir, addr, classCode, driverName string) {
	t.Helper()
	addrDir := filepath.Join(devicesDir, addr)
	if err := os.MkdirAll(addrDir, 0o755); err != nil {
		t.Fatalf("makeFakePCIDevice: mkdir %s: %v", addrDir, err)
	}
	if err := os.WriteFile(filepath.Join(addrDir, "class"), []byte(classCode+"\n"), 0o644); err != nil {
		t.Fatalf("makeFakePCIDevice: write class: %v", err)
	}
	if driverName != "" {
		// Create the target directory so Readlink works with a real path.
		driverTarget := filepath.Join(t.TempDir(), "drivers", driverName)
		if err := os.MkdirAll(driverTarget, 0o755); err != nil {
			t.Fatalf("makeFakePCIDevice: mkdir driver target: %v", err)
		}
		if err := os.Symlink(driverTarget, filepath.Join(addrDir, "driver")); err != nil {
			t.Fatalf("makeFakePCIDevice: symlink driver: %v", err)
		}
	}
}

func TestIsKernelViewComplete(t *testing.T) {
	// Save and restore the package-level sysfs path override.
	origDir := sysBusPCIDevicesDir
	t.Cleanup(func() { sysBusPCIDevicesDir = origDir })

	ctx := context.Background()

	t.Run("empty devices dir returns true", func(t *testing.T) {
		devicesDir := t.TempDir()
		sysBusPCIDevicesDir = devicesDir
		if !IsKernelViewComplete(ctx) {
			t.Error("got false, want true for empty devices dir")
		}
	})

	t.Run("non-NVMe device ignored, returns true", func(t *testing.T) {
		devicesDir := t.TempDir()
		sysBusPCIDevicesDir = devicesDir
		// SATA controller class (0x010601), not NVMe
		makeFakePCIDevice(t, devicesDir, "0000:00:01.0", "0x010601", "ahci")
		if !IsKernelViewComplete(ctx) {
			t.Error("got false, want true — non-NVMe device should be ignored")
		}
	})

	t.Run("NVMe device bound to nvme driver returns true", func(t *testing.T) {
		devicesDir := t.TempDir()
		sysBusPCIDevicesDir = devicesDir
		makeFakePCIDevice(t, devicesDir, "0000:00:04.0", nvmeClassCode, "nvme")
		if !IsKernelViewComplete(ctx) {
			t.Error("got false, want true — NVMe bound to nvme should be complete")
		}
	})

	t.Run("NVMe device bound to vfio-pci returns false", func(t *testing.T) {
		devicesDir := t.TempDir()
		sysBusPCIDevicesDir = devicesDir
		makeFakePCIDevice(t, devicesDir, "0000:00:04.0", nvmeClassCode, "vfio-pci")
		if IsKernelViewComplete(ctx) {
			t.Error("got true, want false — NVMe bound to vfio-pci should be incomplete")
		}
	})

	t.Run("unbound NVMe device (no driver symlink) returns false", func(t *testing.T) {
		devicesDir := t.TempDir()
		sysBusPCIDevicesDir = devicesDir
		// driverName="" → no symlink created → unbound
		makeFakePCIDevice(t, devicesDir, "0000:00:04.0", nvmeClassCode, "")
		if IsKernelViewComplete(ctx) {
			t.Error("got true, want false — unbound NVMe should be incomplete")
		}
	})

	t.Run("mixed: NVMe bound to nvme and non-NVMe device returns true", func(t *testing.T) {
		devicesDir := t.TempDir()
		sysBusPCIDevicesDir = devicesDir
		makeFakePCIDevice(t, devicesDir, "0000:00:04.0", nvmeClassCode, "nvme")
		makeFakePCIDevice(t, devicesDir, "0000:00:1f.2", "0x010601", "ahci") // SATA
		if !IsKernelViewComplete(ctx) {
			t.Error("got false, want true — all NVMe bound to nvme, non-NVMe ignored")
		}
	})

	t.Run("mixed: one NVMe bound to nvme, one to vfio-pci returns false", func(t *testing.T) {
		devicesDir := t.TempDir()
		sysBusPCIDevicesDir = devicesDir
		makeFakePCIDevice(t, devicesDir, "0000:00:04.0", nvmeClassCode, "nvme")
		makeFakePCIDevice(t, devicesDir, "0000:00:05.0", nvmeClassCode, "vfio-pci")
		if IsKernelViewComplete(ctx) {
			t.Error("got true, want false — one NVMe bound to vfio-pci should be incomplete")
		}
	})

	t.Run("missing devices dir returns false", func(t *testing.T) {
		sysBusPCIDevicesDir = "/nonexistent/path/that/does/not/exist"
		if IsKernelViewComplete(ctx) {
			t.Error("got true, want false — missing devices dir should return false")
		}
	})
}
