package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/weka/go-steps-engine/lifecycle"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

func TestGetAlreadySignedDrives_NewFormat(t *testing.T) {
	entries := []domain.DriveEntry{
		{Serial: "SERIAL1", CapacityGiB: 500},
		{Serial: "SERIAL2", CapacityGiB: 1000},
	}
	data, _ := json.Marshal(entries)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				consts.AnnotationWekaFullDrives: string(data),
			},
		},
	}

	drives := getAlreadySignedDrives(node)
	if len(drives) != 2 {
		t.Fatalf("expected 2 drives, got %d", len(drives))
	}
	if drives[0] != "SERIAL1" || drives[1] != "SERIAL2" {
		t.Errorf("unexpected drives: %v", drives)
	}
}

func TestGetAlreadySignedDrives_OldFormat(t *testing.T) {
	serials := []string{"OLD1", "OLD2", "OLD3"}
	data, _ := json.Marshal(serials)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				consts.AnnotationWekaDrives: string(data),
			},
		},
	}

	drives := getAlreadySignedDrives(node)
	if len(drives) != 3 {
		t.Fatalf("expected 3 drives, got %d", len(drives))
	}
	for i, serial := range serials {
		if drives[i] != serial {
			t.Errorf("drive %d: expected %q, got %q", i, serial, drives[i])
		}
	}
}

func TestGetAlreadySignedDrives_SharedDrives(t *testing.T) {
	sharedDrives := []domain.SharedDriveInfo{
		{PhysicalUUID: "uuid1", Serial: "SHARED1", CapacityGiB: 100, Type: "TLC"},
		{PhysicalUUID: "uuid2", Serial: "SHARED2", CapacityGiB: 200, Type: "QLC"},
	}
	data, _ := json.Marshal(sharedDrives)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				consts.AnnotationSharedDrives: string(data),
			},
		},
	}

	drives := getAlreadySignedDrives(node)
	if len(drives) != 2 {
		t.Fatalf("expected 2 drives, got %d", len(drives))
	}
	if drives[0] != "SHARED1" || drives[1] != "SHARED2" {
		t.Errorf("unexpected drives: %v", drives)
	}
}

func TestGetAlreadySignedDrives_BothAnnotations(t *testing.T) {
	regularEntries := []domain.DriveEntry{
		{Serial: "REG1", CapacityGiB: 500},
	}
	regularData, _ := json.Marshal(regularEntries)

	sharedDrives := []domain.SharedDriveInfo{
		{PhysicalUUID: "uuid1", Serial: "SHARED1", CapacityGiB: 100, Type: "TLC"},
	}
	sharedData, _ := json.Marshal(sharedDrives)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				consts.AnnotationWekaFullDrives: string(regularData),
				consts.AnnotationSharedDrives:   string(sharedData),
			},
		},
	}

	drives := getAlreadySignedDrives(node)
	if len(drives) != 2 {
		t.Fatalf("expected 2 drives, got %d", len(drives))
	}
}

func TestGetAlreadySignedDrives_NoAnnotations(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{},
	}

	drives := getAlreadySignedDrives(node)
	if len(drives) != 0 {
		t.Fatalf("expected 0 drives, got %d", len(drives))
	}
}

// --- ApplyDriveTypeOverrides (fake-client backed) ---

func newSignDrivesTestScheme(t *testing.T) *runtime.Scheme {
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

func newOverrideTestNode(name string, annotations map[string]string) *corev1.Node {
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

func marshalSharedDrives(t *testing.T, drives []domain.SharedDriveInfo) string {
	t.Helper()
	data, err := json.Marshal(drives)
	if err != nil {
		t.Fatalf("failed to marshal shared drives: %v", err)
	}
	return string(data)
}

func unmarshalSharedDrives(t *testing.T, raw string) []domain.SharedDriveInfo {
	t.Helper()
	var drives []domain.SharedDriveInfo
	if err := json.Unmarshal([]byte(raw), &drives); err != nil {
		t.Fatalf("failed to unmarshal shared drives: %v", err)
	}
	return drives
}

func assertResourceQuantity(t *testing.T, list corev1.ResourceList, name string, wantGiB int64) {
	t.Helper()
	q, ok := list[corev1.ResourceName(name)]
	if !ok {
		t.Errorf("resource %s not present in %#v", name, list)
		return
	}
	if q.Value() != wantGiB {
		t.Errorf("resource %s = %d, want %d", name, q.Value(), wantGiB)
	}
}

// testOwnerRef is a placeholder ownerRef for ApplyDriveTypeOverrides tests. record.FakeRecorder.Event
// does not dereference the object, so any client.Object satisfies it.
func testOwnerRef() client.Object {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: "default"}}
}

// newOverrideTestOp builds a SignDrivesOperation wired to the fake client, with the nil
// callbacks and uncached-reader fallback the ApplyDriveTypeOverrides tests rely on.
func newOverrideTestOp(c client.Client, payload *weka.SignDrivesPayload) *SignDrivesOperation {
	return &SignDrivesOperation{
		client:      c,
		kubeService: kubernetes.NewKubeService(c),
		payload:     payload,
		recorder:    record.NewFakeRecorder(10),
		ownerRef:    testOwnerRef(),
	}
}

// assertNodeChangedWait asserts that ApplyDriveTypeOverrides returned the WaitError it uses to
// defer EnsureContainers to the next reconcile after actually writing to a node (see the
// anyNodeChanged comment in sign_drives.go). This is expected, not a failure: the caller's
// go-steps-engine step machinery treats a *lifecycle.WaitError as "requeue", not "failed".
func assertNodeChangedWait(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a WaitError (a node was changed) but got nil")
	}
	var waitErr *lifecycle.WaitError
	if !errors.As(err, &waitErr) {
		t.Fatalf("expected a *lifecycle.WaitError, got %T: %v", err, err)
	}
}

// assertNodeUnchangedNoWait asserts that ApplyDriveTypeOverrides returned no error at all,
// because no node needed writing this pass.
func assertNodeUnchangedNoWait(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("expected nil error (no node changed), got %v", err)
	}
}

