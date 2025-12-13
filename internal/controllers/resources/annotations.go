package resources

import (
	"encoding/json"
	"fmt"
)

// SharedDriveInfo represents a shared drive with its metadata from proxy signing
type SharedDriveInfo struct {
	// UUID is the physical drive UUID returned by weka-sign-drive sign proxy
	PhysicalUUID string
	// Serial is the drive serial number
	Serial string
	// CapacityGiB is the total capacity of the drive in GiB
	CapacityGiB int
	// DevicePath is the device path (e.g., /dev/nvme0n1)
	DevicePath string
}

// parseSharedDrives parses the weka.io/shared-drives annotation
// Expected format: [[uuid, serial, capacityGiB, devicePath], ...]
// Example: [["550e8400-...", "SERIAL123", 7000, "/dev/nvme0n1"], ["550e8400-...", "SERIAL456", 14000, "/dev/nvme1n1"]]
func ParseSharedDrives(annotationValue string) ([]SharedDriveInfo, error) {
	// Parse as array of arrays: [[string, string, int, string], ...]
	var rawDrives [][]any
	if err := json.Unmarshal([]byte(annotationValue), &rawDrives); err != nil {
		return nil, fmt.Errorf("failed to unmarshal shared drives JSON: %w", err)
	}

	drives := make([]SharedDriveInfo, 0, len(rawDrives))
	for i, rawDrive := range rawDrives {
		if len(rawDrive) != 4 {
			return nil, fmt.Errorf("drive at index %d has %d fields, expected 4 (uuid, serial, capacity, devicePath)", i, len(rawDrive))
		}

		uuid, ok := rawDrive[0].(string)
		if !ok {
			return nil, fmt.Errorf("drive at index %d: UUID (field 0) is not a string", i)
		}

		serial, ok := rawDrive[1].(string)
		if !ok {
			return nil, fmt.Errorf("drive at index %d: Serial (field 1) is not a string", i)
		}

		// Capacity can be float64 (JSON numbers are float64) or int
		var capacityGiB int
		switch v := rawDrive[2].(type) {
		case float64:
			capacityGiB = int(v)
		case int:
			capacityGiB = v
		default:
			return nil, fmt.Errorf("drive at index %d: Capacity (field 2) is not a number", i)
		}

		devicePath, ok := rawDrive[3].(string)
		if !ok {
			return nil, fmt.Errorf("drive at index %d: DevicePath (field 3) is not a string", i)
		}

		drives = append(drives, SharedDriveInfo{
			PhysicalUUID: uuid,
			Serial:       serial,
			CapacityGiB:  capacityGiB,
			DevicePath:   devicePath,
		})
	}

	return drives, nil
}
