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
// predicate shared by capacityplanner/inventory (NodeInventory/FullDrivesInventory/ExploreNodes) and
// controllers/operations (GetTargetNodes).
func TestNodeIneligibleReason(t *testing.T) {
	gpuTaint := corev1.Taint{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}
	gpuToleration := corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "gpu", Effect: corev1.TaintEffectNoSchedule}

	tests := []struct {
		name        string
		mutate      func(n *corev1.Node)
		tolerations []corev1.Toleration
		want        string
	}{
		{name: "all eligible: ready, no taints, not cordoned", mutate: func(n *corev1.Node) {}, want: ""},
		{
			name:   "cordoned: flagged regardless of readiness or taints",
			mutate: func(n *corev1.Node) { n.Spec.Unschedulable = true },
			want:   "cordoned",
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