// TestApplyDriveTypeOverrides_GuardClauses_NoOp covers the two-clause early return
// (Shared=false, or DriveTypeOverrides=nil): either one must leave the node completely untouched.
func TestApplyDriveTypeOverrides_GuardClauses_NoOp(t *testing.T) {
	tests := []struct {
		name      string
		shared    bool
		overrides *weka.DriveTypeOverrides
	}{
		{
			name:   "Shared=false",
			shared: false,
			overrides: &weka.DriveTypeOverrides{
				Rules: []weka.DriveTypeOverrideRule{{Model: "Samsung PM1733", Type: "QLC"}},
			},
		},
		{name: "DriveTypeOverrides=nil", shared: true, overrides: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newSignDrivesTestScheme(t)
			node := newOverrideTestNode("node1", map[string]string{
				consts.AnnotationSharedDrives: marshalSharedDrives(t, []domain.SharedDriveInfo{
					{Serial: "SN1", Model: "Samsung PM1733", CapacityGiB: 7000, Type: "TLC"},
				}),
			})
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
			ctx := context.Background()

			before := &corev1.Node{}
			if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, before); err != nil {
				t.Fatalf("failed to get node before: %v", err)
			}

			op := newOverrideTestOp(c, &weka.SignDrivesPayload{
				Shared:             tt.shared,
				DriveTypeOverrides: tt.overrides,
			})

			if err := op.ApplyDriveTypeOverrides(ctx); err != nil {
				t.Fatalf("ApplyDriveTypeOverrides returned error: %v", err)
			}

			after := &corev1.Node{}
			if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, after); err != nil {
				t.Fatalf("failed to get node after: %v", err)
			}
			if before.ResourceVersion != after.ResourceVersion {
				t.Errorf("node was written despite guard clause %q: ResourceVersion %s -> %s", tt.name, before.ResourceVersion, after.ResourceVersion)
			}
		})
	}
}

func TestApplyDriveTypeOverrides_ModelRuleMatch(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)
	drives := []domain.SharedDriveInfo{
		{Serial: "SN1", PhysicalUUID: "uuid1", Model: "Samsung PM1733", CapacityGiB: 7000, Type: "TLC"},
		{Serial: "SN2", PhysicalUUID: "uuid2", Model: "Other Model", CapacityGiB: 4000, Type: "TLC"},
	}
	node := newOverrideTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives:   marshalSharedDrives(t, drives),
		consts.AnnotationSignDrivesHash: "stale-hash",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := newOverrideTestOp(c, &weka.SignDrivesPayload{
		Shared: true,
		DriveTypeOverrides: &weka.DriveTypeOverrides{
			Rules: []weka.DriveTypeOverrideRule{{Model: "Samsung PM1733", Type: "QLC"}},
		},
	})

	// This node is actually changed (its shared-drives Type and capacity resources are
	// updated), so ApplyDriveTypeOverrides defers EnsureContainers via a WaitError rather
	// than returning nil (see Finding C / the anyNodeChanged comment in sign_drives.go).
	assertNodeChangedWait(t, op.ApplyDriveTypeOverrides(ctx))

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	rulesJSON, ok := got.Annotations[consts.AnnotationDriveTypeOverrides]
	if !ok || rulesJSON == "" {
		t.Fatalf("expected %s annotation to be written", consts.AnnotationDriveTypeOverrides)
	}
	var persistedRules []weka.DriveTypeOverrideRule
	if err := json.Unmarshal([]byte(rulesJSON), &persistedRules); err != nil {
		t.Fatalf("failed to unmarshal persisted rules: %v", err)
	}
	if len(persistedRules) != 1 || persistedRules[0].Model != "Samsung PM1733" || persistedRules[0].Type != "QLC" {
		t.Errorf("unexpected persisted rules: %#v", persistedRules)
	}

	updatedDrives := unmarshalSharedDrives(t, got.Annotations[consts.AnnotationSharedDrives])
	bySerial := map[string]domain.SharedDriveInfo{}
	for _, d := range updatedDrives {
		bySerial[d.Serial] = d
	}
	if bySerial["SN1"].Type != "QLC" {
		t.Errorf("SN1 type = %q, want QLC (matched by model rule)", bySerial["SN1"].Type)
	}
	if bySerial["SN2"].Type != "TLC" {
		t.Errorf("SN2 type = %q, want TLC (not matched by rule)", bySerial["SN2"].Type)
	}

	// SN1 (7000 GiB) moved to QLC, SN2 (4000 GiB) remains TLC.
	assertResourceQuantity(t, got.Status.Capacity, consts.ResourceSharedDrivesCapacity, 4000)
	assertResourceQuantity(t, got.Status.Allocatable, consts.ResourceSharedDrivesCapacity, 4000)
	assertResourceQuantity(t, got.Status.Capacity, consts.ResourcesSharedDrivesCapacityQLC, 7000)
	assertResourceQuantity(t, got.Status.Allocatable, consts.ResourcesSharedDrivesCapacityQLC, 7000)

	if _, ok := got.Annotations[consts.AnnotationSignDrivesHash]; ok {
		t.Errorf("expected %s annotation to be deleted so the node is re-signed", consts.AnnotationSignDrivesHash)
	}
}

// TestApplyDriveTypeOverrides_CapacityOnlyRule_ClearsHash covers the behaviour change: the hash
// is now cleared for ANY rule-set change, including a capacity-only rule with no Model field.
// Previously it was cleared only for model-based rules paired with a missing Model, leaving
// drives overridden until reboot when rules were narrowed or cleared with force=false.
func TestApplyDriveTypeOverrides_CapacityOnlyRule_ClearsHash(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)
	drives := []domain.SharedDriveInfo{
		{Serial: "SN1", CapacityGiB: 4000, Type: "TLC"},
	}
	node := newOverrideTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives:   marshalSharedDrives(t, drives),
		consts.AnnotationSignDrivesHash: "stale-hash",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := newOverrideTestOp(c, &weka.SignDrivesPayload{
		Shared: true,
		DriveTypeOverrides: &weka.DriveTypeOverrides{
			Rules: []weka.DriveTypeOverrideRule{{CapacityGiB: 4000, Type: "QLC"}},
		},
	})

	// This node's drive Type actually changes, so ApplyDriveTypeOverrides defers
	// EnsureContainers via a WaitError instead of returning nil (Finding C).
	assertNodeChangedWait(t, op.ApplyDriveTypeOverrides(ctx))

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	if _, ok := got.Annotations[consts.AnnotationSignDrivesHash]; ok {
		t.Errorf("expected %s to be cleared for a capacity-only rule change too", consts.AnnotationSignDrivesHash)
	}

	updatedDrives := unmarshalSharedDrives(t, got.Annotations[consts.AnnotationSharedDrives])
	if len(updatedDrives) != 1 || updatedDrives[0].Type != "QLC" {
		t.Errorf("unexpected updated drives: %#v", updatedDrives)
	}

	assertResourceQuantity(t, got.Status.Capacity, consts.ResourceSharedDrivesCapacity, 0)
	assertResourceQuantity(t, got.Status.Capacity, consts.ResourcesSharedDrivesCapacityQLC, 4000)
}

