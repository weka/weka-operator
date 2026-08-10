package operations

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

// These tests pin the behaviour of the block/unblock handlers: which annotation each identifier
// writes, the capacity resources it recomputes, whether it clears the sign-drives-hash, and that an
// unrecognised identifier rejects the whole request without writing anything.
//
// Two of them exist because the behaviour they assert was once wrong: the blocked list must survive
// the node write at all (a status write ordered before it used to discard the annotation), and
// unblocking several drives in one request must remove all of them.

func newBlockDrivesTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add corev1 to scheme: %v", err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add weka to scheme: %v", err)
	}
	return scheme
}

func newBlockDrivesTestNode(name string, annotations map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
		},
		Status: corev1.NodeStatus{
			Capacity:    corev1.ResourceList{},
			Allocatable: corev1.ResourceList{},
		},
	}
}

func marshalJSON(t *testing.T, v interface{}) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal %#v: %v", v, err)
	}
	return string(data)
}

func unmarshalStringSlice(t *testing.T, raw string) []string {
	t.Helper()
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("failed to unmarshal %q: %v", raw, err)
	}
	return out
}

func assertQuantity(t *testing.T, list corev1.ResourceList, name string, want int64) {
	t.Helper()
	q, ok := list[corev1.ResourceName(name)]
	if !ok {
		t.Errorf("resource %s not present in %#v", name, list)
		return
	}
	if q.Value() != want {
		t.Errorf("resource %s = %d, want %d", name, q.Value(), want)
	}
}

// --- BlockDrives (serials) ---

func TestBlockDrives_SingleIdentifier_HappyPath(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	allDrives := []domain.DriveEntry{
		{Serial: "SN1", CapacityGiB: 100},
		{Serial: "SN2", CapacityGiB: 200},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationWekaFullDrives: marshalJSON(t, allDrives),
		consts.AnnotationSignDrivesHash: "stale-hash",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", SerialIDs: []string{"SN1"}},
	}
	if err := op.BlockDrives(ctx); err != nil {
		t.Fatalf("BlockDrives returned error: %v", err)
	}

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	blocked := unmarshalStringSlice(t, got.Annotations[consts.AnnotationBlockedDrives])
	if len(blocked) != 1 || blocked[0] != "SN1" {
		t.Errorf("blocked drives = %#v, want [\"SN1\"]", blocked)
	}

	// 2 total, 1 blocked -> 1 available.
	assertQuantity(t, got.Status.Capacity, consts.ResourceDrives, 1)
	assertQuantity(t, got.Status.Allocatable, consts.ResourceDrives, 1)

	if _, ok := got.Annotations[consts.AnnotationSignDrivesHash]; ok {
		t.Errorf("expected %s annotation to be deleted by BlockDrives", consts.AnnotationSignDrivesHash)
	}

	if op.results.Err != "" {
		t.Errorf("results.Err = %q, want empty", op.results.Err)
	}
	if !strings.Contains(op.results.Result, "Successfully blocked 1 drives on node node1") {
		t.Errorf("results.Result = %q, want it to mention blocking 1 drive", op.results.Result)
	}
}

func TestBlockDrives_MultipleIdentifiers_HappyPath(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	allDrives := []domain.DriveEntry{
		{Serial: "SN1", CapacityGiB: 100},
		{Serial: "SN2", CapacityGiB: 200},
		{Serial: "SN3", CapacityGiB: 300},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationWekaFullDrives: marshalJSON(t, allDrives),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", SerialIDs: []string{"SN1", "SN3"}},
	}
	if err := op.BlockDrives(ctx); err != nil {
		t.Fatalf("BlockDrives returned error: %v", err)
	}

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	blocked := unmarshalStringSlice(t, got.Annotations[consts.AnnotationBlockedDrives])
	if len(blocked) != 2 || blocked[0] != "SN1" || blocked[1] != "SN3" {
		t.Errorf("blocked drives = %#v, want [\"SN1\" \"SN3\"] in that order", blocked)
	}

	// 3 total, 2 blocked -> 1 available.
	assertQuantity(t, got.Status.Capacity, consts.ResourceDrives, 1)
	assertQuantity(t, got.Status.Allocatable, consts.ResourceDrives, 1)

	if op.results.Err != "" {
		t.Errorf("results.Err = %q, want empty", op.results.Err)
	}
}

func TestBlockDrives_UnknownIdentifier_ReportsErrAndDoesNotBlock(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	allDrives := []domain.DriveEntry{
		{Serial: "SN1", CapacityGiB: 100},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationWekaFullDrives: marshalJSON(t, allDrives),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	before := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, before); err != nil {
		t.Fatalf("failed to get node before: %v", err)
	}

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", SerialIDs: []string{"UNKNOWN"}},
	}
	if err := op.BlockDrives(ctx); err != nil {
		t.Fatalf("BlockDrives returned error: %v, want nil (not-found is reported via results.Err)", err)
	}

	if op.results.Err == "" {
		t.Errorf("results.Err is empty, want a not-found error")
	}
	if !strings.Contains(op.results.Err, "UNKNOWN") {
		t.Errorf("results.Err = %q, want it to mention UNKNOWN", op.results.Err)
	}
	if op.results.Result != "" {
		t.Errorf("results.Result = %q, want empty on failure", op.results.Result)
	}

	// All-or-nothing: one unrecognised identifier rejects the request before anything is written.
	after := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, after); err != nil {
		t.Fatalf("failed to get node after: %v", err)
	}
	if before.ResourceVersion != after.ResourceVersion {
		t.Errorf("node was written despite the not-found identifier: ResourceVersion %s -> %s", before.ResourceVersion, after.ResourceVersion)
	}
	if _, ok := after.Annotations[consts.AnnotationBlockedDrives]; ok {
		t.Errorf("blocked-drives annotation was written despite the not-found identifier: %q", after.Annotations[consts.AnnotationBlockedDrives])
	}
}

