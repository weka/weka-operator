package allocator

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

		// get from annotations, all serial ids minus blocked-drives serial ids
		allDrivesStr, ok := node.Annotations["weka.io/weka-drives"]
		if !ok {
			nodeInfo.AvailableDrives = []string{}
			return
		}
		blockedDrivesStr, ok := node.Annotations["weka.io/blocked-drives"]
		if !ok {
			blockedDrivesStr = "[]"
		}
		// blockedDrivesStr is json list, unwrap it
		blockedDrives := []string{}
		err = json.Unmarshal([]byte(blockedDrivesStr), &blockedDrives)
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
			if !slices.Contains(blockedDrives, drive) {
				availableDrives = append(availableDrives, drive)
			}
		}

		nodeInfo.AvailableDrives = availableDrives
		return
	}
}