func TestApplyDriveTypeOverrides_Idempotent(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)
	drives := []domain.SharedDriveInfo{
		{Serial: "SN1", Model: "Samsung PM1733", CapacityGiB: 7000, Type: "TLC"},
	}
	node := newOverrideTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives: marshalSharedDrives(t, drives),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	rules := []weka.DriveTypeOverrideRule{{Model: "Samsung PM1733", Type: "QLC"}}
	newOp := func() *SignDrivesOperation {
		return newOverrideTestOp(c, &weka.SignDrivesPayload{
			Shared:             true,
			DriveTypeOverrides: &weka.DriveTypeOverrides{Rules: rules},
		})
	}

	op1 := newOp()
	// The first call actually changes node1 (TLC -> QLC), so it returns a WaitError rather
	// than nil (Finding C).
	assertNodeChangedWait(t, op1.ApplyDriveTypeOverrides(ctx))
	afterFirst := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, afterFirst); err != nil {
		t.Fatalf("failed to get node after first call: %v", err)
	}

	op2 := newOp()
	// The second call is a true no-op: the node already carries the desired rules, so the
	// equality check inside the annotation retry closure returns before any write, `written`
	// stays false, and ApplyDriveTypeOverrides returns nil rather than a WaitError.
	assertNodeUnchangedNoWait(t, op2.ApplyDriveTypeOverrides(ctx))
	afterSecond := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, afterSecond); err != nil {
		t.Fatalf("failed to get node after second call: %v", err)
	}

	if afterFirst.ResourceVersion != afterSecond.ResourceVersion {
		t.Errorf("second identical call wrote to the node: ResourceVersion %s -> %s", afterFirst.ResourceVersion, afterSecond.ResourceVersion)
	}
	if afterFirst.Annotations[consts.AnnotationSharedDrives] != afterSecond.Annotations[consts.AnnotationSharedDrives] {
		t.Errorf("shared-drives annotation changed on an idempotent second call:\nfirst:  %s\nsecond: %s",
			afterFirst.Annotations[consts.AnnotationSharedDrives], afterSecond.Annotations[consts.AnnotationSharedDrives])
	}
}

// TestApplyDriveTypeOverrides_ConcurrentAnnotationWrite_NotClobbered exercises the lost-update
// race the retry closure guards against: another writer (updateProxyModeAnnotations) updates the
// annotation between our Get and Update, and the retry must re-derive from the fresh re-Get, not
// the stale read, so the concurrent change (a new drive, SN2) survives alongside our override.
func TestApplyDriveTypeOverrides_ConcurrentAnnotationWrite_NotClobbered(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)
	initialDrives := []domain.SharedDriveInfo{
		{Serial: "SN1", Model: "M1", CapacityGiB: 100, Type: "TLC"},
	}
	node := newOverrideTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives: marshalSharedDrives(t, initialDrives),
	})
	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()

	var updateAttempts int
	c := interceptor.NewClient(baseClient, interceptor.Funcs{
		Update: func(ctx context.Context, base client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			// Only race the very first Update attempt against the node's body (not the
			// unrelated Status().Update() call the second retry loop makes afterwards, and
			// not any retry beyond the first).
			node, ok := obj.(*corev1.Node)
			if ok && updateAttempts == 0 {
				updateAttempts++

				// Simulate a concurrent writer landing in between our Get and our Update: it
				// reads-modifies-writes the node directly against the underlying store, using
				// its own Get+Update pair, so it advances the node's ResourceVersion out from
				// under our pending Update (obj), which is still holding the ResourceVersion
				// from our earlier Get.
				concurrent := &corev1.Node{}
				if err := base.Get(ctx, client.ObjectKey{Name: "node1"}, concurrent); err != nil {
					return err
				}
				concurrentDrives := unmarshalSharedDrives(t, concurrent.Annotations[consts.AnnotationSharedDrives])
				concurrentDrives = append(concurrentDrives, domain.SharedDriveInfo{Serial: "SN2", Model: "M2", CapacityGiB: 200, Type: "TLC"})
				concurrent.Annotations[consts.AnnotationSharedDrives] = marshalSharedDrives(t, concurrentDrives)
				if err := base.Update(ctx, concurrent); err != nil {
					return err
				}

				// obj still carries the stale ResourceVersion from before the concurrent write
				// above, so this call must return a conflict - exactly the case RetryOnConflict
				// exists to retry.
				return base.Update(ctx, node, opts...)
			}
			return base.Update(ctx, obj, opts...)
		},
	})
	ctx := context.Background()

	op := newOverrideTestOp(c, &weka.SignDrivesPayload{
		Shared: true,
		DriveTypeOverrides: &weka.DriveTypeOverrides{
			Rules: []weka.DriveTypeOverrideRule{{Model: "M1", Type: "QLC"}},
		},
	})

	assertNodeChangedWait(t, op.ApplyDriveTypeOverrides(ctx))

	if updateAttempts == 0 {
		t.Fatalf("test setup bug: the injected conflict never fired, so this test did not exercise the retry path")
	}

	got := &corev1.Node{}
	if err := baseClient.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	finalDrives := unmarshalSharedDrives(t, got.Annotations[consts.AnnotationSharedDrives])
	bySerial := map[string]domain.SharedDriveInfo{}
	for _, d := range finalDrives {
		bySerial[d.Serial] = d
	}

	sn2, ok := bySerial["SN2"]
	if !ok {
		t.Fatalf("concurrently-added drive SN2 was clobbered by the retry; final drives: %#v", finalDrives)
	}
	if sn2.Type != "TLC" {
		t.Errorf("SN2 type = %q, want TLC (untouched by the override rule, unaffected by the race)", sn2.Type)
	}
	if bySerial["SN1"].Type != "QLC" {
		t.Errorf("SN1 type = %q, want QLC (override applied on the retry against the re-Get'd annotation)", bySerial["SN1"].Type)
	}

	events := drainEvents(t, op)
	var applied []string
	for _, msg := range events {
		if strings.Contains(msg, "DriveTypeOverridesApplied") {
			applied = append(applied, msg)
		}
	}
	if len(applied) != 1 {
		t.Fatalf("expected exactly 1 DriveTypeOverridesApplied event, got %d; all events = %v", len(applied), events)
	}
	for _, want := range []string{"1 node(s) updated", "1 drive type(s) changed"} {
		if !strings.Contains(applied[0], want) {
			t.Errorf("applied event = %q, want it to contain %q (only SN1 changed type)", applied[0], want)
		}
	}
}

