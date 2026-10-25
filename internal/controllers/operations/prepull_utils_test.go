package operations

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/controllers/resources"
)

func readyNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
	}
}

// TestGetTargetNodes_NodeEligibility exercises GetTargetNodes' node filtering, which runs entirely
// through resources.NodeIneligibleReason: untolerated-taint nodes are excluded, a cordoned node is
// excluded only for a caller whose tolerations don't cover the cordon taint, and a node reporting no
// NodeReady condition at all (not just NodeReady=False) is excluded too.
func TestGetTargetNodes_NodeEligibility(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	cordoned := readyNode("cordoned")
	cordoned.Spec.Unschedulable = true
	cordoned.Spec.Taints = []corev1.Taint{resources.CordonTaint()}

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

	// A caller that tolerates the cordon gets the cordoned node too: its pods run there, so its image
	// has to be prepulled there.
	got, err = GetTargetNodes(context.Background(), fakeClient, nil, []corev1.Toleration{resources.GetUnschedulableToleration()})
	if err != nil {
		t.Fatalf("GetTargetNodes: %v", err)
	}

	names = nil
	for _, n := range got {
		names = append(names, n.Name)
	}
	slices.Sort(names)
	if len(names) != 2 || names[0] != "cordoned" || names[1] != "eligible" {
		t.Errorf("GetTargetNodes with cordon toleration returned %v, want [cordoned eligible]", names)
	}
}