func TestBlockDrives_AlreadyBlocked_Idempotent(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	allDrives := []domain.DriveEntry{
		{Serial: "SN1", CapacityGiB: 100},
		{Serial: "SN2", CapacityGiB: 200},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationWekaFullDrives: marshalJSON(t, allDrives),
		consts.AnnotationBlockedDrives:  marshalJSON(t, []string{"SN1"}),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", SerialIDs: []string{"SN1"}},
	}
	if err := op.BlockDrives(ctx); err != nil {
		t.Fatalf("BlockDrives returned error: %v", err)
	}

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	blocked := unmarshalStringSlice(t, got.Annotations[consts.AnnotationBlockedDrives])
	if len(blocked) != 1 || blocked[0] != "SN1" {
		t.Errorf("blocked drives = %#v, want [\"SN1\"] (no duplicate)", blocked)
	}
	if op.results.Err != "" {
		t.Errorf("results.Err = %q, want empty", op.results.Err)
	}
}

func TestBlockDrives_MissingNode_ReturnsError(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "does-not-exist", SerialIDs: []string{"SN1"}},
	}
	if err := op.BlockDrives(ctx); err == nil {
		t.Fatalf("expected an error for a missing node, got nil")
	}
}

// --- UnblockDrives (serials) ---

func TestUnblockDrives_SingleIdentifier_HappyPath(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	allDrives := []domain.DriveEntry{
		{Serial: "SN1", CapacityGiB: 100},
		{Serial: "SN2", CapacityGiB: 200},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationWekaFullDrives: marshalJSON(t, allDrives),
		consts.AnnotationBlockedDrives:  marshalJSON(t, []string{"SN1"}),
		consts.AnnotationSignDrivesHash: "hash-from-the-last-sign-drives-run",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", SerialIDs: []string{"SN1"}},
	}
	if err := op.UnblockDrives(ctx); err != nil {
		t.Fatalf("UnblockDrives returned error: %v", err)
	}

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	blocked := unmarshalStringSlice(t, got.Annotations[consts.AnnotationBlockedDrives])
	if len(blocked) != 0 {
		t.Errorf("blocked drives = %#v, want empty", blocked)
	}

	// 2 total, 0 blocked -> 2 available.
	assertQuantity(t, got.Status.Capacity, consts.ResourceDrives, 2)
	assertQuantity(t, got.Status.Allocatable, consts.ResourceDrives, 2)

	// UnblockDrives does not touch the sign-drives-hash annotation (unlike BlockDrives).
	if _, ok := got.Annotations[consts.AnnotationSignDrivesHash]; !ok {
		t.Errorf("expected %s annotation to survive UnblockDrives (this handler does not delete it)", consts.AnnotationSignDrivesHash)
	}

	if op.results.Err != "" {
		t.Errorf("results.Err = %q, want empty", op.results.Err)
	}
	if !strings.Contains(op.results.Result, "Successfully unblocked 1 drives on node node1") {
		t.Errorf("results.Result = %q, want it to mention unblocking 1 drive", op.results.Result)
	}
}

func TestUnblockDrives_UnknownIdentifier_ReportsErrAndDoesNotUnblock(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	allDrives := []domain.DriveEntry{
		{Serial: "SN1", CapacityGiB: 100},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationWekaFullDrives: marshalJSON(t, allDrives),
		consts.AnnotationBlockedDrives:  marshalJSON(t, []string{"SN1"}),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	before := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, before); err != nil {
		t.Fatalf("failed to get node before: %v", err)
	}

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", SerialIDs: []string{"UNKNOWN"}},
	}
	if err := op.UnblockDrives(ctx); err != nil {
		t.Fatalf("UnblockDrives returned error: %v, want nil (not-found is reported via results.Err)", err)
	}

	if op.results.Err == "" {
		t.Errorf("results.Err is empty, want a not-found error")
	}
	if !strings.Contains(op.results.Err, "UNKNOWN") {
		t.Errorf("results.Err = %q, want it to mention UNKNOWN", op.results.Err)
	}

	after := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, after); err != nil {
		t.Fatalf("failed to get node after: %v", err)
	}
	if before.ResourceVersion != after.ResourceVersion {
		t.Errorf("node was written despite the not-found identifier: ResourceVersion %s -> %s", before.ResourceVersion, after.ResourceVersion)
	}
	blocked := unmarshalStringSlice(t, after.Annotations[consts.AnnotationBlockedDrives])
	if len(blocked) != 1 || blocked[0] != "SN1" {
		t.Errorf("blocked-drives annotation changed despite the not-found identifier: %#v", blocked)
	}
}

func TestUnblockDrives_MissingNode_ReturnsError(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "does-not-exist", SerialIDs: []string{"SN1"}},
		unblock: true,
	}
	if err := op.UnblockDrives(ctx); err == nil {
		t.Fatalf("expected an error for a missing node, got nil")
	}
}

// Unblocking several drives in one request must remove all of them and leave the rest untouched.
func TestUnblockDrives_MultipleIdentifiers(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	allDrives := []domain.DriveEntry{
		{Serial: "SN1", CapacityGiB: 100},
		{Serial: "SN2", CapacityGiB: 200},
		{Serial: "SN3", CapacityGiB: 300},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationWekaFullDrives: marshalJSON(t, allDrives),
		consts.AnnotationBlockedDrives:  marshalJSON(t, []string{"SN1", "SN2", "SN3"}),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", SerialIDs: []string{"SN1", "SN3"}},
	}
	if err := op.UnblockDrives(ctx); err != nil {
		t.Fatalf("UnblockDrives returned error: %v", err)
	}

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	blocked := unmarshalStringSlice(t, got.Annotations[consts.AnnotationBlockedDrives])
	want := []string{"SN2"}
	if len(blocked) != len(want) {
		t.Fatalf("blocked drives = %#v, want %#v", blocked, want)
	}
	for i := range want {
		if blocked[i] != want[i] {
			t.Errorf("blocked drives = %#v, want %#v", blocked, want)
			break
		}
	}

	if op.results.Err != "" {
		t.Errorf("results.Err = %q, want empty", op.results.Err)
	}
}

