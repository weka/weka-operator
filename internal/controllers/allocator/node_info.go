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
	"github.com/weka/weka-operator/internal/pkg/domain"
)

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
		nodeInfo.SharedDrives = []domain.SharedDriveInfo{}

		// blockedDriveSerials is used for both exclusive drives and shared drives filtering
		blockedDriveSerials := []string{}

		// get from annotations, all serial ids minus blocked-drives serial ids
		// Note: this is for exclusive drive allocation mode only
		allDrivesStr, ok := node.Annotations[consts.AnnotationWekaDrives]
		if ok {
			blockedDrivesStr, ok := node.Annotations[consts.AnnotationBlockedDrives]
			if !ok {
				blockedDrivesStr = "[]"
			}
			// blockedDrivesStr is json list, unwrap it
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
		} else {
			// No exclusive drives annotation - set empty list
			// This is expected in drive-sharing/proxy mode where we only use shared drives
			nodeInfo.AvailableDrives = []string{}
		}

		var sharedDrives []domain.SharedDriveInfo
		// Parse shared drives if present (drive sharing / proxy mode)
		sharedDrivesStr, ok := node.Annotations[consts.AnnotationSharedDrives]
		if ok {
			err = json.Unmarshal([]byte(sharedDrivesStr), &sharedDrives)
			if err != nil {
				err = fmt.Errorf("failed to unmarshal shared-drives: %v", err)
				return
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

// filterBlockedSharedDrives removes blocked drives from the list
// blockedUUIDs is a list of virtual UUIDs that are blocked (via shared drive annotation or drive serials)
func filterBlockedSharedDrives(drives []domain.SharedDriveInfo, blockedDrivePhysicalUUIDs, blockedDriveSerials []string) []domain.SharedDriveInfo {
	if len(blockedDrivePhysicalUUIDs) == 0 && len(blockedDriveSerials) == 0 {
		return drives
	}

	filtered := make([]domain.SharedDriveInfo, 0, len(drives))
	for _, drive := range drives {
		if !slices.Contains(blockedDrivePhysicalUUIDs, drive.PhysicalUUID) && !slices.Contains(blockedDriveSerials, drive.Serial) {
			filtered = append(filtered, drive)
		}
	}
	return filtered
}
