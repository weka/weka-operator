package operations

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func readyNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
	}
}

// TestGetTargetNodes_NodeEligibility exercises GetTargetNodes' node filtering, which now runs entirely
// through resources.NodeIneligibleReason: cordoned and untolerated-taint nodes are excluded like before,
// and a node reporting no NodeReady condition at all (not just NodeReady=False) is excluded too.
func TestGetTargetNodes_NodeEligibility(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	cordoned := readyNode("cordoned")
	cordoned.Spec.Unschedulable = true

	noReadyCondition := readyNode("no-ready-condition")
	noReadyCondition.Status.Conditions = nil

	notReady := readyNode("not-ready")
	notReady.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}

	eligible := readyNode("eligible")

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cordoned, noReadyCondition, notReady, eligible).Build()

	got, err := GetTargetNodes(context.Background(), fakeClient, nil, nil)
	if err != nil {
		t.Fatalf("GetTargetNodes: %v", err)
	}

	var names []string
	for _, n := range got {
		names = append(names, n.Name)
	}
	if len(names) != 1 || names[0] != "eligible" {
		t.Errorf("GetTargetNodes returned %v, want only [eligible]", names)
	}
}