// --- BlockSharedDrives (physical UUIDs) ---

func TestBlockSharedDrives_SingleIdentifier_HappyPath(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	sharedDrives := []domain.SharedDriveInfo{
		{PhysicalUUID: "uuid1", Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
		{PhysicalUUID: "uuid2", Serial: "SN2", CapacityGiB: 2000, Type: "QLC"},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives:   marshalJSON(t, sharedDrives),
		consts.AnnotationSignDrivesHash: "stale-hash",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", PhysicalUUIDs: []string{"uuid1"}},
	}
	if err := op.BlockSharedDrives(ctx); err != nil {
		t.Fatalf("BlockSharedDrives returned error: %v", err)
	}

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	blocked := unmarshalStringSlice(t, got.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids])
	if len(blocked) != 1 || blocked[0] != "uuid1" {
		t.Errorf("blocked drive uuids = %#v, want [\"uuid1\"]", blocked)
	}

	// uuid1 (TLC, 1000 GiB) blocked -> TLC 0, QLC 2000 remains.
	assertQuantity(t, got.Status.Capacity, consts.ResourceSharedDrivesCapacity, 0)
	assertQuantity(t, got.Status.Allocatable, consts.ResourceSharedDrivesCapacity, 0)
	assertQuantity(t, got.Status.Capacity, consts.ResourcesSharedDrivesCapacityQLC, 2000)
	assertQuantity(t, got.Status.Allocatable, consts.ResourcesSharedDrivesCapacityQLC, 2000)

	if _, ok := got.Annotations[consts.AnnotationSignDrivesHash]; ok {
		t.Errorf("expected %s annotation to be deleted by BlockSharedDrives", consts.AnnotationSignDrivesHash)
	}

	if op.results.Err != "" {
		t.Errorf("results.Err = %q, want empty", op.results.Err)
	}
	if !strings.Contains(op.results.Result, "Successfully blocked 1 drives on node node1") {
		t.Errorf("results.Result = %q, want it to mention blocking 1 drive", op.results.Result)
	}
}

func TestBlockSharedDrives_MultipleIdentifiers_HappyPath(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	sharedDrives := []domain.SharedDriveInfo{
		{PhysicalUUID: "uuid1", Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
		{PhysicalUUID: "uuid2", Serial: "SN2", CapacityGiB: 2000, Type: "QLC"},
		{PhysicalUUID: "uuid3", Serial: "SN3", CapacityGiB: 3000, Type: "TLC"},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives: marshalJSON(t, sharedDrives),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", PhysicalUUIDs: []string{"uuid1", "uuid2"}},
	}
	if err := op.BlockSharedDrives(ctx); err != nil {
		t.Fatalf("BlockSharedDrives returned error: %v", err)
	}

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	blocked := unmarshalStringSlice(t, got.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids])
	if len(blocked) != 2 || blocked[0] != "uuid1" || blocked[1] != "uuid2" {
		t.Errorf("blocked drive uuids = %#v, want [\"uuid1\" \"uuid2\"] in that order", blocked)
	}

	// uuid1 (TLC 1000) and uuid2 (QLC 2000) blocked -> only uuid3 (TLC 3000) remains.
	assertQuantity(t, got.Status.Capacity, consts.ResourceSharedDrivesCapacity, 3000)
	assertQuantity(t, got.Status.Capacity, consts.ResourcesSharedDrivesCapacityQLC, 0)

	if op.results.Err != "" {
		t.Errorf("results.Err = %q, want empty", op.results.Err)
	}
}

func TestBlockSharedDrives_UnknownIdentifier_ReportsErrAndDoesNotBlock(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	sharedDrives := []domain.SharedDriveInfo{
		{PhysicalUUID: "uuid1", Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives: marshalJSON(t, sharedDrives),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	before := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, before); err != nil {
		t.Fatalf("failed to get node before: %v", err)
	}

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", PhysicalUUIDs: []string{"unknown-uuid"}},
	}
	if err := op.BlockSharedDrives(ctx); err != nil {
		t.Fatalf("BlockSharedDrives returned error: %v, want nil (not-found is reported via results.Err)", err)
	}

	if op.results.Err == "" {
		t.Errorf("results.Err is empty, want a not-found error")
	}
	if !strings.Contains(op.results.Err, "unknown-uuid") {
		t.Errorf("results.Err = %q, want it to mention unknown-uuid", op.results.Err)
	}

	after := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, after); err != nil {
		t.Fatalf("failed to get node after: %v", err)
	}
	if before.ResourceVersion != after.ResourceVersion {
		t.Errorf("node was written despite the not-found identifier: ResourceVersion %s -> %s", before.ResourceVersion, after.ResourceVersion)
	}
	if _, ok := after.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids]; ok {
		t.Errorf("blocked-drives-physical-uuids annotation was written despite the not-found identifier: %q", after.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids])
	}
}

func TestBlockSharedDrives_AlreadyBlocked_Idempotent(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	sharedDrives := []domain.SharedDriveInfo{
		{PhysicalUUID: "uuid1", Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
		{PhysicalUUID: "uuid2", Serial: "SN2", CapacityGiB: 2000, Type: "QLC"},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives:               marshalJSON(t, sharedDrives),
		consts.AnnotationBlockedDrivesPhysicalUuids: marshalJSON(t, []string{"uuid1"}),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", PhysicalUUIDs: []string{"uuid1"}},
	}
	if err := op.BlockSharedDrives(ctx); err != nil {
		t.Fatalf("BlockSharedDrives returned error: %v", err)
	}

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	blocked := unmarshalStringSlice(t, got.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids])
	if len(blocked) != 1 || blocked[0] != "uuid1" {
		t.Errorf("blocked drive uuids = %#v, want [\"uuid1\"] (no duplicate)", blocked)
	}
	if op.results.Err != "" {
		t.Errorf("results.Err = %q, want empty", op.results.Err)
	}
}

