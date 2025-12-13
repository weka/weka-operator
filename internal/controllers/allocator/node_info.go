package allocator

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
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

type NodeInfoGetter func(ctx context.Context, nodeName weka.NodeName) (*AllocatorNodeInfo, error)

func NewK8sNodeInfoGetter(k8sClient client.Client) NodeInfoGetter {
	return func(ctx context.Context, nodeName weka.NodeName) (nodeInfo *AllocatorNodeInfo, err error) {
		node := &v1.Node{}
		err = k8sClient.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node)
		if err != nil {
			return
		}

		nodeInfo = &AllocatorNodeInfo{}
		// initialize shared drives slice
		nodeInfo.SharedDrives = []SharedDriveInfo{}

		// get from annotations, all serial ids minus blocked-drives serial ids
		allDrivesStr, ok := node.Annotations[consts.AnnotationWekaDrives]
		if !ok {
			nodeInfo.AvailableDrives = []string{}
			return
		}
		blockedDrivesStr, ok := node.Annotations[consts.AnnotationBlockedDrives]
		if !ok {
			blockedDrivesStr = "[]"
		}
		// blockedDrivesStr is json list, unwrap it
		blockedDriveSerials := []string{}
		err = json.Unmarshal([]byte(blockedDrivesStr), &blockedDriveSerials)
		if err != nil {
			err = fmt.Errorf("failed to unmarshal blocked-drives: %v", err)
			return
		}

		availableDrives := []string{}
		allDrives := []string{}
		err = json.Unmarshal([]byte(allDrivesStr), &allDrives)
		if err != nil {
			err = fmt.Errorf("failed to unmarshal weka-drives: %v", err)
			return
		}

		for _, drive := range allDrives {
			if !slices.Contains(blockedDriveSerials, drive) {
				availableDrives = append(availableDrives, drive)
			}
		}

		nodeInfo.AvailableDrives = availableDrives

		// Parse shared drives if present (drive sharing / proxy mode)
		sharedDrivesStr, ok := node.Annotations[consts.AnnotationSharedDrives]
		if ok {
			sharedDrives, err := parseSharedDrives(sharedDrivesStr)
			if err != nil {
				err = fmt.Errorf("failed to parse shared-drives annotation: %w", err)
				return nodeInfo, err
			}

			// Filter out blocked shared drives
			blockedSharedDrivesStr, ok := node.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids]
			if ok {
				blockedSharedDrives := []string{}
				if err := json.Unmarshal([]byte(blockedSharedDrivesStr), &blockedSharedDrives); err != nil {
					err = fmt.Errorf("failed to unmarshal blocked-shared-drives: %w", err)
					return nodeInfo, err
				}
				sharedDrives = filterBlockedSharedDrives(sharedDrives, blockedSharedDrives, blockedDriveSerials)
			}

			nodeInfo.SharedDrives = sharedDrives
		}

		return
	}
}

// parseSharedDrives parses the weka.io/shared-drives annotation
// Expected format: [[uuid, serial, capacityGiB, devicePath], ...]
// Example: [["550e8400-...", "SERIAL123", 7000, "/dev/nvme0n1"], ["550e8400-...", "SERIAL456", 14000, "/dev/nvme1n1"]]
func parseSharedDrives(annotationValue string) ([]SharedDriveInfo, error) {
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

// filterBlockedSharedDrives removes blocked drives from the list
// blockedUUIDs is a list of virtual UUIDs that are blocked (via shared drive annotation or drive serials)
func filterBlockedSharedDrives(drives []SharedDriveInfo, blockedDrivePhysicalUUIDs, blockedDriveSerials []string) []SharedDriveInfo {
	if len(blockedDrivePhysicalUUIDs) == 0 && len(blockedDriveSerials) == 0 {
		return drives
	}

	filtered := make([]SharedDriveInfo, 0, len(drives))
	for _, drive := range drives {
		if !slices.Contains(blockedDrivePhysicalUUIDs, drive.PhysicalUUID) && !slices.Contains(blockedDriveSerials, drive.Serial) {
			filtered = append(filtered, drive)
		}
	}
	return filtered
}
