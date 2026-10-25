package resources

import (
	"slices"

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
//
// A cordon is answered by the caller's tolerations, so one that tolerates it (see cordonTolerantModes,
// opt-in through the configuration policy) stays eligible on a cordoned node. It is checked ahead of the
// node's other taints, and against nodeIsCordoned rather than the taint list, because the NoSchedule
// expansion some modes carry tolerates every NoSchedule taint — leaving it to the general taint check
// would let a cordon pass unnoticed. It keeps a reason of its own because "untolerated taint" would send
// a reader hunting for a custom taint that isn't there.
func NodeIneligibleReason(node *v1.Node, tolerations []v1.Toleration) string {
	if node == nil {
		return "not ready"
	}
	if !NodeIsReady(node) {
		return "not ready"
	}
	if nodeIsCordoned(node) && !util.CheckTolerations([]v1.Taint{CordonTaint()}, tolerations, nil) {
		return "cordoned"
	}
	if !util.CheckTolerations(node.Spec.Taints, tolerations, nil) {
		return "untolerated taint"
	}
	return ""
}

// nodeIsCordoned reads Unschedulable as well as the taint: kubectl sets both, but a node carrying only
// the flag must not read as a valid placement target.
func nodeIsCordoned(node *v1.Node) bool {
	return node.Spec.Unschedulable ||
		slices.ContainsFunc(node.Spec.Taints, func(t v1.Taint) bool { return t.Key == CordonTaintKey })
}