// TestApplyDriveTypeOverrides_ClearRules_DoesNotRevertTypes exercises `rules: []` against a node
// that already carries a persisted override. The override annotation and hash are both cleared,
// but the drive's Type recorded in weka.io/weka-shared-drives is NOT reverted here — recovering
// the IU-derived base type requires the sign-drives pod run that the cleared hash now forces.
func TestApplyDriveTypeOverrides_ClearRules_DoesNotRevertTypes(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)
	drives := []domain.SharedDriveInfo{
		{Serial: "SN1", Model: "Samsung PM1733", CapacityGiB: 7000, Type: "QLC"},
	}
	existingRules := []weka.DriveTypeOverrideRule{{Model: "Samsung PM1733", Type: "QLC"}}
	existingRulesJSON, err := json.Marshal(existingRules)
	if err != nil {
		t.Fatalf("failed to marshal existing rules: %v", err)
	}
	node := newOverrideTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives:       marshalSharedDrives(t, drives),
		consts.AnnotationDriveTypeOverrides: string(existingRulesJSON),
		consts.AnnotationSignDrivesHash:     "stale-hash",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := newOverrideTestOp(c, &weka.SignDrivesPayload{
		Shared:             true,
		DriveTypeOverrides: &weka.DriveTypeOverrides{Rules: []weka.DriveTypeOverrideRule{}},
	})

	// Clearing the rules changes the node (the override annotation and hash are removed), so
	// ApplyDriveTypeOverrides defers EnsureContainers via a WaitError instead of nil (Finding C).
	assertNodeChangedWait(t, op.ApplyDriveTypeOverrides(ctx))

	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	if _, ok := got.Annotations[consts.AnnotationDriveTypeOverrides]; ok {
		t.Errorf("expected %s annotation to be removed when rules is empty", consts.AnnotationDriveTypeOverrides)
	}
	if _, ok := got.Annotations[consts.AnnotationSignDrivesHash]; ok {
		t.Errorf("expected %s annotation to be cleared", consts.AnnotationSignDrivesHash)
	}

	updatedDrives := unmarshalSharedDrives(t, got.Annotations[consts.AnnotationSharedDrives])
	if len(updatedDrives) != 1 || updatedDrives[0].Type != "QLC" {
		t.Errorf("drive type should remain QLC until the forced re-sign runs, got: %#v", updatedDrives)
	}
}

// TestApplyDriveTypeOverrides_UnmatchedRule_EmitsEventOnIdempotentPass verifies a dead rule is
// still reported on the idempotent second pass, once the node already carries the (unmatched)
// rule and no write happens. It also pins the Event denominator to evaluatedNodes, not len(nodes):
// node2 has no shared-drives annotation, so it must not count toward "of N evaluated nodes".
func TestApplyDriveTypeOverrides_UnmatchedRule_EmitsEventOnIdempotentPass(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)
	node1 := newOverrideTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives: marshalSharedDrives(t, []domain.SharedDriveInfo{
			{Serial: "SN1", Model: "Other Model", CapacityGiB: 4000, Type: "TLC"},
		}),
	})
	node2 := newOverrideTestNode("node2", map[string]string{})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node1, node2).Build()
	ctx := context.Background()

	rules := []weka.DriveTypeOverrideRule{{Model: "Samsung PM1733", Type: "QLC"}}
	newOp := func(recorder *record.FakeRecorder) *SignDrivesOperation {
		return &SignDrivesOperation{
			client:      c,
			kubeService: kubernetes.NewKubeService(c),
			payload: &weka.SignDrivesPayload{
				Shared:             true,
				DriveTypeOverrides: &weka.DriveTypeOverrides{Rules: rules},
			},
			recorder: recorder,
			ownerRef: testOwnerRef(),
		}
	}

	// Pass 1: node1's rules annotation goes from absent to present (a real change), so this
	// returns a WaitError rather than nil (Finding C), and the dead rule fires a
	// DriveTypeOverrideNoMatch event.
	op1 := newOp(record.NewFakeRecorder(10))
	assertNodeChangedWait(t, op1.ApplyDriveTypeOverrides(ctx))

	select {
	case msg := <-op1.recorder.(*record.FakeRecorder).Events:
		if !strings.Contains(msg, "DriveTypeOverrideNoMatch") {
			t.Errorf("pass 1 event = %q, want it to mention DriveTypeOverrideNoMatch", msg)
		}
	default:
		t.Errorf("expected a DriveTypeOverrideNoMatch event on pass 1, got none")
	}

	// Pass 2: a fresh op and a fresh recorder against the now-persisted rules. The rules are
	// already equal, so needsWrite is false and nothing is written this pass — the call returns
	// nil, not a WaitError. But the dead rule must still be reported: the precheck that decides
	// evaluatedNodes and the matched/unmatched set runs regardless of whether a write happens.
	op2 := newOp(record.NewFakeRecorder(10))
	assertNodeUnchangedNoWait(t, op2.ApplyDriveTypeOverrides(ctx))

	select {
	case msg := <-op2.recorder.(*record.FakeRecorder).Events:
		if !strings.Contains(msg, "DriveTypeOverrideNoMatch") {
			t.Errorf("pass 2 event = %q, want it to mention DriveTypeOverrideNoMatch", msg)
		}
		// node2 has no shared-drives annotation at all, so it must never be counted as
		// evaluated: the denominator must be "1 of 1", not "1 of 2".
		if !strings.Contains(msg, "1 of 1 evaluated nodes") {
			t.Errorf("pass 2 event = %q, want denominator \"1 of 1 evaluated nodes\" (node2 must not count as evaluated)", msg)
		}
		if strings.Contains(msg, "1 of 2") {
			t.Errorf("pass 2 event = %q, denominator counted node2 even though it has no shared-drives annotation", msg)
		}
	default:
		t.Errorf("expected a DriveTypeOverrideNoMatch event to still be recorded on the idempotent second pass, got none")
	}
}

// TestApplyDriveTypeOverrides_TwoNodesTwoDrives_BothUpdatedNoUnmatchedEvent covers a single pass
// touching multiple nodes: both nodes have a drive matching the rule, so both are updated and no
// DriveTypeOverrideNoMatch event should fire for it.
func TestApplyDriveTypeOverrides_TwoNodesTwoDrives_BothUpdatedNoUnmatchedEvent(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)

	node1 := newOverrideTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives: marshalSharedDrives(t, []domain.SharedDriveInfo{
			{Serial: "SN1", Model: "Samsung PM1733", CapacityGiB: 7000, Type: "TLC"},
		}),
	})
	node2 := newOverrideTestNode("node2", map[string]string{
		consts.AnnotationSharedDrives: marshalSharedDrives(t, []domain.SharedDriveInfo{
			{Serial: "SN2", Model: "Samsung PM1733", CapacityGiB: 7000, Type: "TLC"},
		}),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node1, node2).Build()
	ctx := context.Background()

	recorder := record.NewFakeRecorder(10)
	op := &SignDrivesOperation{
		client:      c,
		kubeService: kubernetes.NewKubeService(c),
		payload: &weka.SignDrivesPayload{
			Shared: true,
			DriveTypeOverrides: &weka.DriveTypeOverrides{
				Rules: []weka.DriveTypeOverrideRule{{Model: "Samsung PM1733", Type: "QLC"}},
			},
		},
		recorder: recorder,
		ownerRef: testOwnerRef(),
	}

	// Both nodes are actually changed, so this returns a WaitError rather than nil (Finding C).
	assertNodeChangedWait(t, op.ApplyDriveTypeOverrides(ctx))

	for _, name := range []string{"node1", "node2"} {
		got := &corev1.Node{}
		if err := c.Get(ctx, client.ObjectKey{Name: name}, got); err != nil {
			t.Fatalf("failed to get %s: %v", name, err)
		}
		updatedDrives := unmarshalSharedDrives(t, got.Annotations[consts.AnnotationSharedDrives])
		if len(updatedDrives) != 1 || updatedDrives[0].Type != "QLC" {
			t.Errorf("%s: unexpected updated drives: %#v", name, updatedDrives)
		}

		// The extended resources must follow the annotation on *every* changed node, not just the
		// first: the drive flipped to QLC, so its capacity must move out of the TLC resource and
		// into the QLC one. Both are asserted because a rewrite that sets only one leaves the
		// other stale, which the allocator would then read as phantom capacity.
		assertResourceQuantity(t, got.Status.Capacity, consts.ResourceSharedDrivesCapacity, 0)
		assertResourceQuantity(t, got.Status.Allocatable, consts.ResourceSharedDrivesCapacity, 0)
		assertResourceQuantity(t, got.Status.Capacity, consts.ResourcesSharedDrivesCapacityQLC, 7000)
		assertResourceQuantity(t, got.Status.Allocatable, consts.ResourcesSharedDrivesCapacityQLC, 7000)
	}

	assertNoEventWithReason(t, recorder, "DriveTypeOverrideNoMatch")
}

