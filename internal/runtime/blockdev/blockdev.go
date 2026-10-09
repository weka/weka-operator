// Package blockdev discovers block devices on the host using nsenter + lsblk.
package blockdev

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/weka/weka-operator/internal/pkg/osinfo"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
)

// Disk represents a block device found on the host.
type Disk struct {
	Path        string
	IsMounted   bool
	SerialID    string
	CapacityGiB int
}

type lsblkOutput struct {
	BlockDevices []lsblkDevice `json:"blockdevices"`
}

type lsblkDevice struct {
	Name       string        `json:"name"`
	Type       string        `json:"type"`
	Mountpoint string        `json:"mountpoint"`
	Serial     string        `json:"serial"`
	Children   []lsblkDevice `json:"children"`
}

func (d *lsblkDevice) hasMountpoint() bool {
	if d.Mountpoint != "" {
		return true
	}
	for i := range d.Children {
		if d.Children[i].hasMountpoint() {
			return true
		}
	}
	return false
}

// disksFromLsblk parses raw lsblk JSON output and returns Disk entries for every
// device whose Type=="disk". Per-device I/O (serial, capacity) is NOT done here.
func disksFromLsblk(out []byte) ([]Disk, error) {
	var parsed lsblkOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("lsblk JSON parse: %w", err)
	}
	var disks []Disk
	for i := range parsed.BlockDevices {
		dev := &parsed.BlockDevices[i]
		if dev.Type != "disk" {
			continue
		}
		disks = append(disks, Disk{
			Path:      dev.Name,
			IsMounted: dev.hasMountpoint(),
		})
	}
	return disks, nil
}

// FindDisks enumerates all disk-type block devices visible from the host PID namespace.
func FindDisks(ctx context.Context) ([]Disk, error) {
	out, err := cmdutil.Output(ctx,
		"nsenter", "--mount", "--pid", "--target", "1", "--",
		"lsblk", "-p", "-J", "-o", "NAME,TYPE,MOUNTPOINT,SERIAL",
	)
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}

	disks, err := disksFromLsblk(out)
	if err != nil {
		return nil, err
	}

	var result []Disk
	for _, d := range disks {
		serialID, err := GetDeviceSerialID(ctx, d.Path)
		if err != nil {
			serialID = ""
		}
		devCap, err := GetCapacityGiB(ctx, d.Path)
		if err != nil || devCap == 0 {
			continue
		}
		result = append(result, Disk{
			Path:        d.Path,
			IsMounted:   d.IsMounted,
			SerialID:    serialID,
			CapacityGiB: devCap,
		})
	}
	return result, nil
}

// GetDeviceSerialID returns the serial ID for the given block device path.
// For NVMe on COS: reads /sys/block/<name>/wwid
// For NVMe elsewhere: reads /sys/class/block/<name>/../serial
// For SATA/SCSI: reads ID_SERIAL from /host/run/udev/data/b<maj:min>
func GetDeviceSerialID(ctx context.Context, devicePath string) (string, error) {
	deviceName := filepath.Base(devicePath)

	if strings.Contains(strings.ToLower(deviceName), "nvme") {
		// COS has a different sysfs layout; use wwid directly.
		if nodeInfo, err := osinfo.Load(); err == nil && nodeInfo.IsCos() {
			return cosSerialFallback(ctx, deviceName)
		}
		// NVMe: serial is one level up from the namespace dir
		sysPath, err := filepath.EvalSymlinks(fmt.Sprintf("/sys/class/block/%s", deviceName))
		if err != nil {
			return cosSerialFallback(ctx, deviceName)
		}
		// Remove last path component (the namespace, e.g. nvme0n1) to get the controller dir
		controllerDir := filepath.Dir(sysPath)
		serialPath := filepath.Join(controllerDir, "serial")
		data, err := os.ReadFile(serialPath)
		if err != nil {
			return cosSerialFallback(ctx, deviceName)
		}
		return strings.TrimSpace(string(data)), nil
	}

	// SATA/SCSI: use udev data
	devIndex, err := os.ReadFile(fmt.Sprintf("/sys/block/%s/dev", deviceName))
	if err != nil {
		return "", fmt.Errorf("reading /sys/block/%s/dev: %w", deviceName, err)
	}
	maj_min := strings.TrimSpace(string(devIndex))
	udevPath := fmt.Sprintf("/host/run/udev/data/b%s", maj_min)
	data, err := os.ReadFile(udevPath)
	if err != nil {
		return "", fmt.Errorf("reading udev data %s: %w", udevPath, err)
	}
	return parseUdevSerial(data), nil
}

// parseUdevSerial finds the first line containing the substring "ID_SERIAL=" and
// returns the text after the first "=" on that line, trimmed. This matches the
// Python original (grep 'ID_SERIAL=' | cut -d= -f2-).
//
// Bug fix: the previous Go code used strings.CutPrefix(line, "ID_SERIAL="), which
// only matched lines *starting* with "ID_SERIAL=". Real udev data lines are
// "E:"-prefixed (e.g. "E:ID_SERIAL=Samsung_SSD_970"), so the old code always
// returned "" for real SATA/SCSI drives. Note that "ID_SERIAL_SHORT=" does NOT
// contain the substring "ID_SERIAL=" so it is correctly excluded.
func parseUdevSerial(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "ID_SERIAL=") {
			continue
		}
		// Return everything after the first "=" on this line, trimmed.
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		return strings.TrimSpace(line[idx+1:])
	}
	return ""
}