func TestBlockSharedDrives_MissingNode_ReturnsError(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "does-not-exist", PhysicalUUIDs: []string{"uuid1"}},
	}
	if err := op.BlockSharedDrives(ctx); err == nil {
		t.Fatalf("expected an error for a missing node, got nil")
	}
}

// TestBlockSharedDrives_ExcludesSerialBlockedCapacity covers the cross-annotation exclusion
// documented at block_drives.go:310-317: a drive already blocked by serial (weka.io/blocked-drives)
// must not have its capacity counted here, even though this handler only writes the physical-uuid
// annotation.
func TestBlockSharedDrives_ExcludesSerialBlockedCapacity(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	sharedDrives := []domain.SharedDriveInfo{
		{PhysicalUUID: "uuid1", Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
		{PhysicalUUID: "uuid2", Serial: "SN2", CapacityGiB: 2000, Type: "TLC"},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives:  marshalJSON(t, sharedDrives),
		consts.AnnotationBlockedDrives: marshalJSON(t, []string{"SN1"}), // blocked by serial already
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", PhysicalUUIDs: []string{"uuid2"}},
	}
	if err := op.BlockSharedDrives(ctx); err != nil {
		t.Fatalf("BlockSharedDrives returned error: %v", err)
	}

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	// SN1 excluded (serial-blocked) and uuid2 excluded (uuid-blocked by this call) -> 0 TLC left.
	assertQuantity(t, got.Status.Capacity, consts.ResourceSharedDrivesCapacity, 0)
	assertQuantity(t, got.Status.Allocatable, consts.ResourceSharedDrivesCapacity, 0)
}

// --- UnblockSharedDrives (physical UUIDs) ---

func TestUnblockSharedDrives_SingleIdentifier_HappyPath(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	sharedDrives := []domain.SharedDriveInfo{
		{PhysicalUUID: "uuid1", Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
		{PhysicalUUID: "uuid2", Serial: "SN2", CapacityGiB: 2000, Type: "QLC"},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives:               marshalJSON(t, sharedDrives),
		consts.AnnotationBlockedDrivesPhysicalUuids: marshalJSON(t, []string{"uuid1"}),
		consts.AnnotationSignDrivesHash:             "stale-hash",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", PhysicalUUIDs: []string{"uuid1"}},
	}
	if err := op.UnblockSharedDrives(ctx); err != nil {
		t.Fatalf("UnblockSharedDrives returned error: %v", err)
	}

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	blocked := unmarshalStringSlice(t, got.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids])
	if len(blocked) != 0 {
		t.Errorf("blocked drive uuids = %#v, want empty", blocked)
	}

	// Nothing blocked anymore -> both TLC (uuid1, 1000) and QLC (uuid2, 2000) fully counted.
	assertQuantity(t, got.Status.Capacity, consts.ResourceSharedDrivesCapacity, 1000)
	assertQuantity(t, got.Status.Allocatable, consts.ResourceSharedDrivesCapacity, 1000)
	assertQuantity(t, got.Status.Capacity, consts.ResourcesSharedDrivesCapacityQLC, 2000)
	assertQuantity(t, got.Status.Allocatable, consts.ResourcesSharedDrivesCapacityQLC, 2000)

	// UnblockSharedDrives does not touch the sign-drives-hash annotation (unlike BlockSharedDrives).
	if _, ok := got.Annotations[consts.AnnotationSignDrivesHash]; !ok {
		t.Errorf("expected %s annotation to survive UnblockSharedDrives (this handler does not delete it)", consts.AnnotationSignDrivesHash)
	}

	if op.results.Err != "" {
		t.Errorf("results.Err = %q, want empty", op.results.Err)
	}
	if !strings.Contains(op.results.Result, "Successfully unblocked 1 drives on node node1") {
		t.Errorf("results.Result = %q, want it to mention unblocking 1 drive", op.results.Result)
	}
}

func TestUnblockSharedDrives_UnknownIdentifier_ReportsErrAndDoesNotUnblock(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	sharedDrives := []domain.SharedDriveInfo{
		{PhysicalUUID: "uuid1", Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives:               marshalJSON(t, sharedDrives),
		consts.AnnotationBlockedDrivesPhysicalUuids: marshalJSON(t, []string{"uuid1"}),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	before := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, before); err != nil {
		t.Fatalf("failed to get node before: %v", err)
	}

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", PhysicalUUIDs: []string{"unknown-uuid"}},
	}
	if err := op.UnblockSharedDrives(ctx); err != nil {
		t.Fatalf("UnblockSharedDrives returned error: %v, want nil (not-found is reported via results.Err)", err)
	}

	if op.results.Err == "" {
		t.Errorf("results.Err is empty, want a not-found error")
	}
	if !strings.Contains(op.results.Err, "unknown-uuid") {
		t.Errorf("results.Err = %q, want it to mention unknown-uuid", op.results.Err)
	}

	after := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, after); err != nil {
		t.Fatalf("failed to get node after: %v", err)
	}
	if before.ResourceVersion != after.ResourceVersion {
		t.Errorf("node was written despite the not-found identifier: ResourceVersion %s -> %s", before.ResourceVersion, after.ResourceVersion)
	}
	blocked := unmarshalStringSlice(t, after.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids])
	if len(blocked) != 1 || blocked[0] != "uuid1" {
		t.Errorf("blocked-drives-physical-uuids annotation changed despite the not-found identifier: %#v", blocked)
	}
}

func TestUnblockSharedDrives_MissingNode_ReturnsError(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "does-not-exist", PhysicalUUIDs: []string{"uuid1"}},
		unblock: true,
	}
	if err := op.UnblockSharedDrives(ctx); err == nil {
		t.Fatalf("expected an error for a missing node, got nil")
	}
}

