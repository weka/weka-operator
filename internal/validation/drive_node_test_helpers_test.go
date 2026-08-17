package validation

import (
	"encoding/json"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

// driveRoleNode builds a Node with the given labels (for nodeSelector matching). When
// driveCapacitiesGiB is non-nil, the weka.io/weka-full-drives annotation is set with one synthetic
// signed drive entry per capacity value; driveCapacitiesGiB == nil means "sign-drives hasn't run on
// this node yet" (no annotation at all).
func driveRoleNode(t *testing.T, name string, labels map[string]string, driveCapacitiesGiB []int) *corev1.Node {
	t.Helper()
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	}
	if driveCapacitiesGiB != nil {
		entries := make([]domain.DriveEntry, 0, len(driveCapacitiesGiB))
		for i, capGiB := range driveCapacitiesGiB {
			entries = append(entries, domain.DriveEntry{Serial: fmt.Sprintf("%s-d%d", name, i), CapacityGiB: capGiB})
		}
		b, err := json.Marshal(entries)
		if err != nil {
			t.Fatalf("marshal drive entries: %v", err)
		}
		n.Annotations = map[string]string{consts.AnnotationWekaFullDrives: string(b)}
	}
	return n
}

// fakeClientWithNodes builds a fake client.Client seeded with the given Nodes.
func fakeClientWithNodes(t *testing.T, nodes ...*corev1.Node) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme)
	for _, n := range nodes {
		b = b.WithObjects(n)
	}
	return b.Build()
}