// TestApplyDriveTypeOverrides_NoDriveEvidence_PersistsRulesOnly covers the two ways a node can lack
// usable shared-drives evidence — never signed (no weka.io/weka-shared-drives annotation at all)
// and signed but decoding to an empty list. Neither may have weka-shared-drives or the capacity
// resources touched, because the cluster_signed_drives webhook treats that annotation's presence as
// "signed". The rules annotation IS written, though: updateProxyModeAnnotations reads it back when
// it writes the freshly signed drives, so skipping it would make the node's first sign publish
// IU-derived types and the matching TLC/QLC split, with the override landing only on a later pass.
// No WaitError either — the node still has to be signed, so deferring EnsureContainers by a cycle
// would only slow that down.
func TestApplyDriveTypeOverrides_NoDriveEvidence_PersistsRulesOnly(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
	}{
		{name: "never signed", annotations: map[string]string{}},
		{name: "empty shared-drives list", annotations: map[string]string{
			consts.AnnotationSharedDrives: marshalSharedDrives(t, []domain.SharedDriveInfo{}),
		}},
	}

	rules := []weka.DriveTypeOverrideRule{{Model: "Samsung PM1733", Type: "QLC"}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newSignDrivesTestScheme(t)
			node := newOverrideTestNode("node1", tt.annotations)
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
			ctx := context.Background()

			before := &corev1.Node{}
			if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, before); err != nil {
				t.Fatalf("failed to get node before: %v", err)
			}

			op := newOverrideTestOp(c, &weka.SignDrivesPayload{
				Shared:             true,
				DriveTypeOverrides: &weka.DriveTypeOverrides{Rules: rules},
			})

			assertNodeUnchangedNoWait(t, op.ApplyDriveTypeOverrides(ctx))

			after := &corev1.Node{}
			if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, after); err != nil {
				t.Fatalf("failed to get node after: %v", err)
			}

			// The rules are persisted for the signing pod to pick up.
			gotRules, err := domain.ReadDriveTypeOverrides(after)
			if err != nil {
				t.Fatalf("%s: failed to read persisted rules: %v", tt.name, err)
			}
			if !slices.Equal(gotRules, rules) {
				t.Errorf("%s: persisted rules = %#v, want %#v", tt.name, gotRules, rules)
			}

			// weka-shared-drives must be byte-identical to before — including still absent when it
			// was absent, so the webhook does not start treating this node as signed.
			if before.Annotations[consts.AnnotationSharedDrives] != after.Annotations[consts.AnnotationSharedDrives] {
				t.Errorf("%s: weka-shared-drives changed: %q -> %q", tt.name,
					before.Annotations[consts.AnnotationSharedDrives], after.Annotations[consts.AnnotationSharedDrives])
			}
			if _, had := before.Annotations[consts.AnnotationSharedDrives]; !had {
				if _, has := after.Annotations[consts.AnnotationSharedDrives]; has {
					t.Errorf("%s: weka-shared-drives was created on a never-signed node", tt.name)
				}
			}

			// No capacity was computed, so the extended resources must not have been conjured.
			if _, ok := after.Status.Capacity[corev1.ResourceName(consts.ResourceSharedDrivesCapacity)]; ok {
				t.Errorf("%s: %s capacity was written for a node with no drives", tt.name, consts.ResourceSharedDrivesCapacity)
			}
			if _, ok := after.Status.Capacity[corev1.ResourceName(consts.ResourcesSharedDrivesCapacityQLC)]; ok {
				t.Errorf("%s: %s capacity was written for a node with no drives", tt.name, consts.ResourcesSharedDrivesCapacityQLC)
			}
		})
	}
}

// TestApplyDriveTypeOverrides_ClearOnUnsignedNode_DeletesRules covers the corollary of persisting
// rules on an unsigned node: `rules: []` must actually clear them there too. Leaving them would let
// updateProxyModeAnnotations re-apply the rules an admin just deleted the next time that node is
// signed, and the clear would only converge on a later pass that happens to catch it signed.
func TestApplyDriveTypeOverrides_ClearOnUnsignedNode_DeletesRules(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)
	staleRules, err := json.Marshal([]weka.DriveTypeOverrideRule{{CapacityGiB: 7000, Type: "QLC"}})
	if err != nil {
		t.Fatalf("failed to marshal stale rules: %v", err)
	}
	node := newOverrideTestNode("node1", map[string]string{
		consts.AnnotationDriveTypeOverrides: string(staleRules),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := newOverrideTestOp(c, &weka.SignDrivesPayload{
		Shared:             true,
		DriveTypeOverrides: &weka.DriveTypeOverrides{Rules: []weka.DriveTypeOverrideRule{}},
	})

	assertNodeUnchangedNoWait(t, op.ApplyDriveTypeOverrides(ctx))

	after := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, after); err != nil {
		t.Fatalf("failed to get node after: %v", err)
	}
	if raw, ok := after.Annotations[consts.AnnotationDriveTypeOverrides]; ok {
		t.Errorf("stale rules survived the clear on an unsigned node: %q", raw)
	}
}

