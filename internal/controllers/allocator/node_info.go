package allocator

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/kubernetes"
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

			// Parse as new []DriveEntry format only — old []string format is an error
			// (sign-drives will convert old format on its next run)
			var allEntries []domain.DriveEntry
			err = json.Unmarshal([]byte(allDrivesStr), &allEntries)
			if err != nil {
				err = fmt.Errorf("failed to unmarshal weka-drives as DriveEntry format (old format pending migration): %v", err)
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
			// No exclusive drives annotation - set empty list
			// This is expected in drive-sharing/proxy mode where we only use shared drives
			nodeInfo.AvailableDrives = []domain.DriveEntry{}
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

const maxNodeSample = 3

// computeMaxNodeDriveCapacity samples up to maxNodeSample nodes matching the selector,
// computes the top-numDrives capacity sum per node, and returns the maximum.
// This represents the worst-case (most memory) capacity a single drive container could manage.
func computeMaxNodeDriveCapacity(ctx context.Context, k8sClient client.Client, nodeSelector map[string]string, numDrives int) (int, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "computeMaxNodeDriveCapacity", "nodeSelector", nodeSelector, "numDrives", numDrives)
	defer end()

	kubeService := kubernetes.NewKubeService(k8sClient)
	nodes, err := kubeService.GetNodes(ctx, nodeSelector)
	if err != nil {
		return 0, err
	}

	sampleSize := min(len(nodes), maxNodeSample)

	maxCapacity := 0
	for i := range sampleSize {
		node := nodes[i]
		drivesStr, ok := node.Annotations[consts.AnnotationWekaDrives]
		if !ok || drivesStr == "" {
			continue
		}
		entries, _, err := domain.ParseDriveEntries(drivesStr)
		if err != nil {
			continue
		}

		// Sort capacities descending, take top numDrives
		capacities := make([]int, 0, len(entries))
		for _, e := range entries {
			if e.CapacityGiB > 0 {
				capacities = append(capacities, e.CapacityGiB)
			}
		}
		slices.SortFunc(capacities, func(a, b int) int { return b - a }) // descending

		nodeSum := 0
		for j := 0; j < min(numDrives, len(capacities)); j++ {
			nodeSum += capacities[j]
		}
		maxCapacity = max(maxCapacity, nodeSum)
	}

	logger.Info("Computed max node drive capacity", "maxCapacityGiB", maxCapacity)

	return maxCapacity, nil
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