// Unblocking every blocked physical UUID in one request must leave the list empty.
func TestUnblockSharedDrives_MultipleIdentifiers(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	sharedDrives := []domain.SharedDriveInfo{
		{PhysicalUUID: "uuid1", Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
		{PhysicalUUID: "uuid2", Serial: "SN2", CapacityGiB: 2000, Type: "QLC"},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives:               marshalJSON(t, sharedDrives),
		consts.AnnotationBlockedDrivesPhysicalUuids: marshalJSON(t, []string{"uuid1", "uuid2"}),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", PhysicalUUIDs: []string{"uuid1", "uuid2"}},
	}
	if err := op.UnblockSharedDrives(ctx); err != nil {
		t.Fatalf("UnblockSharedDrives returned error: %v", err)
	}

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	blocked := unmarshalStringSlice(t, got.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids])
	if len(blocked) != 0 {
		t.Errorf("blocked drive uuids = %#v, want empty", blocked)
	}

	// Nothing blocked, so both types publish their full capacity.
	assertQuantity(t, got.Status.Capacity, consts.ResourceSharedDrivesCapacity, 1000)
	assertQuantity(t, got.Status.Capacity, consts.ResourcesSharedDrivesCapacityQLC, 2000)

	if op.results.Err != "" {
		t.Errorf("results.Err = %q, want empty", op.results.Err)
	}
}

// --- Virtual UUIDs (OP-371) ---

func newVidTestContainer(name, node string, vids ...weka.VirtualDrive) *weka.WekaContainer {
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       weka.WekaContainerSpec{NodeAffinity: weka.NodeName(node)},
	}
	if len(vids) > 0 {
		container.Status.Allocations = &weka.ContainerAllocations{VirtualDrives: vids}
	}
	return container
}

func TestBuildNodeClaimedVids(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	onNode1a := newVidTestContainer("drives0", "node1",
		weka.VirtualDrive{VirtualUUID: "vid-1", PhysicalUUID: "phys-a"},
		weka.VirtualDrive{VirtualUUID: "vid-2", PhysicalUUID: "phys-a"},
	)
	onNode1b := newVidTestContainer("drives1", "node1",
		weka.VirtualDrive{VirtualUUID: "vid-3", PhysicalUUID: "phys-b"},
	)
	onNode2 := newVidTestContainer("drives2", "node2",
		weka.VirtualDrive{VirtualUUID: "vid-elsewhere", PhysicalUUID: "phys-c"},
	)
	noAllocations := newVidTestContainer("compute0", "node1")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(onNode1a, onNode1b, onNode2, noAllocations).Build()

	op := &BlockDrivesOperation{client: c, payload: &weka.BlockDrivesPayload{Node: "node1"}}
	claimed, err := op.buildNodeClaimedVids(context.Background(), "node1")
	if err != nil {
		t.Fatalf("buildNodeClaimedVids returned error: %v", err)
	}

	// Unions every container on the node, skips nil allocations, excludes other nodes.
	want := map[string]bool{"vid-1": true, "vid-2": true, "vid-3": true}
	if len(claimed) != len(want) {
		t.Fatalf("claimed = %#v, want the 3 VIDs on node1", claimed)
	}
	for _, vid := range claimed {
		if !want[vid] {
			t.Errorf("claimed contains %q, which is not allocated on node1", vid)
		}
	}
}

func TestBlockVirtualDrives_HappyPath(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	sharedDrives := []domain.SharedDriveInfo{
		{PhysicalUUID: "phys-a", Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives:   marshalJSON(t, sharedDrives),
		consts.AnnotationSignDrivesHash: "hash-from-the-last-sign-drives-run",
	})
	container := newVidTestContainer("drives0", "node1",
		weka.VirtualDrive{VirtualUUID: "vid-1", PhysicalUUID: "phys-a"},
		weka.VirtualDrive{VirtualUUID: "vid-2", PhysicalUUID: "phys-a"},
	)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).
		WithObjects(node, container).Build()
	ctx := context.Background()

	before := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, before); err != nil {
		t.Fatalf("failed to get node before: %v", err)
	}

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", VirtualUUIDs: []string{"vid-1"}},
	}
	if err := op.BlockVirtualDrives(ctx); err != nil {
		t.Fatalf("BlockVirtualDrives returned error: %v", err)
	}

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	blocked := unmarshalStringSlice(t, got.Annotations[consts.AnnotationBlockedDrivesVirtualUuids])
	if len(blocked) != 1 || blocked[0] != "vid-1" {
		t.Errorf("blocked virtual uuids = %#v, want [\"vid-1\"]", blocked)
	}

	// A virtual-UUID block leaves the node's physical inventory alone, so neither the capacity
	// resources nor the sign-drives hash may move.
	if !reflect.DeepEqual(before.Status.Capacity, got.Status.Capacity) {
		t.Errorf("Status.Capacity changed: %#v -> %#v", before.Status.Capacity, got.Status.Capacity)
	}
	if !reflect.DeepEqual(before.Status.Allocatable, got.Status.Allocatable) {
		t.Errorf("Status.Allocatable changed: %#v -> %#v", before.Status.Allocatable, got.Status.Allocatable)
	}
	if got.Annotations[consts.AnnotationSignDrivesHash] != "hash-from-the-last-sign-drives-run" {
		t.Errorf("%s = %q, want it untouched", consts.AnnotationSignDrivesHash, got.Annotations[consts.AnnotationSignDrivesHash])
	}

	if op.results.Err != "" {
		t.Errorf("results.Err = %q, want empty", op.results.Err)
	}
	if !strings.Contains(op.results.Result, "Successfully blocked 1 virtual drives on node node1") {
		t.Errorf("results.Result = %q, want it to mention blocking 1 virtual drive", op.results.Result)
	}
}

