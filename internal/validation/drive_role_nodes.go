package validation

import (
	"context"
	"fmt"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// listDriveRoleNodes lists nodes matching cluster's drive-role nodeSelector — the List step shared by
// every drive validator. On failure it returns a field.InternalError against fldPath; whether to
// surface or discard that error is the caller's choice, not a parameter here.
func listDriveRoleNodes(ctx context.Context, c client.Client, cluster *weka.WekaCluster, fldPath *field.Path) ([]corev1.Node, field.ErrorList) {
	selector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeDrive)
	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes, client.MatchingLabels(selector)); err != nil {
		return nil, field.ErrorList{field.InternalError(fldPath, fmt.Errorf("listing drive-role nodes: %w", err))}
	}
	return nodes.Items, nil
}

// driveRoleNodeInfo pairs a drive-role node with its parsed AllocatorNodeInfo.
type driveRoleNodeInfo struct {
	Node *corev1.Node
	Info *allocator.AllocatorNodeInfo
}

// driveRoleNodeInfos parses AllocatorNodeInfo for each already-fetched node, using
// ParseAllocatorNodeInfo directly instead of re-fetching each one by name.
func driveRoleNodeInfos(nodes []corev1.Node, fldPath *field.Path) ([]driveRoleNodeInfo, field.ErrorList) {
	out := make([]driveRoleNodeInfo, 0, len(nodes))
	for i := range nodes {
		info, err := allocator.ParseAllocatorNodeInfo(&nodes[i])
		if err != nil {
			return nil, field.ErrorList{field.InternalError(fldPath, fmt.Errorf("reading drive info for node %q: %w", nodes[i].Name, err))}
		}
		out = append(out, driveRoleNodeInfo{Node: &nodes[i], Info: info})
	}
	return out, nil
}
