package resources

import (
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/pkg/util"
)

// NodeIsReady reports whether node carries a NodeReady=True condition. A node that has not reported a
// NodeReady condition at all (nil node, or the condition simply absent) reads as NOT ready — it has not
// yet told us it can run pods, so it is not a safe placement target.
func NodeIsReady(node *v1.Node) bool {
	if node == nil {
		return false
	}
	isNodeReady := false
	for _, condition := range node.Status.Conditions {
		if condition.Type == v1.NodeReady && condition.Status == v1.ConditionTrue {
			isNodeReady = true
			break
		}
	}
	return isNodeReady
}

// NodeIneligibleReason reports why a node cannot host a new weka pod right now — cordoned, not ready, or
// carrying a taint outside tolerations — or "" when the node is a valid placement candidate. This is the
// single predicate for "can this node receive a new pod": every caller across the operator and CLI that
// needs this check goes through it, so the classifications can never quietly diverge between call sites.
func NodeIneligibleReason(node *v1.Node, tolerations []v1.Toleration) string {
	if node == nil {
		return "not ready"
	}
	if node.Spec.Unschedulable {
		return "cordoned"
	}
	if !NodeIsReady(node) {
		return "not ready"
	}
	if !util.CheckTolerations(node.Spec.Taints, tolerations, nil) {
		return "untolerated taint"
	}
	return ""
}