func TestBlockVirtualDrives_UnknownVidRejectsWholeRequest(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	node := newBlockDrivesTestNode("node1", map[string]string{})
	container := newVidTestContainer("drives0", "node1",
		weka.VirtualDrive{VirtualUUID: "vid-1", PhysicalUUID: "phys-a"},
	)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).
		WithObjects(node, container).Build()
	ctx := context.Background()

	before := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, before); err != nil {
		t.Fatalf("failed to get node before: %v", err)
	}

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", VirtualUUIDs: []string{"vid-1", "vid-unknown"}},
	}
	if err := op.BlockVirtualDrives(ctx); err != nil {
		t.Fatalf("BlockVirtualDrives returned error: %v, want nil (not-found is reported via results.Err)", err)
	}

	if !strings.Contains(op.results.Err, "vid-unknown") {
		t.Errorf("results.Err = %q, want it to name vid-unknown", op.results.Err)
	}
	if !strings.Contains(op.results.Err, "clean-stale-virtual-drives") {
		t.Errorf("results.Err = %q, want it to point at clean-stale-virtual-drives", op.results.Err)
	}

	// All-or-nothing: the known VID must not be blocked either.
	after := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, after); err != nil {
		t.Fatalf("failed to get node after: %v", err)
	}
	if before.ResourceVersion != after.ResourceVersion {
		t.Errorf("node was written despite an unknown VID: ResourceVersion %s -> %s", before.ResourceVersion, after.ResourceVersion)
	}
	if _, ok := after.Annotations[consts.AnnotationBlockedDrivesVirtualUuids]; ok {
		t.Errorf("blocked virtual uuids annotation was written despite an unknown VID: %q", after.Annotations[consts.AnnotationBlockedDrivesVirtualUuids])
	}
}

func TestBlockVirtualDrives_AlreadyBlockedIsIdempotent(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationBlockedDrivesVirtualUuids: marshalJSON(t, []string{"vid-1"}),
	})
	container := newVidTestContainer("drives0", "node1",
		weka.VirtualDrive{VirtualUUID: "vid-1", PhysicalUUID: "phys-a"},
	)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).
		WithObjects(node, container).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", VirtualUUIDs: []string{"vid-1"}},
	}
	if err := op.BlockVirtualDrives(ctx); err != nil {
		t.Fatalf("BlockVirtualDrives returned error: %v", err)
	}

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}
	blocked := unmarshalStringSlice(t, got.Annotations[consts.AnnotationBlockedDrivesVirtualUuids])
	if len(blocked) != 1 || blocked[0] != "vid-1" {
		t.Errorf("blocked virtual uuids = %#v, want no duplicate entry", blocked)
	}
}

func TestUnblockVirtualDrives_BatchAndRetiredVids(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationBlockedDrivesVirtualUuids: marshalJSON(t, []string{"vid-1", "vid-2", "vid-3"}),
	})
	// No container claims any of them: a retired VID exists nowhere any more, and unblocking it
	// must still work or the drives this operation just replaced could never be cleaned up.
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).
		WithObjects(node).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		unblock: true,
		payload: &weka.BlockDrivesPayload{Node: "node1", VirtualUUIDs: []string{"vid-1", "vid-3"}},
	}
	if err := op.UnblockVirtualDrives(ctx); err != nil {
		t.Fatalf("UnblockVirtualDrives returned error: %v", err)
	}

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}
	blocked := unmarshalStringSlice(t, got.Annotations[consts.AnnotationBlockedDrivesVirtualUuids])
	if len(blocked) != 1 || blocked[0] != "vid-2" {
		t.Errorf("blocked virtual uuids = %#v, want [\"vid-2\"] (both named VIDs removed in one request)", blocked)
	}
	if op.results.Err != "" {
		t.Errorf("results.Err = %q, want empty", op.results.Err)
	}
}

// --- addToBlockedList / removeFromBlockedList (pure helpers) ---
//
// These exercise the batch logic directly, independent of any handler: what matters is that removing
// or adding several entries in one call never mutates a shared backing array across iterations (the
// bug an earlier in-place `append(s[:i], s[i+1:]...)` had), and that every entry not found is reported
// rather than silently dropped.

func TestAddToBlockedList(t *testing.T) {
	known := []string{"a", "b", "c", "d"}

	tests := []struct {
		name         string
		blocked      []string
		requested    []string
		wantUpdated  []string
		wantNotFound []string
	}{
		{
			name:         "adds multiple new entries preserving order",
			blocked:      []string{"a"},
			requested:    []string{"b", "c"},
			wantUpdated:  []string{"a", "b", "c"},
			wantNotFound: []string{},
		},
		{
			name:         "already-blocked entries are not duplicated",
			blocked:      []string{"a", "b"},
			requested:    []string{"a", "b"},
			wantUpdated:  []string{"a", "b"},
			wantNotFound: []string{},
		},
		{
			// addToBlockedList itself adds every requested entry it recognises and separately
			// reports the rest as not found; rejecting the whole request when any is unknown is
			// the caller's job (it checks notFound before persisting), not this helper's.
			name:         "known entries are added even when another entry is unknown",
			blocked:      []string{"a"},
			requested:    []string{"b", "z"},
			wantUpdated:  []string{"a", "b"},
			wantNotFound: []string{"z"},
		},
		{
			name:         "empty blocked list",
			blocked:      nil,
			requested:    []string{"a"},
			wantUpdated:  []string{"a"},
			wantNotFound: []string{},
		},
		{
			name:         "empty request is a no-op",
			blocked:      []string{"a"},
			requested:    nil,
			wantUpdated:  []string{"a"},
			wantNotFound: []string{},
		},
		{
			// Mirrors removeFromBlockedList's existing "duplicate names in the request are reported
			// once" case: a repeated entry in requested must still only be added once.
			name:         "duplicate entries in the request are added only once",
			blocked:      []string{"a"},
			requested:    []string{"b", "b"},
			wantUpdated:  []string{"a", "b"},
			wantNotFound: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotUpdated, gotNotFound := addToBlockedList(tt.blocked, tt.requested, known)
			if !reflect.DeepEqual(gotUpdated, tt.wantUpdated) {
				t.Errorf("updated = %#v, want %#v", gotUpdated, tt.wantUpdated)
			}
			if !reflect.DeepEqual(gotNotFound, tt.wantNotFound) {
				t.Errorf("notFound = %#v, want %#v", gotNotFound, tt.wantNotFound)
			}
		})
	}
}

