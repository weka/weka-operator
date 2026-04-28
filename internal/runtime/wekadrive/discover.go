// Package wekadrive discovers and manages Weka-formatted partitions on the host.
package wekadrive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/blockdev"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
)

const (
	partTypeGUID           = "993ec906-b4e2-11e7-a205-a0a8cd3ea1de"
	unsignedDriveSignature = "90f0090f90f0090f90f0090f90f0090f"
)

// FindWekaPartitions scans /dev/disk/by-id/ and /dev/disk/by-path/ for Weka-formatted partitions.
// It checks partition GUID and reads the Weka magic to determine if the drive is signed.
func FindWekaPartitions(ctx context.Context) ([]domain.DriveInfo, error) {
	partNames, err := collectPartNames(ctx)
	if err != nil {
		return nil, err
	}

	var drives []domain.DriveInfo
	seen := make(map[string]struct{})

	for _, partName := range partNames {
		if _, dup := seen[partName]; dup {
			continue
		}
		seen[partName] = struct{}{}

		typeID, err := getPartEntryType(ctx, partName)
		if err != nil {
			continue
		}
		if typeID != partTypeGUID {
			continue
		}

		signature, err := readDriveSignature(ctx, partName)
		if err != nil {
			signature = ""
		}

		isSigned := signature != "" && signature != unsignedDriveSignature
		wekaGUID := ""
		if isSigned && len(signature) == 32 {
			wekaGUID = fmt.Sprintf("%s-%s-%s-%s-%s",
				signature[0:8], signature[8:12], signature[12:16], signature[16:20], signature[20:32])
		}

		// Resolve partition block device to its parent disk
		pciDevPath, err := filepath.EvalSymlinks(fmt.Sprintf("/sys/class/block/%s", partName))
		if err != nil {
			continue
		}
		// Parent disk is one directory up: e.g., .../nvme0n1p1 -> .../nvme0n1
		parentName := filepath.Base(filepath.Dir(pciDevPath))
		diskPath := "/dev/" + parentName

		serialID, err := blockdev.GetDeviceSerialID(ctx, diskPath)
		if err != nil {
			serialID = ""
		}

		drives = append(drives, domain.DriveInfo{
			SerialId:   serialID,
			DevicePath: diskPath,
			Partition:  "/dev/" + partName,
			IsSigned:   isSigned,
			WekaGuid:   wekaGUID,
		})
	}
	return drives, nil
}

// collectPartNames collects unique partition names from /dev/disk/by-path/ and /dev/disk/by-id/.
func collectPartNames(_ context.Context) ([]string, error) {
	var partNames []string
	seen := make(map[string]struct{})

	for _, dir := range []string{"/dev/disk/by-path", "/dev/disk/by-id"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading %s: %w", dir, err)
		}
		for _, e := range entries {
			target, err := filepath.EvalSymlinks(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			name := filepath.Base(target)
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			partNames = append(partNames, name)
		}
	}
	return partNames, nil
}

func getPartEntryType(ctx context.Context, partName string) (string, error) {
	out, err := cmdutil.Output(ctx, "blkid", "-s", "PART_ENTRY_TYPE", "-o", "value", "-p", "/dev/"+partName)
	if err != nil {
		return "", fmt.Errorf("blkid for %s: %w", partName, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func readDriveSignature(ctx context.Context, partName string) (string, error) {
	// Read 16 bytes at offset 8, formatted as hex (matches Python hexdump -v -e '1/1 "%.2x"' -s 8 -n 16)
	out, err := cmdutil.Output(ctx, "hexdump", "-v", "-e", `1/1 "%.2x"`, "-s", "8", "-n", "16", "/dev/"+partName)
	if err != nil {
		return "", fmt.Errorf("hexdump for %s: %w", partName, err)
	}
	return strings.TrimSpace(string(out)), nil
}