// TestApplyDriveTypeOverrides_AllNodesUnsigned_EmitsPersistedEvent covers the first-ever sign: every
// selected node is unsigned, so nothing can be re-typed yet and nodesUpdated stays 0. That left the
// Applied/Cleared block silent and the operation emitted no override Event whatsoever, even though
// the rules were stored and processResults would go on to force the type of every matching drive as
// it was signed. Events are this feature's only reporting channel, so silence here made the most
// common greenfield case unobservable — live testing on a wiped 8-node cluster saw 24 drive types
// flipped with zero Events to show for it.
func TestApplyDriveTypeOverrides_AllNodesUnsigned_EmitsPersistedEvent(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)
	// No AnnotationSharedDrives: the node has never been signed.
	node := newOverrideTestNode("node1", map[string]string{})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := newOverrideTestOp(c, &weka.SignDrivesPayload{
		Shared: true,
		DriveTypeOverrides: &weka.DriveTypeOverrides{
			Rules: []weka.DriveTypeOverrideRule{{CapacityGiB: 14307, Type: "QLC"}},
		},
	})

	// A rules-only pass writes the annotation but re-types no drive, so it does not take the
	// WaitError path that guards EnsureContainers against a stale sign-drives hash.
	assertNodeUnchangedNoWait(t, op.ApplyDriveTypeOverrides(ctx))

	recorder := op.recorder.(*record.FakeRecorder)
	var events []string
	for {
		select {
		case msg := <-recorder.Events:
			events = append(events, msg)
			continue
		default:
		}
		break
	}

	var persisted int
	for _, msg := range events {
		if strings.Contains(msg, "DriveTypeOverridesPersisted") {
			persisted++
			if !strings.Contains(msg, "1 not-yet-signed node(s)") {
				t.Errorf("Persisted event = %q, want it to report the not-yet-signed node count", msg)
			}
		}
		if strings.Contains(msg, "DriveTypeOverridesApplied") {
			t.Errorf("got %q, want DriveTypeOverridesPersisted instead: no drive was re-typed this pass", msg)
		}
	}
	if persisted != 1 {
		t.Fatalf("got %d DriveTypeOverridesPersisted events, want exactly 1; all events = %v", persisted, events)
	}

	// The rules must actually be on the node, or the Event would be claiming something untrue.
	after := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, after); err != nil {
		t.Fatalf("failed to get node after: %v", err)
	}
	rules, err := domain.ReadDriveTypeOverrides(after)
	if err != nil {
		t.Fatalf("failed to read rules: %v", err)
	}
	if len(rules) != 1 {
		t.Errorf("persisted rules = %d, want 1", len(rules))
	}
}

// TestApplyDriveTypeOverrides_ClearOnUnsignedNode_EmitsNoPersistedEvent pins the asymmetry in the
// Persisted Event's gate: clearing rules on a not-yet-signed node is not worth reporting, because
// there is no forced type on an unsigned node for the clear to undo. Only a non-empty rule set gets
// the Event.
func TestApplyDriveTypeOverrides_ClearOnUnsignedNode_EmitsNoPersistedEvent(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)
	staleRules, err := json.Marshal([]weka.DriveTypeOverrideRule{{CapacityGiB: 7000, Type: "QLC"}})
	if err != nil {
		t.Fatalf("failed to marshal stale rules: %v", err)
	}
	node := newOverrideTestNode("node1", map[string]string{
		consts.AnnotationDriveTypeOverrides: string(staleRules),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()

	op := newOverrideTestOp(c, &weka.SignDrivesPayload{
		Shared:             true,
		DriveTypeOverrides: &weka.DriveTypeOverrides{Rules: []weka.DriveTypeOverrideRule{}},
	})

	assertNodeUnchangedNoWait(t, op.ApplyDriveTypeOverrides(context.Background()))

	assertNoEventWithReason(t, op.recorder.(*record.FakeRecorder), "DriveTypeOverridesPersisted")
}

// TestApplyDriveTypeOverrides_NoModelEvidence_SuppressesUnmatchedEvent verifies a model-based rule
// matching nothing on a node whose drives have no Model recorded at all (as opposed to a
// recorded-but-different Model) is not reported as unmatched: the forced re-sign this loop
// triggers (via hash deletion) is what will populate Model evidence, so reporting now would be a
// false positive telling an admin their rule is dead.
func TestApplyDriveTypeOverrides_NoModelEvidence_SuppressesUnmatchedEvent(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)
	drives := []domain.SharedDriveInfo{
		{Serial: "SN1", CapacityGiB: 4000, Type: "TLC"}, // no Model recorded at all
	}
	node := newOverrideTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives: marshalSharedDrives(t, drives),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	recorder := record.NewFakeRecorder(10)
	op := &SignDrivesOperation{
		client:      c,
		kubeService: kubernetes.NewKubeService(c),
		payload: &weka.SignDrivesPayload{
			Shared: true,
			DriveTypeOverrides: &weka.DriveTypeOverrides{
				Rules: []weka.DriveTypeOverrideRule{{Model: "Samsung PM1733", Type: "QLC"}},
			},
		},
		recorder: recorder,
		ownerRef: testOwnerRef(),
	}

	// The rules annotation still goes from absent to present, so this is a real node change.
	assertNodeChangedWait(t, op.ApplyDriveTypeOverrides(ctx))

	assertNoEventWithReason(t, recorder, "DriveTypeOverrideNoMatch")
}

// TestGetJsonResult_OmitsDriveTypeOverrides pins that the sign-drives result carries no
// driveTypeOverrides key. A status summary can only ever describe the last writing pass, so it
// under-reports; the per-pass truth is in the DriveTypeOverridesApplied/Cleared Events. A value
// left in status by an older operator version must not be echoed back either.
func TestGetJsonResult_OmitsDriveTypeOverrides(t *testing.T) {
	owner := &weka.WekaManualOperation{ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: "default"}}
	owner.Status.Result = `{"message":"No new drives signed","driveTypeOverrides":{"nodesUpdated":8,"drivesChanged":24,"unmatchedRules":1}}`

	op := &SignDrivesOperation{
		recorder: record.NewFakeRecorder(10),
		ownerRef: owner,
	}
	gotJSON := op.GetJsonResult()
	if strings.Contains(gotJSON, "driveTypeOverrides") {
		t.Errorf("expected no driveTypeOverrides key even when an older-version status carries one, got: %s", gotJSON)
	}
}

// assertNoEventWithReason asserts no queued event carries the given reason. It filters by reason
// rather than asserting the queue is empty, so the normal-path DriveTypeOverridesApplied event
// does not read as a spurious warning.
func assertNoEventWithReason(t *testing.T, recorder *record.FakeRecorder, reason string) {
	t.Helper()
	for {
		select {
		case msg := <-recorder.Events:
			if strings.Contains(msg, reason) {
				t.Errorf("expected no %s event, got: %q", reason, msg)
			}
		default:
			return
		}
	}
}

// drainEvents collects everything currently queued on a FakeRecorder without blocking.
func drainEvents(t *testing.T, op *SignDrivesOperation) []string {
	t.Helper()
	var got []string
	for {
		select {
		case msg := <-op.recorder.(*record.FakeRecorder).Events:
			got = append(got, msg)
		default:
			return got
		}
	}
}