// GetCapacityGiB returns the capacity of a block device in GiB.
func GetCapacityGiB(ctx context.Context, devicePath string) (int, error) {
	out, err := cmdutil.Output(ctx, "blockdev", "--getsize64", devicePath)
	if err != nil {
		return 0, fmt.Errorf("blockdev --getsize64 %s: %w", devicePath, err)
	}
	var sizeBytes int64
	if _, err := fmt.Sscan(strings.TrimSpace(string(out)), &sizeBytes); err != nil {
		return 0, fmt.Errorf("parsing blockdev output: %w", err)
	}
	if sizeBytes == 0 {
		return 0, nil
	}
	return int(sizeBytes / (1024 * 1024 * 1024)), nil
}

// sysClassBlockRoot is a package var so tests can point it at a temp dir mimicking
// /sys/class/block's layout instead of the real sysfs.
var sysClassBlockRoot = "/sys/class/block"

// ResolveBlockDeviceSysfsPath resolves a block device name (e.g. nvme0n1, sda, nvme0n1p1) to its
// canonical path under /sys/class/block. Mirrors Python _resolve_block_device_sysfs_path()
// (os.path.realpath), which does not require the target to exist; EvalSymlinks does, so on error
// this falls back to the unresolved path and lets the caller treat a missing model as unknown.
func ResolveBlockDeviceSysfsPath(deviceName string) string {
	path := filepath.Join(sysClassBlockRoot, deviceName)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

// GetDeviceModel returns the device model string for a block device or one of its partitions.
// Supports NVMe (e.g. nvme0n1, nvme0n1p1) and SCSI/SATA (e.g. sda, sda1). Mirrors Python
// get_device_model(); callers decide how to log/handle a missing model.
func GetDeviceModel(devicePath string) (string, error) {
	deviceName := filepath.Base(devicePath)
	sysfsPath := ResolveBlockDeviceSysfsPath(deviceName)

	// A partition's sysfs dir is nested inside the whole-device dir and has a "partition"
	// marker file; step up so resolution below is device-agnostic.
	if _, err := os.Stat(filepath.Join(sysfsPath, "partition")); err == nil {
		sysfsPath = filepath.Dir(sysfsPath)
	}

	var modelPath string
	if strings.Contains(strings.ToLower(deviceName), "nvme") {
		// NVMe: model lives on the controller dir, one level above the namespace dir.
		modelPath = filepath.Join(filepath.Dir(sysfsPath), "model")
	} else {
		// SCSI/SATA: model lives at <whole-device>/device/model
		modelPath = filepath.Join(sysfsPath, "device", "model")
	}

	data, err := os.ReadFile(modelPath)
	if err != nil {
		return "", fmt.Errorf("reading model at %s: %w", modelPath, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// ResolveDriveModel prefers the model reported in hardware info (hardwareModel, then
// hardwareModelNumber) and falls back to sysfs-based resolution via GetDeviceModel. Returns ""
// if neither source has one. Mirrors Python resolve_drive_model().
func ResolveDriveModel(hardwareModel, hardwareModelNumber, devicePath string) string {
	model := strings.TrimSpace(hardwareModel)
	if model == "" {
		model = strings.TrimSpace(hardwareModelNumber)
	}
	if model != "" {
		return model
	}
	if devicePath == "" {
		return ""
	}
	model, _ = GetDeviceModel(devicePath) //nolint:errcheck // best-effort, mirrors Python's broad except+warn-in-caller
	return model
}

// GetDevicePathBySerial resolves a drive serial to a /dev/ path by searching /dev/disk/by-id/.
// It finds the first symlink whose name contains the serial string, then resolves it to the
// canonical /dev/ device path.
func GetDevicePathBySerial(ctx context.Context, serial string) (string, error) {
	entries, err := os.ReadDir("/dev/disk/by-id")
	if err != nil {
		return "", fmt.Errorf("reading /dev/disk/by-id: %w", err)
	}
	for _, e := range entries {
		if !strings.Contains(e.Name(), serial) {
			continue
		}
		target, evalErr := filepath.EvalSymlinks("/dev/disk/by-id/" + e.Name())
		if evalErr != nil {
			continue
		}
		return target, nil
	}
	return "", fmt.Errorf("no device found for serial %q in /dev/disk/by-id", serial)
}

// cosSerialFallback reads /sys/block/<name>/wwid for Google COS where NVMe serial path differs.
func cosSerialFallback(_ context.Context, deviceName string) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/sys/block/%s/wwid", deviceName))
	if err != nil {
		return "", fmt.Errorf("reading wwid for %s: %w", deviceName, err)
	}
	s := strings.TrimSpace(string(data))
	if s == "None" || s == "" {
		return "", nil
	}
	return s, nil
}
