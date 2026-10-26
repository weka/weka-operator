package resources

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func readyNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
	}
}

func TestNodeIsReady(t *testing.T) {
	tests := []struct {
		name string
		node *corev1.Node
		want bool
	}{
		{name: "nil node", node: nil, want: false},
		{
			name: "NodeReady=True",
			node: readyNode("n1"),
			want: true,
		},
		{
			name: "NodeReady=False",
			node: &corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}},
			want: false,
		},
		{
			name: "no conditions at all: kubelet has not reported readiness yet",
			node: &corev1.Node{},
			want: false,
		},
		{
			name: "conditions present but no NodeReady entry",
			node: &corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse}}}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NodeIsReady(tt.node); got != tt.want {
				t.Errorf("NodeIsReady() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNodeIneligibleReason covers every classification NodeIneligibleReason makes, table-driven since each
// row is the same shape: build a node, call the function, compare the reason string. This is the single
// predicate shared by capacityplanner/inventory (NodeInventory/ExploreNodes) and
// controllers/operations (GetTargetNodes).
func TestNodeIneligibleReason(t *testing.T) {
	gpuTaint := corev1.Taint{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}
	gpuToleration := corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "gpu", Effect: corev1.TaintEffectNoSchedule}

	// A cordon sets Spec.Unschedulable and the matching taint; either one alone counts as cordoned.
	cordon := func(n *corev1.Node) {
		n.Spec.Unschedulable = true
		n.Spec.Taints = []corev1.Taint{CordonTaint()}
	}

	tests := []struct {
		name        string
		mutate      func(n *corev1.Node)
		tolerations []corev1.Toleration
		want        string
	}{
		{name: "all eligible: ready, no taints, not cordoned", mutate: func(n *corev1.Node) {}, want: ""},
		{
			name:   "cordoned: ineligible for a toleration set that does not cover the cordon taint",
			mutate: cordon,
			want:   "cordoned",
		},
		{
			name: "cordoned plus an untolerated taint: the other taint is named, not the cordon",
			mutate: func(n *corev1.Node) {
				cordon(n)
				n.Spec.Taints = append(n.Spec.Taints, gpuTaint)
			},
			tolerations: []corev1.Toleration{GetUnschedulableToleration()},
			want:        "untolerated taint",
		},
		{
			name:        "cordoned: eligible for a toleration set that covers the cordon taint",
			mutate:      cordon,
			tolerations: []corev1.Toleration{GetUnschedulableToleration()},
			want:        "",
		},
		{
			// Unschedulable without the taint: reading taints alone would call this node a valid
			// placement target, which is how a cordon stops holding backends off.
			name:   "cordoned by the flag alone: ineligible, same as a cordon carrying its taint",
			mutate: func(n *corev1.Node) { n.Spec.Unschedulable = true },
			want:   "cordoned",
		},
		{
			name:        "cordoned by the flag alone: eligible for a caller that tolerates the cordon",
			mutate:      func(n *corev1.Node) { n.Spec.Unschedulable = true },
			tolerations: []corev1.Toleration{GetUnschedulableToleration()},
			want:        "",
		},
		{
			// A catch-all a user wrote in rawTolerations for some unrelated taint reaches the backend
			// toleration set through GetWekaPodTolerationsForCluster. It tolerates the cordon taint like
			// any other NoSchedule taint, so matching generically would quietly report cordoned nodes as
			// placement targets to the capacity and daemonset planners.
			name:        "cordoned: a keyless Exists toleration does not clear the cordon",
			mutate:      cordon,
			tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			want:        "cordoned",
		},
		{
			name:        "cordoned: the keyless NoSchedule expansion does not clear the cordon",
			mutate:      cordon,
			tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}},
			want:        "cordoned",
		},
		{
			name:        "cordoned by the flag alone: a keyless Exists toleration does not clear it either",
			mutate:      func(n *corev1.Node) { n.Spec.Unschedulable = true },
			tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			want:        "cordoned",
		},
		{
			name: "not ready: NodeReady=False",
			mutate: func(n *corev1.Node) {
				n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}
			},
			want: "not ready",
		},
		{
			name: "not ready: no NodeReady condition at all",
			mutate: func(n *corev1.Node) {
				n.Status.Conditions = nil
			},
			want: "not ready",
		},
		{
			name:   "untolerated taint: caller's toleration set does not cover the NoSchedule taint",
			mutate: func(n *corev1.Node) { n.Spec.Taints = []corev1.Taint{gpuTaint} },
			want:   "untolerated taint",
		},
		{
			name:        "tolerated taint: caller's toleration set covers it, reads as eligible",
			mutate:      func(n *corev1.Node) { n.Spec.Taints = []corev1.Taint{gpuTaint} },
			tolerations: []corev1.Toleration{gpuToleration},
			want:        "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n1 := readyNode("n1")
			tt.mutate(n1)
			if got := NodeIneligibleReason(n1, tt.tolerations); got != tt.want {
				t.Errorf("NodeIneligibleReason(n1) = %q, want %q", got, tt.want)
			}
		})
	}
}