// TestApplyDriveTypeOverrides_AppliedEventOncePerChange pins the normal-path Event that makes a
// pass's work observable without polling status.result (which a later pass recomputes). It must
// fire on the writing pass and stay silent on the idempotent pass that follows — unlike the
// unmatched-rule Warning, which re-fires every evaluated pass on purpose.
func TestApplyDriveTypeOverrides_AppliedEventOncePerChange(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)
	node := newOverrideTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives: marshalSharedDrives(t, []domain.SharedDriveInfo{
			{Serial: "SN1", Model: "Samsung PM1733", CapacityGiB: 7000, Type: "TLC"},
			{Serial: "SN2", Model: "Samsung PM1733", CapacityGiB: 4000, Type: "TLC"},
		}),
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	payload := func() *weka.SignDrivesPayload {
		return &weka.SignDrivesPayload{
			Shared: true,
			DriveTypeOverrides: &weka.DriveTypeOverrides{Rules: []weka.DriveTypeOverrideRule{
				{Model: "Samsung PM1733", Type: "QLC"},
			}},
		}
	}

	// Writing pass: one node updated, both drives flipped to QLC.
	op1 := newOverrideTestOp(c, payload())
	assertNodeChangedWait(t, op1.ApplyDriveTypeOverrides(ctx))

	events := drainEvents(t, op1)
	var applied []string
	for _, msg := range events {
		if strings.Contains(msg, "DriveTypeOverridesApplied") {
			applied = append(applied, msg)
		}
	}
	if len(applied) != 1 {
		t.Fatalf("writing pass emitted %d DriveTypeOverridesApplied events, want exactly 1; all events = %v", len(applied), events)
	}
	for _, want := range []string{"Normal", "Applied 1 drive type override rule(s)", "1 node(s) updated", "2 drive type(s) changed"} {
		if !strings.Contains(applied[0], want) {
			t.Errorf("applied event = %q, want it to contain %q", applied[0], want)
		}
	}

	// The event reports the same work the extended resources must reflect: both drives are now
	// QLC, so all 11000 GiB moved from the TLC resource to the QLC one.
	got := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node1: %v", err)
	}
	assertResourceQuantity(t, got.Status.Capacity, consts.ResourceSharedDrivesCapacity, 0)
	assertResourceQuantity(t, got.Status.Allocatable, consts.ResourceSharedDrivesCapacity, 0)
	assertResourceQuantity(t, got.Status.Capacity, consts.ResourcesSharedDrivesCapacityQLC, 11000)
	assertResourceQuantity(t, got.Status.Allocatable, consts.ResourcesSharedDrivesCapacityQLC, 11000)

	// Idempotent pass: rules already persisted, nothing written, so no applied event.
	op2 := newOverrideTestOp(c, payload())
	assertNodeUnchangedNoWait(t, op2.ApplyDriveTypeOverrides(ctx))

	for _, msg := range drainEvents(t, op2) {
		if strings.Contains(msg, "DriveTypeOverridesApplied") {
			t.Errorf("idempotent pass emitted %q, want no applied event when nothing was written", msg)
		}
	}
}

// TestApplyDriveTypeOverrides_ClearedEvent covers the rules:[] path, which is a write (the
// annotation is removed) but changes no drive type, so it gets its own reason rather than an
// "Applied 0 rules" message.
func TestApplyDriveTypeOverrides_ClearedEvent(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)
	node := newOverrideTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives: marshalSharedDrives(t, []domain.SharedDriveInfo{
			{Serial: "SN1", Model: "Samsung PM1733", CapacityGiB: 7000, Type: "QLC"},
		}),
		consts.AnnotationDriveTypeOverrides: `[{"model":"Samsung PM1733","type":"QLC"}]`,
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()
	ctx := context.Background()

	op := newOverrideTestOp(c, &weka.SignDrivesPayload{
		Shared:             true,
		DriveTypeOverrides: &weka.DriveTypeOverrides{Rules: []weka.DriveTypeOverrideRule{}},
	})
	assertNodeChangedWait(t, op.ApplyDriveTypeOverrides(ctx))

	events := drainEvents(t, op)
	var cleared int
	for _, msg := range events {
		if strings.Contains(msg, "DriveTypeOverridesApplied") {
			t.Errorf("clear emitted %q, want DriveTypeOverridesCleared instead", msg)
		}
		if strings.Contains(msg, "DriveTypeOverridesCleared") {
			cleared++
			if !strings.Contains(msg, "1 node(s)") {
				t.Errorf("cleared event = %q, want it to report 1 node(s)", msg)
			}
		}
	}
	if cleared != 1 {
		t.Errorf("got %d DriveTypeOverridesCleared events, want 1; all events = %v", cleared, events)
	}
}

// TestEnsureContainers_ExtendedPayloadPerNode_NoCrossNodeLeak pins a cross-node leak fix:
// extendedPayload used to be built once before the node loop and then mutated conditionally
// inside it, so a node with nothing to exclude could inherit the previous node's
// ExcludedSerialIds. node1 has already-signed drives recorded via the legacy weka.io/weka-drives
// annotation; node2 has none and must not pick up node1's exclusions.
//
// This exercises the leak at the ExcludedSerialIds level rather than via a real ssdproxy
// container: GetSsdProxyContainerUuid (the other field extendedPayload carries) resolves the
// operator's own pod namespace, which needs env-var scaffolding beyond what this fake-client test
// sets up. ExcludedSerialIds is populated by the same per-node code path and mutates the same
// struct, so it exercises an equivalent regression.
func TestEnsureContainers_ExtendedPayloadPerNode_NoCrossNodeLeak(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)
	readyConditions := []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}

	serials, err := json.Marshal([]string{"SN1"})
	if err != nil {
		t.Fatalf("failed to marshal serials: %v", err)
	}
	node1 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "node1",
			Annotations: map[string]string{consts.AnnotationWekaDrives: string(serials)},
		},
		Status: corev1.NodeStatus{Conditions: readyConditions},
	}
	node2 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node2"},
		Status:     corev1.NodeStatus{Conditions: readyConditions},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node1, node2).Build()
	ctx := context.Background()

	op := &SignDrivesOperation{
		client:      c,
		kubeService: kubernetes.NewKubeService(c),
		scheme:      scheme,
		payload:     &weka.SignDrivesPayload{},
		image:       "test-image",
		ownerRef:    testOwnerRef(),
		force:       true,
	}

	if err := op.EnsureContainers(ctx); err != nil {
		t.Fatalf("EnsureContainers failed: %v", err)
	}

	getExcludedSerialIds := func(nodeName string) []string {
		t.Helper()
		container := &weka.WekaContainer{}
		name := fmt.Sprintf("weka-sign-and-discover-drives-%s", nodeName)
		if getErr := c.Get(ctx, client.ObjectKey{Name: name, Namespace: "default"}, container); getErr != nil {
			t.Fatalf("failed to get container for %s: %v", nodeName, getErr)
		}
		var payload SignedDrivesExtendedPayload
		if unmarshalErr := json.Unmarshal([]byte(container.Spec.Instructions.Payload), &payload); unmarshalErr != nil {
			t.Fatalf("failed to unmarshal instructions payload for %s: %v", nodeName, unmarshalErr)
		}
		return payload.ExcludedSerialIds
	}

	if got := getExcludedSerialIds("node1"); len(got) != 1 || got[0] != "SN1" {
		t.Errorf("node1 ExcludedSerialIds = %v, want [\"SN1\"]", got)
	}
	if got := getExcludedSerialIds("node2"); len(got) != 0 {
		t.Errorf("node2 ExcludedSerialIds = %v, want empty (must not inherit node1's exclusions)", got)
	}
}