func TestRemoveFromBlockedList(t *testing.T) {
	tests := []struct {
		name          string
		blocked       []string
		toUnblock     []string
		wantRemaining []string
		wantNotFound  []string
	}{
		{
			name:          "removes multiple non-adjacent entries, keeps the rest in order",
			blocked:       []string{"a", "b", "c", "d"},
			toUnblock:     []string{"a", "c"},
			wantRemaining: []string{"b", "d"},
			wantNotFound:  []string{},
		},
		{
			name:          "removes every entry leaving an empty list",
			blocked:       []string{"a", "b"},
			toUnblock:     []string{"a", "b"},
			wantRemaining: []string{},
			wantNotFound:  []string{},
		},
		{
			name:          "unknown entry is reported and the rest are still removed",
			blocked:       []string{"a", "b"},
			toUnblock:     []string{"a", "z"},
			wantRemaining: []string{"b"},
			wantNotFound:  []string{"z"},
		},
		{
			name:          "duplicate names in the request are reported once",
			blocked:       []string{"a"},
			toUnblock:     []string{"z", "z"},
			wantRemaining: []string{"a"},
			wantNotFound:  []string{"z"},
		},
		{
			name:          "empty blocked list reports every requested entry as not found",
			blocked:       nil,
			toUnblock:     []string{"a"},
			wantRemaining: []string{},
			wantNotFound:  []string{"a"},
		},
		{
			name:          "empty request is a no-op",
			blocked:       []string{"a"},
			toUnblock:     nil,
			wantRemaining: []string{"a"},
			wantNotFound:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRemaining, gotNotFound := removeFromBlockedList(tt.blocked, tt.toUnblock)
			if !reflect.DeepEqual(gotRemaining, tt.wantRemaining) {
				t.Errorf("remaining = %#v, want %#v", gotRemaining, tt.wantRemaining)
			}
			if !reflect.DeepEqual(gotNotFound, tt.wantNotFound) {
				t.Errorf("notFound = %#v, want %#v", gotNotFound, tt.wantNotFound)
			}
		})
	}
}

// --- persistBlockedList failure paths (interceptor-injected) ---
//
// persistBlockedList writes the annotation and the capacity status in two separate calls. These pin
// that the ordering actually protects against what it was built for: a failure in the later status
// call must not undo the earlier, already-committed annotation write, and a failure in the earlier
// annotation call must leave nothing written at all.

// TestBlockDrives_StatusUpdateFails_AnnotationSurvives injects a failure into the status-subresource
// Update only. The annotation write (and the sign-drives-hash deletion riding along with it) must
// survive even though the operation as a whole still fails.
func TestBlockDrives_StatusUpdateFails_AnnotationSurvives(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	allDrives := []domain.DriveEntry{
		{Serial: "SN1", CapacityGiB: 100},
		{Serial: "SN2", CapacityGiB: 200},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationWekaFullDrives: marshalJSON(t, allDrives),
		consts.AnnotationSignDrivesHash: "stale-hash",
	})
	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()

	injectedErr := errors.New("injected status write failure")
	c := interceptor.NewClient(baseClient, interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, cli client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if subResourceName == "status" {
				return injectedErr
			}
			return cli.SubResource(subResourceName).Update(ctx, obj, opts...)
		},
	})
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", SerialIDs: []string{"SN1"}},
	}
	err := op.BlockDrives(ctx)
	if err == nil {
		t.Fatalf("BlockDrives returned nil, want the injected status-write error")
	}
	if !errors.Is(err, injectedErr) {
		t.Errorf("BlockDrives error = %v, want it to wrap the injected error", err)
	}

	// Read via the un-intercepted base client: only the Status().Update call is rigged to fail.
	got := &corev1.Node{}
	if err := baseClient.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	blocked := unmarshalStringSlice(t, got.Annotations[consts.AnnotationBlockedDrives])
	if len(blocked) != 1 || blocked[0] != "SN1" {
		t.Errorf("blocked drives = %#v, want [\"SN1\"] to survive the failed status write", blocked)
	}
	if _, ok := got.Annotations[consts.AnnotationSignDrivesHash]; ok {
		t.Errorf("expected %s to still be deleted despite the failed status write", consts.AnnotationSignDrivesHash)
	}

	// The status write never landed, so capacity must remain exactly as it started: absent.
	if _, ok := got.Status.Capacity[corev1.ResourceName(consts.ResourceDrives)]; ok {
		t.Errorf("Status.Capacity[%s] = %#v, want absent (the status write never committed)", consts.ResourceDrives, got.Status.Capacity)
	}
}

// TestBlockDrives_AnnotationUpdateFails_NothingWritten complements the above: when the earlier
// annotation write itself fails, persistBlockedList must never reach the status phase, so nothing at
// all is written.
func TestBlockDrives_AnnotationUpdateFails_NothingWritten(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	allDrives := []domain.DriveEntry{
		{Serial: "SN1", CapacityGiB: 100},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationWekaFullDrives: marshalJSON(t, allDrives),
	})
	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	before := &corev1.Node{}
	if err := baseClient.Get(ctx, client.ObjectKey{Name: "node1"}, before); err != nil {
		t.Fatalf("failed to get node before: %v", err)
	}

	injectedErr := errors.New("injected annotation write failure")
	c := interceptor.NewClient(baseClient, interceptor.Funcs{
		Update: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			return injectedErr
		},
	})

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", SerialIDs: []string{"SN1"}},
	}
	err := op.BlockDrives(ctx)
	if err == nil {
		t.Fatalf("BlockDrives returned nil, want the injected annotation-write error")
	}
	if !errors.Is(err, injectedErr) {
		t.Errorf("BlockDrives error = %v, want it to wrap the injected error", err)
	}

	after := &corev1.Node{}
	if err := baseClient.Get(ctx, client.ObjectKey{Name: "node1"}, after); err != nil {
		t.Fatalf("failed to get node after: %v", err)
	}
	if before.ResourceVersion != after.ResourceVersion {
		t.Errorf("node was written despite the failed annotation update: ResourceVersion %s -> %s", before.ResourceVersion, after.ResourceVersion)
	}
	if _, ok := after.Annotations[consts.AnnotationBlockedDrives]; ok {
		t.Errorf("blocked-drives annotation was written despite the failed annotation update: %q", after.Annotations[consts.AnnotationBlockedDrives])
	}
}

