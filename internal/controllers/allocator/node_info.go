package allocator

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

type NodeInfoGetter func(ctx context.Context, nodeName weka.NodeName) (*AllocatorNodeInfo, error)

func NewK8sNodeInfoGetter(k8sClient client.Client) NodeInfoGetter {
	return func(ctx context.Context, nodeName weka.NodeName) (*AllocatorNodeInfo, error) {
		node := &v1.Node{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node); err != nil {
			return nil, err
		}
		return ParseAllocatorNodeInfo(node)
	}
}

// SignedFullDrives reports whether the node carries a full-drives annotation and, if it parses, its
// non-blocked drives, largest first.
func SignedFullDrives(node *v1.Node) (drives []domain.DriveEntry, signed bool, err error) {
	signed = node.Annotations[consts.AnnotationWekaFullDrives] != ""
	if !signed {
		return nil, false, nil
	}
	entries, err := domain.ReadDriveAnnotations(node.Annotations[consts.AnnotationWekaFullDrives])
	if err != nil {
		return nil, signed, fmt.Errorf("failed to read drive annotations: %w", err)
	}
	var blocked []string
	if s, ok := node.Annotations[consts.AnnotationBlockedDrives]; ok {
		if err := json.Unmarshal([]byte(s), &blocked); err != nil {
			return nil, signed, fmt.Errorf("failed to unmarshal blocked-drives: %w", err)
		}
	}
	for _, d := range entries {
		if !slices.Contains(blocked, d.Serial) {
			drives = append(drives, d)
		}
	}
	return domain.SortDriveEntriesDesc(drives), signed, nil
}

// SignedFullDrivesGiB is SignedFullDrives reduced to the drive capacities, largest first.
func SignedFullDrivesGiB(node *v1.Node) (drivesGiB []int, signed bool, err error) {
	drives, signed, err := SignedFullDrives(node)
	for _, d := range drives {
		drivesGiB = append(drivesGiB, d.CapacityGiB)
	}
	return drivesGiB, signed, err
}

// ParseAllocatorNodeInfo builds an AllocatorNodeInfo from an already-fetched node's annotations
// (exclusive drives, shared drives, and blocked-drive filtering). Callers that already hold the node
// object (e.g. from a List) should use this directly instead of re-fetching via NewK8sNodeInfoGetter.
func ParseAllocatorNodeInfo(node *v1.Node) (nodeInfo *AllocatorNodeInfo, err error) {
	nodeInfo = &AllocatorNodeInfo{}
	nodeInfo.SharedDrives = []domain.SharedDriveInfo{}

	// blockedDriveSerials is used for both exclusive drives and shared drives filtering
	blockedDriveSerials := []string{}
	if blockedDrivesStr, ok := node.Annotations[consts.AnnotationBlockedDrives]; ok {
		if err = json.Unmarshal([]byte(blockedDrivesStr), &blockedDriveSerials); err != nil {
			err = fmt.Errorf("failed to unmarshal blocked-drives: %v", err)
			return
		}
	}
	nodeInfo.BlockedDriveCount = len(blockedDriveSerials)

	// Exclusive drive allocation mode only: all serial ids minus blocked-drives serial ids.
	fullAnnotation := node.Annotations[consts.AnnotationWekaFullDrives]
	if fullAnnotation != "" {
		allEntries, readErr := domain.ReadDriveAnnotations(fullAnnotation)
		if readErr != nil {
			err = fmt.Errorf("failed to read drive annotations: %v", readErr)
			return
		}

		availableDrives := make([]domain.DriveEntry, 0, len(allEntries))
		for _, entry := range allEntries {
			if !slices.Contains(blockedDriveSerials, entry.Serial) {
				availableDrives = append(availableDrives, entry)
			}
		}

		nodeInfo.AvailableDrives = availableDrives
	} else {
		// This is expected in drive-sharing/proxy mode where we only use shared drives
		nodeInfo.AvailableDrives = []domain.DriveEntry{}
	}

	var sharedDrives []domain.SharedDriveInfo
	sharedDrivesStr, ok := node.Annotations[consts.AnnotationSharedDrives]
	if ok {
		err = json.Unmarshal([]byte(sharedDrivesStr), &sharedDrives)
		if err != nil {
			err = fmt.Errorf("failed to unmarshal shared-drives: %v", err)
			return
		}

		var blockedSharedDrives []string
		if blockedSharedDrivesStr, ok := node.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids]; ok {
			if err := json.Unmarshal([]byte(blockedSharedDrivesStr), &blockedSharedDrives); err != nil {
				err = fmt.Errorf("failed to unmarshal blocked-shared-drives: %w", err)
				return nodeInfo, err
			}
		}
		sharedDrives = filterBlockedSharedDrives(sharedDrives, blockedSharedDrives, blockedDriveSerials)

		nodeInfo.SharedDrives = sharedDrives
	}

	return
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
