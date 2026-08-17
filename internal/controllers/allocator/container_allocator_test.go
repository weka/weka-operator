package allocator

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

// newTestNodeWithDrives builds a fake-client-backed allocator and a Node carrying the given drive
// entries verbatim (no re-sorting) in the weka-full-drives annotation.
func newTestNodeWithDrives(t *testing.T, nodeName string, entries []domain.DriveEntry) (*ContainerResourceAllocator, *corev1.Node) {
	t.Helper()

	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("failed to marshal drive entries: %v", err)
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Annotations: map[string]string{
				consts.AnnotationWekaFullDrives: string(raw),
			},
		},
	}

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add corev1 to scheme: %v", err)
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()

	return &ContainerResourceAllocator{client: fakeClient}, node
}

// A numDrives pin takes a prefix of the returned slice, so it must land on the largest N drives.
func TestGetAvailableDrivesFromStatus_PicksLargestFirst(t *testing.T) {
	ctx := context.Background()

	entries := []domain.DriveEntry{
		{Serial: "small", CapacityGiB: 100},
		{Serial: "largest", CapacityGiB: 500},
		{Serial: "medium", CapacityGiB: 300},
		{Serial: "smallest", CapacityGiB: 50},
		{Serial: "large", CapacityGiB: 400},
	}

	a, node := newTestNodeWithDrives(t, "node-1", entries)

	drives, err := a.getAvailableDrivesFromStatus(ctx, node, map[string]bool{})
	if err != nil {
		t.Fatalf("getAvailableDrivesFromStatus failed: %v", err)
	}

	expected := []string{"largest", "large", "medium", "small", "smallest"}
	if len(drives) != len(expected) {
		t.Fatalf("expected %d drives, got %d: %v", len(expected), len(drives), drives)
	}
	for i, serial := range expected {
		if drives[i] != serial {
			t.Errorf("position %d: expected serial %q, got %q (full order: %v)", i, serial, drives[i], drives)
		}
	}

	// Must pin "largest" (500) and "large" (400).
	const numDrives = 2
	if len(drives) < numDrives {
		t.Fatalf("not enough drives for prefix check")
	}
	picked := drives[:numDrives]
	if picked[0] != "largest" || picked[1] != "large" {
		t.Errorf("numDrives=%d pin should select the largest drives, got %v", numDrives, picked)
	}
}

// Equal-capacity drives must resolve to the same order (serial ascending) regardless of annotation order.
func TestGetAvailableDrivesFromStatus_DeterministicTiebreak(t *testing.T) {
	ctx := context.Background()

	permutationA := []domain.DriveEntry{
		{Serial: "C", CapacityGiB: 200},
		{Serial: "A", CapacityGiB: 200},
		{Serial: "B", CapacityGiB: 200},
	}
	permutationB := []domain.DriveEntry{
		{Serial: "B", CapacityGiB: 200},
		{Serial: "C", CapacityGiB: 200},
		{Serial: "A", CapacityGiB: 200},
	}

	aAllocator, nodeA := newTestNodeWithDrives(t, "node-a", permutationA)
	drivesA, err := aAllocator.getAvailableDrivesFromStatus(ctx, nodeA, map[string]bool{})
	if err != nil {
		t.Fatalf("getAvailableDrivesFromStatus failed for permutation A: %v", err)
	}

	bAllocator, nodeB := newTestNodeWithDrives(t, "node-b", permutationB)
	drivesB, err := bAllocator.getAvailableDrivesFromStatus(ctx, nodeB, map[string]bool{})
	if err != nil {
		t.Fatalf("getAvailableDrivesFromStatus failed for permutation B: %v", err)
	}

	expected := []string{"A", "B", "C"}
	for i, serial := range expected {
		if drivesA[i] != serial {
			t.Errorf("permutation A position %d: expected %q, got %q (full: %v)", i, serial, drivesA[i], drivesA)
		}
		if drivesB[i] != serial {
			t.Errorf("permutation B position %d: expected %q, got %q (full: %v)", i, serial, drivesB[i], drivesB)
		}
	}
}

// Allocated drives must be excluded even after the remaining drives are sorted largest-first.
func TestGetAvailableDrivesFromStatus_FiltersAllocated(t *testing.T) {
	ctx := context.Background()

	entries := []domain.DriveEntry{
		{Serial: "small", CapacityGiB: 100},
		{Serial: "largest", CapacityGiB: 500},
		{Serial: "medium", CapacityGiB: 300},
	}

	a, node := newTestNodeWithDrives(t, "node-1", entries)

	drives, err := a.getAvailableDrivesFromStatus(ctx, node, map[string]bool{"largest": true})
	if err != nil {
		t.Fatalf("getAvailableDrivesFromStatus failed: %v", err)
	}

	expected := []string{"medium", "small"}
	if len(drives) != len(expected) {
		t.Fatalf("expected %d drives, got %d: %v", len(expected), len(drives), drives)
	}
	for i, serial := range expected {
		if drives[i] != serial {
			t.Errorf("position %d: expected serial %q, got %q (full order: %v)", i, serial, drives[i], drives)
		}
	}
}