// --- Mixed-kind payload result accumulation ---
//
// A payload naming more than one identifier kind runs a handler per kind (see GetSteps), each
// contributing to the same o.results. recordResult must join every handler's message and recordErr
// must keep only the first failure, exactly the contract documented in block-drives.md's "Reading the
// result" section.

func TestBlockDrivesOperation_MixedPayload_AccumulatesBothResults(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	allDrives := []domain.DriveEntry{
		{Serial: "SN1", CapacityGiB: 100},
	}
	sharedDrives := []domain.SharedDriveInfo{
		{PhysicalUUID: "uuid1", Serial: "SN2", CapacityGiB: 1000, Type: "TLC"},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationWekaFullDrives: marshalJSON(t, allDrives),
		consts.AnnotationSharedDrives:   marshalJSON(t, sharedDrives),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	// Same sequence GetSteps runs when a payload names both kinds: BlockDrives then
	// BlockSharedDrives, both against the one shared *BlockDrivesOperation.
	op := &BlockDrivesOperation{
		client: c,
		payload: &weka.BlockDrivesPayload{
			Node:          "node1",
			SerialIDs:     []string{"SN1"},
			PhysicalUUIDs: []string{"uuid1"},
		},
	}
	if err := op.BlockDrives(ctx); err != nil {
		t.Fatalf("BlockDrives returned error: %v", err)
	}
	if err := op.BlockSharedDrives(ctx); err != nil {
		t.Fatalf("BlockSharedDrives returned error: %v", err)
	}

	want := "Successfully blocked 1 drives on node node1; Successfully blocked 1 drives on node node1"
	if op.results.Result != want {
		t.Errorf("results.Result = %q, want %q (both handlers' messages joined by \"; \")", op.results.Result, want)
	}
	if op.results.Err != "" {
		t.Errorf("results.Err = %q, want empty", op.results.Err)
	}
}

func TestBlockDrivesOperation_MixedPayload_KeepsFirstError(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	allDrives := []domain.DriveEntry{
		{Serial: "SN1", CapacityGiB: 100},
	}
	sharedDrives := []domain.SharedDriveInfo{
		{PhysicalUUID: "uuid1", Serial: "SN2", CapacityGiB: 1000, Type: "TLC"},
	}
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationWekaFullDrives: marshalJSON(t, allDrives),
		consts.AnnotationSharedDrives:   marshalJSON(t, sharedDrives),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client: c,
		payload: &weka.BlockDrivesPayload{
			Node:          "node1",
			SerialIDs:     []string{"UNKNOWN-SERIAL"},
			PhysicalUUIDs: []string{"unknown-uuid"},
		},
	}
	if err := op.BlockDrives(ctx); err != nil {
		t.Fatalf("BlockDrives returned error: %v, want nil (not-found is reported via results.Err)", err)
	}
	if err := op.BlockSharedDrives(ctx); err != nil {
		t.Fatalf("BlockSharedDrives returned error: %v, want nil (not-found is reported via results.Err)", err)
	}

	if !strings.Contains(op.results.Err, "UNKNOWN-SERIAL") {
		t.Errorf("results.Err = %q, want it to mention the first handler's UNKNOWN-SERIAL", op.results.Err)
	}
	if strings.Contains(op.results.Err, "unknown-uuid") {
		t.Errorf("results.Err = %q, want the second handler's error discarded rather than appended", op.results.Err)
	}
}

// --- Malformed annotation JSON ---

// TestBlockDrives_MalformedBlockedListAnnotation_ReturnsError covers the blocked-list side of
// loadNodeAndBlockedList: a corrupt weka.io/blocked-drives value must be a hard error, not treated as
// an empty list.
func TestBlockDrives_MalformedBlockedListAnnotation_ReturnsError(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationWekaFullDrives: marshalJSON(t, []domain.DriveEntry{{Serial: "SN1", CapacityGiB: 100}}),
		consts.AnnotationBlockedDrives:  "not-json",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", SerialIDs: []string{"SN1"}},
	}
	err := op.BlockDrives(ctx)
	if err == nil {
		t.Fatalf("BlockDrives returned nil, want an error for malformed %s", consts.AnnotationBlockedDrives)
	}
	if !strings.Contains(err.Error(), "failed to unmarshal "+consts.AnnotationBlockedDrives+" annotation") {
		t.Errorf("error = %q, want it to name the malformed annotation", err.Error())
	}
}

// TestBlockDrives_MalformedKnownDrivesAnnotation_ReturnsError covers the known-drives side: a corrupt
// weka.io/weka-full-drives value must fail the same way.
func TestBlockDrives_MalformedKnownDrivesAnnotation_ReturnsError(t *testing.T) {
	scheme := newBlockDrivesTestScheme(t)
	node := newBlockDrivesTestNode("node1", map[string]string{
		consts.AnnotationWekaFullDrives: "not-json",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := &BlockDrivesOperation{
		client:  c,
		payload: &weka.BlockDrivesPayload{Node: "node1", SerialIDs: []string{"SN1"}},
	}
	err := op.BlockDrives(ctx)
	if err == nil {
		t.Fatalf("BlockDrives returned nil, want an error for malformed %s", consts.AnnotationWekaFullDrives)
	}
	if !strings.Contains(err.Error(), "failed to parse weka-full-drives") {
		t.Errorf("error = %q, want it to name the malformed weka-full-drives data", err.Error())
	}
}