// TestApplyDriveTypeOverrides_WriteFailure_EmitsFailedEvent pins the only user-visible record of a
// node write failure. The returned error never reaches OperationFailed() — the steps engine logs it
// and requeues — so without the Event a node whose write keeps failing leaves the operation sitting
// in Running with no reason a user can find. The step must still report the error, and must not
// convert one bad node into a failure of the whole operation.
func TestApplyDriveTypeOverrides_WriteFailure_EmitsFailedEvent(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)
	node := newOverrideTestNode("node1", map[string]string{
		consts.AnnotationSharedDrives: marshalSharedDrives(t, []domain.SharedDriveInfo{
			{Serial: "SN1", Model: "M1", CapacityGiB: 100, Type: "TLC"},
		}),
	})
	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()

	// A non-conflict error, so RetryOnConflict gives up on the first attempt instead of retrying.
	writeErr := errors.New("simulated node write failure")
	c := interceptor.NewClient(baseClient, interceptor.Funcs{
		Update: func(ctx context.Context, base client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*corev1.Node); ok {
				return writeErr
			}
			return base.Update(ctx, obj, opts...)
		},
	})

	op := newOverrideTestOp(c, &weka.SignDrivesPayload{
		Shared: true,
		DriveTypeOverrides: &weka.DriveTypeOverrides{
			Rules: []weka.DriveTypeOverrideRule{{Model: "M1", Type: "QLC"}},
		},
	})

	err := op.ApplyDriveTypeOverrides(context.Background())
	if err == nil {
		t.Fatalf("expected ApplyDriveTypeOverrides to report the node write failure, got nil")
	}
	// Not a WaitError: a genuine failure must not be masked as "waiting".
	var waitErr *lifecycle.WaitError
	if errors.As(err, &waitErr) {
		t.Errorf("expected a real error, got a WaitError: %v", err)
	}

	var failed string
	for _, msg := range drainEvents(t, op) {
		if strings.Contains(msg, "DriveTypeOverrideFailed") {
			failed = msg
		}
	}
	if failed == "" {
		t.Fatalf("expected a DriveTypeOverrideFailed Warning event; the returned error alone never reaches the owner's status")
	}
	if !strings.Contains(failed, "Warning") {
		t.Errorf("DriveTypeOverrideFailed event = %q, want it recorded as a Warning", failed)
	}
	for _, want := range []string{"1 of 1 node(s)", "node1"} {
		if !strings.Contains(failed, want) {
			t.Errorf("DriveTypeOverrideFailed event = %q, want it to contain %q", failed, want)
		}
	}

	// The failing pass must not also claim success.
	for _, msg := range drainEvents(t, op) {
		if strings.Contains(msg, "DriveTypeOverridesApplied") {
			t.Errorf("unexpected Applied event on a pass where the only node failed: %q", msg)
		}
	}
}

// TestApplyDriveTypeOverrides_StaleCachedRead_NoBogusDenominator pins that the no-match Event's
// numerator and denominator always come from the same read of the node.
//
// The step lists nodes through the cached reader but re-reads each node through the uncached one
// before writing, precisely because those two can disagree: the signing pod's node-annotation write
// and its WekaContainer-status write reach the operator through independent informers. In that
// window the cached list can say "signed" while the fresh read says "not signed yet" (or vice
// versa). If `evaluated` came from one read and the unmatched-rule set from the other, the Event
// would read "matched no drive on 1 of 0 evaluated nodes".
//
// Reproduced here by intercepting List so it reports a shared-drives annotation the stored node does
// not have, which is what the operator sees mid-handoff. Note the production divergence is between
// two different readers; the fake client has only one, so List is the seam.
func TestApplyDriveTypeOverrides_StaleCachedRead_NoBogusDenominator(t *testing.T) {
	scheme := newSignDrivesTestScheme(t)
	// Stored node: NOT signed (no shared-drives annotation) — this is what the fresh Get sees.
	node := newOverrideTestNode("node1", map[string]string{})
	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Node{}).WithObjects(node).Build()

	staleDrives := marshalSharedDrives(t, []domain.SharedDriveInfo{
		{Serial: "SN1", Model: "M1", CapacityGiB: 100, Type: "TLC"},
	})
	c := interceptor.NewClient(baseClient, interceptor.Funcs{
		List: func(ctx context.Context, base client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if err := base.List(ctx, list, opts...); err != nil {
				return err
			}
			nodeList, ok := list.(*corev1.NodeList)
			if !ok {
				return nil
			}
			// The cached view still carries the pre-sign annotation.
			for i := range nodeList.Items {
				if nodeList.Items[i].Annotations == nil {
					nodeList.Items[i].Annotations = map[string]string{}
				}
				nodeList.Items[i].Annotations[consts.AnnotationSharedDrives] = staleDrives
			}
			return nil
		},
	})

	// A rule that matches nothing in the stale view either, so the stale read yields an unmatched
	// rule index that must NOT be reported once the fresh read shows there were no drives at all.
	op := newOverrideTestOp(c, &weka.SignDrivesPayload{
		Shared: true,
		DriveTypeOverrides: &weka.DriveTypeOverrides{
			Rules: []weka.DriveTypeOverrideRule{{Model: "NO-SUCH-MODEL", Type: "QLC"}},
		},
	})

	if err := op.ApplyDriveTypeOverrides(context.Background()); err != nil {
		var waitErr *lifecycle.WaitError
		if !errors.As(err, &waitErr) {
			t.Fatalf("ApplyDriveTypeOverrides returned an unexpected error: %v", err)
		}
	}

	// The node was not evaluated (no drives on the fresh read), so it contributes to neither the
	// numerator nor the denominator: no no-match Event at all. Before this was fixed the stale
	// unmatched index survived, reporting a rule as dead on a node that had no drives to match it
	// against ("matched no drive on 1 of 1 evaluated nodes"), and the node was counted in both
	// evaluatedNodes and rulesOnlyNodes, which the caller documents as disjoint.
	assertNoEventWithReason(t, op.recorder.(*record.FakeRecorder), "DriveTypeOverrideNoMatch")

	// The rules must still have been persisted on the not-yet-signed node.
	got := &corev1.Node{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "node1"}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}
	rules, err := domain.ReadDriveTypeOverrides(got)
	if err != nil {
		t.Fatalf("failed to read rules: %v", err)
	}
	if len(rules) != 1 {
		t.Errorf("persisted rules = %d, want 1 (a not-yet-signed node still stores the rules)", len(rules))
	}
}
