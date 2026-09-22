package operations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/ssdproxy"
)

const (
	migrationTestNamespace = "weka-operator-system"
	migrationTestCluster   = "tenant1"
	migrationTestNode      = "node-a"
)

// newMigrationFakeClient is newFakeClient plus the Node status subresource, which
// RemoveFullDrivesFromNode writes through.
func newMigrationFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("add weka scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&corev1.Node{}).
		WithObjects(objs...).
		Build()
}

// migrationCluster is a WekaCluster already flipped to drive sharing and annotated for migration.
func migrationCluster() *weka.WekaCluster {
	return &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        migrationTestCluster,
			Namespace:   migrationTestNamespace,
			UID:         "cluster-uid",
			Annotations: map[string]string{consts.AnnotationSizingModeMigration: consts.SizingModeMigrationDriveSharing},
		},
		Spec: weka.WekaClusterSpec{
			Dynamic: &weka.WekaClusterTemplate{DriveContainers: 2, ContainerCapacity: 1000},
		},
	}
}

func migrationNode(annotations map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:        migrationTestNode,
		Labels:      map[string]string{corev1.LabelHostname: migrationTestNode},
		Annotations: annotations,
	}}
}

func exclusiveDriveContainer(name string) weka.WekaContainer {
	return weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: migrationTestNamespace},
		Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, NumDrives: 2, NodeAffinity: migrationTestNode},
	}
}

// newMigrationOp builds an operation with every live-cluster seam stubbed to a healthy answer, so
// each test overrides only what it is about.
func newMigrationOp(t *testing.T, c client.Client, owner *weka.WekaManualOperation, containers []weka.WekaContainer) *MigrateToDriveSharingOperation {
	t.Helper()
	return &MigrateToDriveSharingOperation{
		mgr:         &rotateSsdProxyTestManager{reader: newFakeClient(t)},
		client:      c,
		kubeService: &fakeSsdProxyKubeService{containers: containers},
		payload:     &weka.MigrateToDriveSharingPayload{Cluster: weka.ObjectReference{Name: migrationTestCluster, Namespace: migrationTestNamespace}},
		ownerRef:    owner,
		recorder:    events.NewFakeRecorder(20),
		gate: func(context.Context, *weka.WekaCluster, weka.NodeName) ClusterVerdict {
			return ClusterVerdict{Name: migrationTestCluster, Allowed: true}
		},
		wekaStatus: func(context.Context, *weka.WekaCluster) (services.WekaStatusResponse, error) {
			return services.WekaStatusResponse{StripeWidth: 1, Capacity: services.WekaStatusCapacity{
				TotalBytes: 100 * gibBytes, UnprovisionedBytes: 100 * gibBytes,
			}}, nil
		},
		containerDrives: func(context.Context, *weka.WekaCluster, int) ([]weka.Drive, error) {
			return nil, nil
		},
		proxyDrives: func(context.Context, weka.NodeName, *weka.WekaContainer) ([]ssdproxy.PhysicalDrive, error) {
			return nil, nil
		},
		progressCallback: func(context.Context) error { return nil },
	}
}

func asMigrationWaitError(t *testing.T, err error) *lifecycle.WaitError {
	t.Helper()
	var we *lifecycle.WaitError
	if !errors.As(err, &we) {
		t.Fatalf("expected a lifecycle.WaitError (park), got %T: %v", err, err)
	}
	return we
}

func TestPlanParksUntilTheSpecIsFlippedAndAnnotated(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	t.Run("drive-sharing spec without the migration annotation", func(t *testing.T) {
		cluster := migrationCluster()
		cluster.Annotations = nil
		op := newMigrationOp(t, newMigrationFakeClient(t, cluster), &weka.WekaManualOperation{}, nil)

		err := op.Plan(context.Background())

		asMigrationWaitError(t, err)
		if !strings.Contains(op.results.Err, consts.AnnotationSizingModeMigration) {
			t.Errorf("results.Err = %q, want it to name the missing annotation", op.results.Err)
		}
	})

	t.Run("annotation set but the spec still uses explicit counts", func(t *testing.T) {
		cluster := migrationCluster()
		cluster.Spec.Dynamic = &weka.WekaClusterTemplate{DriveContainers: 2, NumDrives: 6}
		op := newMigrationOp(t, newMigrationFakeClient(t, cluster), &weka.WekaManualOperation{}, nil)

		err := op.Plan(context.Background())

		asMigrationWaitError(t, err)
		if !strings.Contains(op.results.Err, "not in drive-sharing mode") {
			t.Errorf("results.Err = %q, want it to name the unflipped spec", op.results.Err)
		}
	})

	t.Run("clusterCapacity is not a migration target", func(t *testing.T) {
		cluster := migrationCluster()
		cluster.Spec.Dynamic = &weka.WekaClusterTemplate{DriveContainers: 2, ClusterCapacity: "100TiB"}
		op := newMigrationOp(t, newMigrationFakeClient(t, cluster), &weka.WekaManualOperation{}, nil)

		err := op.Plan(context.Background())

		asMigrationWaitError(t, err)
		if !strings.Contains(op.results.Err, "clusterCapacity") {
			t.Errorf("results.Err = %q, want it to reject clusterCapacity as a target", op.results.Err)
		}
	})
}

// A nodeSelector that matches no node must park with a message naming the selector as the cause,
// and must not touch the cluster's migration annotation: only the user's own edit removes it.
func TestPlanParksWhenTheNodeSelectorMatchesNoNode(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	cluster := migrationCluster()
	c := newMigrationFakeClient(t, cluster)
	// An already-sharing container on another node must not count toward Total either.
	sharing := exclusiveDriveContainer("drive-1")
	sharing.Spec.NodeAffinity = "other-node"
	sharing.Spec.ContainerCapacity = 1000
	op := newMigrationOp(t, c, &weka.WekaManualOperation{}, []weka.WekaContainer{exclusiveDriveContainer("drive-0"), sharing})
	op.payload.NodeSelector = map[string]string{"disktype": "nvme"}

	err := op.Plan(context.Background())

	asMigrationWaitError(t, err)
	if !strings.Contains(op.results.Err, "nodeSelector") {
		t.Errorf("results.Err = %q, want it to name the nodeSelector", op.results.Err)
	}
	live := &weka.WekaCluster{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: migrationTestNamespace, Name: migrationTestCluster}, live); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	if live.Annotations[consts.AnnotationSizingModeMigration] != consts.SizingModeMigrationDriveSharing {
		t.Errorf("%s = %q, want it left untouched", consts.AnnotationSizingModeMigration, live.Annotations[consts.AnnotationSizingModeMigration])
	}
}

// A sign-drives policy still selecting a node we are about to drain would re-sign the freed drives
// as exclusive in the window between the two signatures, so the campaign parks instead of racing it.
func TestPlanParksWhileASignDrivesPolicyMatchesATargetNode(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	policy := &weka.WekaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "sign-all", Namespace: migrationTestNamespace},
		Spec: weka.WekaPolicySpec{
			Type:    weka.WekaPolicyTypeSignDrives,
			Payload: weka.PolicyPayload{SignDrives: &weka.SignDrivesPayload{Type: "all-not-root"}},
		},
	}
	c := newMigrationFakeClient(t, migrationCluster(), migrationNode(nil), policy)
	op := newMigrationOp(t, c, &weka.WekaManualOperation{}, []weka.WekaContainer{exclusiveDriveContainer("drive-0")})

	err := op.Plan(context.Background())

	asMigrationWaitError(t, err)
	if !strings.Contains(op.results.Err, "sign-all") {
		t.Errorf("results.Err = %q, want it to name the blocking policy", op.results.Err)
	}
	// The campaign set is still planned, so status.result shows what is waiting.
	if len(op.results.Containers) != 1 || op.results.Containers[0].Phase != MigrateDriveSharingPhasePending {
		t.Errorf("containers = %+v, want one Pending entry", op.results.Containers)
	}
}

func TestPlanRefusesASecondCampaign(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	self := &weka.WekaManualOperation{ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: migrationTestNamespace, UID: "self"}}
	other := &weka.WekaManualOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "theirs", Namespace: migrationTestNamespace, UID: "other"},
		Spec:       weka.WekaManualOperationSpec{Action: weka.WekaManualOperationActionMigrateToDriveSharing},
	}
	op := newMigrationOp(t, newMigrationFakeClient(t, migrationCluster()), self, nil)
	op.mgr = &rotateSsdProxyTestManager{reader: newFakeClient(t, other)}

	err := op.Plan(context.Background())

	asMigrationWaitError(t, err)
	if !strings.Contains(op.results.Err, "theirs") {
		t.Errorf("results.Err = %q, want it to name the other campaign", op.results.Err)
	}
}

// The flipped sizing must still cover what the cluster has already provisioned, or the migration
// would strand data it cannot place.
func TestPlanParksWhenTheFlippedSizingIsTooSmall(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	c := newMigrationFakeClient(t, migrationCluster(), migrationNode(nil))
	op := newMigrationOp(t, c, &weka.WekaManualOperation{}, []weka.WekaContainer{exclusiveDriveContainer("drive-0")})
	op.wekaStatus = func(context.Context, *weka.WekaCluster) (services.WekaStatusResponse, error) {
		// 2 x 1000GiB after the flip, against 9000GiB provisioned today.
		return services.WekaStatusResponse{StripeWidth: 1, Capacity: services.WekaStatusCapacity{
			TotalBytes: 10000 * gibBytes, UnprovisionedBytes: 1000 * gibBytes,
		}}, nil
	}

	err := op.Plan(context.Background())

	asMigrationWaitError(t, err)
	if !strings.Contains(op.results.Err, "already") {
		t.Errorf("results.Err = %q, want it to compare new capacity against provisioned", op.results.Err)
	}
	if op.results.CapacityChecked {
		t.Error("CapacityChecked latched despite the check failing")
	}
	if op.results.NewTotalBytes != 2000*gibBytes {
		t.Errorf("NewTotalBytes = %d, want %d", op.results.NewTotalBytes, 2000*gibBytes)
	}
}

func TestAdvanceOnePausedDoesNotStartANewContainer(t *testing.T) {
	op := newMigrationOp(t, newMigrationFakeClient(t), &weka.WekaManualOperation{}, nil)
	op.payload.Paused = true
	op.results.Containers = []MigrateToDriveSharingContainerState{
		{Node: migrationTestNode, Container: "drive-0", Phase: MigrateDriveSharingPhasePending},
	}

	err := op.AdvanceOne(context.Background())

	asMigrationWaitError(t, err)
	if op.results.Containers[0].Phase != MigrateDriveSharingPhasePending {
		t.Errorf("phase = %q, want it left Pending while paused", op.results.Containers[0].Phase)
	}
}

// startPending must record the drive inventory BEFORE anything is mutated: the container object it
// comes from is deleted moments later and is the only place those serials exist.
func TestStartPendingRecordsInventoryAndEntersDraining(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	container := exclusiveDriveContainer("drive-0")
	container.Status.Allocations = &weka.ContainerAllocations{Drives: []string{"S1", "S2"}}
	node := migrationNode(map[string]string{
		consts.AnnotationWekaFullDrives: `[{"serial":"S1","capacity_gib":10},{"serial":"S2","capacity_gib":10},{"serial":"S9","capacity_gib":10}]`,
	})
	c := newMigrationFakeClient(t, migrationCluster(), node, &container)

	op := newMigrationOp(t, c, &weka.WekaManualOperation{}, []weka.WekaContainer{container})
	op.cluster = migrationCluster()
	op.results.Containers = []MigrateToDriveSharingContainerState{
		{Node: migrationTestNode, Container: "drive-0", Phase: MigrateDriveSharingPhasePending},
	}

	err := op.startPending(context.Background(), 0)

	asMigrationWaitError(t, err)
	state := op.results.Containers[0]
	if state.Phase != MigrateDriveSharingPhaseInFlight || state.SubPhase != MigrateDriveSharingSubPhaseDraining {
		t.Fatalf("phase/subPhase = %q/%q, want InFlight/Draining", state.Phase, state.SubPhase)
	}
	if strings.Join(state.Serials, ",") != "S1,S2" {
		t.Errorf("serials = %v, want the container's own two", state.Serials)
	}
	// weka reported no drive sizes, so capacity falls back to the node annotation -- S9 excluded.
	if state.CapacityBytes != 20*gibBytes || state.CapacitySource != MigrateDriveSharingCapacityFromAnnotation {
		t.Errorf("capacity = %d from %q, want %d from the annotation", state.CapacityBytes, state.CapacitySource, 20*gibBytes)
	}
}

// A failure partway through recordDriveInventory must leave state.Serials empty, or the
// len(state.Serials) > 0 guard would treat the half-filled record as already done.
func TestRecordDriveInventoryDoesNotPersistAHalfFilledRecordOnFailure(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	containerID := 7
	container := exclusiveDriveContainer("drive-0")
	container.Status.Allocations = &weka.ContainerAllocations{Drives: []string{"S1", "S2"}}
	container.Status.ClusterContainerID = &containerID
	node := migrationNode(map[string]string{
		consts.AnnotationWekaFullDrives: `[{"serial":"S1","capacity_gib":10},{"serial":"S2","capacity_gib":10}]`,
	})
	c := newMigrationFakeClient(t, node)

	op := newMigrationOp(t, c, &weka.WekaManualOperation{}, nil)
	var calls int
	op.containerDrives = func(context.Context, *weka.WekaCluster, int) ([]weka.Drive, error) {
		calls++
		return nil, errors.New("weka api unavailable")
	}
	op.cluster = migrationCluster()
	state := &MigrateToDriveSharingContainerState{Node: migrationTestNode, Container: "drive-0"}

	if err := op.recordDriveInventory(context.Background(), &container, state); err == nil {
		t.Fatal("expected an error from the failing containerDrives call")
	}
	if len(state.Serials) != 0 {
		t.Errorf("serials = %v, want none persisted after the failure", state.Serials)
	}

	// Retried on the next cycle: containerDrives now succeeds, so every field is set together.
	op.containerDrives = func(context.Context, *weka.WekaCluster, int) ([]weka.Drive, error) {
		return []weka.Drive{{Uuid: "u1", SizeBytes: 5 * gibBytes}, {Uuid: "u2", SizeBytes: 5 * gibBytes}}, nil
	}
	if err := op.recordDriveInventory(context.Background(), &container, state); err != nil {
		t.Fatalf("recordDriveInventory: %v", err)
	}
	if strings.Join(state.Serials, ",") != "S1,S2" {
		t.Errorf("serials = %v, want S1,S2", state.Serials)
	}
	if strings.Join(state.DriveUuids, ",") != "u1,u2" {
		t.Errorf("driveUuids = %v, want u1,u2", state.DriveUuids)
	}
	if state.CapacityBytes != 10*gibBytes || state.CapacitySource != MigrateDriveSharingCapacityFromWeka {
		t.Errorf("capacity = %d from %q, want %d from weka", state.CapacityBytes, state.CapacitySource, 10*gibBytes)
	}
}

// Fail-closed: with no ClusterContainerID and the node annotation missing the serials, capacity
// resolves to 0 and recordDriveInventory must error rather than record a vacuous capacity.
func TestRecordDriveInventoryFailsClosedWhenCapacityIsUnknown(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	container := exclusiveDriveContainer("drive-0")
	container.Status.Allocations = &weka.ContainerAllocations{Drives: []string{"S1"}}
	node := migrationNode(nil)
	c := newMigrationFakeClient(t, node)

	op := newMigrationOp(t, c, &weka.WekaManualOperation{}, nil)
	op.cluster = migrationCluster()
	state := &MigrateToDriveSharingContainerState{Node: migrationTestNode, Container: "drive-0"}

	err := op.recordDriveInventory(context.Background(), &container, state)

	if err == nil {
		t.Fatal("expected an error when capacity cannot be determined from weka or the annotation")
	}
	if !strings.Contains(err.Error(), "capacity") {
		t.Errorf("err = %q, want the capacity error", err)
	}
	if len(state.Serials) != 0 {
		t.Errorf("serials = %v, want none persisted", state.Serials)
	}
}

func TestStartPendingParksWithoutHeadroom(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	container := exclusiveDriveContainer("drive-0")
	container.Status.Allocations = &weka.ContainerAllocations{Drives: []string{"S1"}}
	node := migrationNode(map[string]string{
		consts.AnnotationWekaFullDrives: `[{"serial":"S1","capacity_gib":500}]`,
	})
	c := newMigrationFakeClient(t, migrationCluster(), node, &container)

	op := newMigrationOp(t, c, &weka.WekaManualOperation{}, []weka.WekaContainer{container})
	op.cluster = migrationCluster()
	op.results.Containers = []MigrateToDriveSharingContainerState{
		{Node: migrationTestNode, Container: "drive-0", Phase: MigrateDriveSharingPhasePending},
	}

	err := op.startPending(context.Background(), 0)

	asMigrationWaitError(t, err)
	if op.results.Containers[0].Phase != MigrateDriveSharingPhasePending {
		t.Errorf("phase = %q, want it left Pending when the gate refuses", op.results.Containers[0].Phase)
	}
	if !strings.Contains(op.results.Containers[0].Reason, "headroom") {
		t.Errorf("reason = %q, want it to name the missing headroom", op.results.Containers[0].Reason)
	}
}

// Draining suppresses the exclusive force-resign before deleting: the shared sign overwrites the
// signature moments later, and an exclusive resign in between only widens the reclaim window.
func TestAdvanceDrainingSuppressesResignDeletesAndThenMovesToSigning(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	container := exclusiveDriveContainer("drive-0")
	c := newMigrationFakeClient(t, migrationCluster(), &container)
	op := newMigrationOp(t, c, &weka.WekaManualOperation{}, nil)
	op.cluster = migrationCluster()
	op.results.Containers = []MigrateToDriveSharingContainerState{{
		Node: migrationTestNode, Container: "drive-0",
		Phase: MigrateDriveSharingPhaseInFlight, SubPhase: MigrateDriveSharingSubPhaseDraining,
		Serials: []string{"S1"},
	}}

	err := op.advanceInFlight(context.Background(), 0)
	asMigrationWaitError(t, err)

	live := &weka.WekaContainer{}
	getErr := c.Get(context.Background(), client.ObjectKey{Namespace: migrationTestNamespace, Name: "drive-0"}, live)
	if getErr == nil {
		if !live.Spec.GetOverrides().SkipDrivesForceResign {
			t.Error("skipDrivesForceResign was not patched before deletion")
		}
		if live.DeletionTimestamp == nil {
			t.Error("drive container was not deleted")
		}
	} else if !apierrors.IsNotFound(getErr) {
		t.Fatalf("get drive container: %v", getErr)
	}
	if op.results.Containers[0].SubPhase != MigrateDriveSharingSubPhaseDraining {
		t.Errorf("subPhase = %q, want it still Draining until the container is gone", op.results.Containers[0].SubPhase)
	}

	// Second pass: the container is gone, so the campaign moves on to the shared re-sign.
	if delErr := c.Delete(context.Background(), live); delErr != nil && !apierrors.IsNotFound(delErr) {
		t.Fatalf("delete drive container: %v", delErr)
	}
	err = op.advanceInFlight(context.Background(), 0)
	asMigrationWaitError(t, err)
	if op.results.Containers[0].SubPhase != MigrateDriveSharingSubPhaseSigning {
		t.Errorf("subPhase = %q, want Signing once the container is gone", op.results.Containers[0].SubPhase)
	}
}

func TestAdvanceSigningCreatesTheChildOperationThenVerifiesTheSharedAnnotation(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	node := migrationNode(map[string]string{
		consts.AnnotationWekaFullDrives: `[{"serial":"S1","capacity_gib":10},{"serial":"S9","capacity_gib":10}]`,
	})
	c := newMigrationFakeClient(t, migrationCluster(), node)

	owner := &weka.WekaManualOperation{ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: migrationTestNamespace, UID: "self"}}
	op := newMigrationOp(t, c, owner, nil)
	op.payload.DriveTypeOverrides = &weka.DriveTypeOverrides{Rules: []weka.DriveTypeOverrideRule{{Model: "m", Type: "QLC"}}}
	op.cluster = migrationCluster()
	op.results.Containers = []MigrateToDriveSharingContainerState{{
		Node: migrationTestNode, Container: "drive-0",
		Phase: MigrateDriveSharingPhaseInFlight, SubPhase: MigrateDriveSharingSubPhaseSigning,
		Serials: []string{"S1"},
	}}

	err := op.advanceInFlight(context.Background(), 0)
	asMigrationWaitError(t, err)

	child := &weka.WekaManualOperation{}
	if getErr := c.Get(context.Background(), client.ObjectKey{Namespace: migrationTestNamespace, Name: "migrate-sign-drive-0"}, child); getErr != nil {
		t.Fatalf("expected the child sign-drives operation to be created: %v", getErr)
	}
	payload := child.Spec.Payload.SignDrives
	switch {
	case child.Spec.Action != weka.WekaManualOperationActionSignDrives:
		t.Errorf("child action = %q", child.Spec.Action)
	case payload == nil:
		t.Fatal("child has no signDrives payload")
	case payload.Type != "device-serials":
		t.Errorf("child type = %q, want device-serials", payload.Type)
	case strings.Join(payload.DeviceSerials, ",") != "S1":
		t.Errorf("child serials = %v, want exactly the drained container's", payload.DeviceSerials)
	case !payload.Shared:
		t.Error("child must sign for the proxy (shared)")
	case payload.NodeSelector[corev1.LabelHostname] != migrationTestNode:
		t.Errorf("child nodeSelector = %v, want the drained node", payload.NodeSelector)
	case payload.SignOptions == nil || !payload.SignOptions.AllowEraseWekaPartitions:
		t.Error("allowEraseWekaPartitions must be forced on: the drives carry the exclusive signature")
	case payload.DriveTypeOverrides == nil:
		t.Error("driveTypeOverrides must be passed through to the child")
	}
	if op.results.Containers[0].SignAttempts != 1 {
		t.Errorf("signAttempts = %d, want 1", op.results.Containers[0].SignAttempts)
	}

	// The serials must be gone from the node's exclusive inventory, or the shared sign skips them.
	live := &corev1.Node{}
	if getErr := c.Get(context.Background(), client.ObjectKey{Name: migrationTestNode}, live); getErr != nil {
		t.Fatalf("get node: %v", getErr)
	}
	if strings.Contains(live.Annotations[consts.AnnotationWekaFullDrives], `"S1"`) {
		t.Errorf("%s = %q, want S1 removed", consts.AnnotationWekaFullDrives, live.Annotations[consts.AnnotationWekaFullDrives])
	}

	// A Done child alone is not enough: the serials must actually show up as shared.
	child.Status.Status = "Done"
	if updateErr := c.Update(context.Background(), child); updateErr != nil {
		t.Fatalf("update child: %v", updateErr)
	}
	err = op.advanceInFlight(context.Background(), 0)
	asMigrationWaitError(t, err)
	if op.results.Containers[0].SubPhase != MigrateDriveSharingSubPhaseSigning {
		t.Errorf("subPhase = %q, want it held at Signing while S1 is not in the shared annotation", op.results.Containers[0].SubPhase)
	}

	live.Annotations[consts.AnnotationSharedDrives] = `[{"physical_uuid":"u1","serial":"S1","capacity_gib":10,"type":"TLC"}]`
	if updateErr := c.Update(context.Background(), live); updateErr != nil {
		t.Fatalf("update node: %v", updateErr)
	}
	err = op.advanceInFlight(context.Background(), 0)
	asMigrationWaitError(t, err)
	if op.results.Containers[0].SubPhase != MigrateDriveSharingSubPhaseProxy {
		t.Errorf("subPhase = %q, want Proxy once the serials are shared", op.results.Containers[0].SubPhase)
	}
}

// A failed child stays inspectable for one backoff window, then is deleted so the next cycle
// recreates it.
func TestAdvanceSigningParksOnAFailedChildThenRecreatesIt(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	failed := &weka.WekaManualOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "migrate-sign-drive-0", Namespace: migrationTestNamespace},
		Spec:       weka.WekaManualOperationSpec{Action: weka.WekaManualOperationActionSignDrives},
		Status:     weka.WekaManualOperationStatus{Status: "Failed", Result: "device /dev/nvme0n1 is busy"},
	}
	c := newMigrationFakeClient(t, migrationCluster(), migrationNode(nil), failed)

	owner := &weka.WekaManualOperation{ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: migrationTestNamespace}}
	op := newMigrationOp(t, c, owner, nil)
	op.cluster = migrationCluster()
	op.results.Containers = []MigrateToDriveSharingContainerState{{
		Node: migrationTestNode, Container: "drive-0",
		Phase: MigrateDriveSharingPhaseInFlight, SubPhase: MigrateDriveSharingSubPhaseSigning,
		Serials: []string{"S1"}, SignAttempts: 1,
	}}

	asMigrationWaitError(t, op.advanceInFlight(context.Background(), 0))
	state := &op.results.Containers[0]
	if !strings.Contains(state.Reason, "is busy") {
		t.Errorf("reason = %q, want the child's own result", state.Reason)
	}
	if state.SignFailedAt == nil {
		t.Fatal("SignFailedAt not stamped, so the child would be deleted before anyone could read it")
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: migrationTestNamespace, Name: "migrate-sign-drive-0"}, &weka.WekaManualOperation{}); err != nil {
		t.Errorf("failed child must survive the first park: %v", err)
	}

	// Backoff elapsed: the child is removed, and the pass after that recreates it.
	expired := metav1.NewTime(time.Now().Add(-2 * migrateDriveSharingSignRetryBackoff))
	state.SignFailedAt = &expired
	asMigrationWaitError(t, op.advanceInFlight(context.Background(), 0))
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: migrationTestNamespace, Name: "migrate-sign-drive-0"}, &weka.WekaManualOperation{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected the failed child to be deleted, got %v", err)
	}

	asMigrationWaitError(t, op.advanceInFlight(context.Background(), 0))
	if op.results.Containers[0].SignAttempts != 2 {
		t.Errorf("signAttempts = %d, want 2 after the retry", op.results.Containers[0].SignAttempts)
	}
}

// The replacement is found by set difference: a sharing drive container of this cluster that was
// not in the campaign's original set and that nobody else has claimed.
func TestAdvanceRejoiningCompletesOnTheReplacement(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	replacement := weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "drive-9", Namespace: migrationTestNamespace},
		Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, ContainerCapacity: 1000, NodeAffinity: migrationTestNode},
		Status: weka.WekaContainerStatus{
			Status:      weka.Running,
			Allocations: &weka.ContainerAllocations{VirtualDrives: []weka.VirtualDrive{{VirtualUUID: "v1", PhysicalUUID: "u1", CapacityGiB: 1000}}},
		},
	}
	recorder := events.NewFakeRecorder(20)
	op := newMigrationOp(t, newMigrationFakeClient(t, migrationCluster()), &weka.WekaManualOperation{}, []weka.WekaContainer{replacement})
	op.recorder = recorder
	op.cluster = migrationCluster()
	op.results.Total = 1
	op.results.Containers = []MigrateToDriveSharingContainerState{{
		Node: migrationTestNode, Container: "drive-0",
		Phase: MigrateDriveSharingPhaseInFlight, SubPhase: MigrateDriveSharingSubPhaseRejoining,
		Serials: []string{"S1"},
	}}

	err := op.advanceInFlight(context.Background(), 0)

	asMigrationWaitError(t, err)
	state := op.results.Containers[0]
	if state.Phase != MigrateDriveSharingPhaseDone {
		t.Fatalf("phase = %q, want Done", state.Phase)
	}
	if state.Replacement != "drive-9" {
		t.Errorf("replacement = %q, want drive-9", state.Replacement)
	}
	if op.results.Done != 1 {
		t.Errorf("done = %d, want 1", op.results.Done)
	}
	// The node-complete event is recorded on the operation and mirrored onto the cluster.
	var nodeComplete int
	for done := false; !done; {
		select {
		case event := <-recorder.Events:
			if strings.Contains(event, migrateDriveSharingEventReasonNodeComplete) {
				nodeComplete++
			}
		default:
			done = true
		}
	}
	if nodeComplete != 2 {
		t.Errorf("NodeComplete events = %d, want 2 (operation + cluster)", nodeComplete)
	}
}

func TestAdvanceRejoiningParksUntilTheReplacementIsReady(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	notReady := weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "drive-9", Namespace: migrationTestNamespace},
		Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, ContainerCapacity: 1000, NodeAffinity: migrationTestNode},
		Status:     weka.WekaContainerStatus{Status: weka.Running},
	}
	op := newMigrationOp(t, newMigrationFakeClient(t, migrationCluster()), &weka.WekaManualOperation{}, []weka.WekaContainer{notReady})
	op.cluster = migrationCluster()
	op.results.Containers = []MigrateToDriveSharingContainerState{{
		Node: migrationTestNode, Container: "drive-0",
		Phase: MigrateDriveSharingPhaseInFlight, SubPhase: MigrateDriveSharingSubPhaseRejoining,
	}}

	asMigrationWaitError(t, op.advanceInFlight(context.Background(), 0))
	if op.results.Containers[0].Phase != MigrateDriveSharingPhaseInFlight {
		t.Errorf("phase = %q, want it held InFlight until virtual drives are allocated", op.results.Containers[0].Phase)
	}
	if !strings.Contains(op.results.Containers[0].Reason, "virtual drives") {
		t.Errorf("reason = %q, want it to name the missing virtual drives", op.results.Containers[0].Reason)
	}
}

// A 1Gi proxy pod already running the spec's own hugepages request must not be reported as
// drifted, or advanceProxy would restart a proxy that has nothing to gain from it.
func TestProxyHugepagesDriftedNotDriftedWhenThePodIsAtSpec(t *testing.T) {
	proxy := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "ssdproxy", Namespace: migrationTestNamespace},
		Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeSSDProxy, Hugepages: 4000, HugepagesSize: "1Gi"},
	}
	name, want := resources.HugepagesRequest(proxy)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: proxy.Name, Namespace: migrationTestNamespace},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:      consts.WekaContainerName,
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{name: want}},
		}}},
	}
	op := newMigrationOp(t, newMigrationFakeClient(t, pod), &weka.WekaManualOperation{}, nil)

	drifted, err := op.proxyHugepagesDrifted(context.Background(), proxy)

	if err != nil {
		t.Fatalf("proxyHugepagesDrifted: %v", err)
	}
	if drifted {
		t.Error("drifted = true, want false: the pod already requests exactly what the spec asks for")
	}
}

// The campaign set is frozen after the first cycle, so a sharing container created later is this
// campaign's own replacement rather than a new Skipped target inflating the totals.
func TestPlanMigrationContainersFreezesTheSetAfterTheFirstCycle(t *testing.T) {
	first := planMigrationContainers(nil,
		[]migrationTarget{{name: "drive-0", node: "node-a"}},
		[]migrationTarget{{name: "drive-1", node: "node-b"}})
	if len(first) != 2 {
		t.Fatalf("first plan = %+v, want one Pending and one Skipped", first)
	}

	second := planMigrationContainers(first,
		nil, // drive-0 has been drained away
		[]migrationTarget{{name: "drive-1", node: "node-b"}, {name: "drive-9", node: "node-a"}})
	if len(second) != 2 {
		t.Fatalf("second plan = %+v, want the replacement drive-9 excluded", second)
	}
	for _, s := range second {
		if s.Container == "drive-9" {
			t.Error("the replacement was added to the campaign set")
		}
	}
}

func TestChildSignOpNameFitsTheObjectNameLimit(t *testing.T) {
	long := childSignOpName(strings.Repeat("a", 70), strings.Repeat("b", 40))
	if len(long) > maxChildOpNameLength {
		t.Errorf("name length = %d, want <= %d", len(long), maxChildOpNameLength)
	}
	if childSignOpName("migrate", "drive-0") != "migrate-sign-drive-0" {
		t.Errorf("short names must stay readable, got %q", childSignOpName("migrate", "drive-0"))
	}
}

// Finalize drops the migration annotation: with the campaign over, the validator exception is no
// longer needed and would silently permit the next unrelated mode flip.
func TestFinalizeClearsTheMigrationAnnotation(t *testing.T) {
	cluster := migrationCluster()
	c := newMigrationFakeClient(t, cluster)
	op := newMigrationOp(t, c, &weka.WekaManualOperation{}, nil)
	op.cluster = cluster
	var succeeded int
	op.successCallback = func(context.Context) error { succeeded++; return nil }

	if err := op.finalize(context.Background()); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	live := &weka.WekaCluster{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: migrationTestNamespace, Name: migrationTestCluster}, live); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	if _, ok := live.Annotations[consts.AnnotationSizingModeMigration]; ok {
		t.Error("migration annotation was not removed")
	}
	if succeeded != 1 {
		t.Errorf("successCallback calls = %d, want 1", succeeded)
	}
}

// The whole campaign state round-trips through status.result, which is what makes the operation
// resumable after an operator restart.
func TestResultRoundTripsThroughTheOwnerStatus(t *testing.T) {
	op := &MigrateToDriveSharingOperation{results: MigrateToDriveSharingResult{
		Cluster: "ns/c", Total: 2, Done: 1,
		Containers: []MigrateToDriveSharingContainerState{{
			Node: "node-a", Container: "drive-0", Phase: MigrateDriveSharingPhaseInFlight,
			SubPhase: MigrateDriveSharingSubPhaseSigning, Serials: []string{"S1"}, SignAttempts: 2,
		}},
	}}
	owner := &weka.WekaManualOperation{Status: weka.WekaManualOperationStatus{Result: op.GetJsonResult()}}

	resumed := &MigrateToDriveSharingOperation{ownerRef: owner}
	resumed.rehydrateFrom(resumed.previousResult())

	if len(resumed.results.Containers) != 1 {
		t.Fatalf("containers = %+v, want the persisted one", resumed.results.Containers)
	}
	state := resumed.results.Containers[0]
	if state.SubPhase != MigrateDriveSharingSubPhaseSigning || state.SignAttempts != 2 || strings.Join(state.Serials, ",") != "S1" {
		t.Errorf("resumed state = %+v, want the sub-phase, attempts and serials preserved", state)
	}

	var decoded MigrateToDriveSharingResult
	if err := json.Unmarshal([]byte(op.GetJsonResult()), &decoded); err != nil {
		t.Fatalf("GetJsonResult is not valid JSON: %v", err)
	}
}

// The child sign operation self-deletes after its deletionDelay, so its absence must never be read
// as "never signed": that would re-run an erasing sign over drives the replacement already holds.
func TestAdvanceSigningTrustsTheNodeAnnotationOverTheChildOperation(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	node := migrationNode(map[string]string{
		consts.AnnotationSharedDrives: `[{"physical_uuid":"u1","serial":"S1","capacity_gib":10,"type":"TLC"}]`,
	})
	c := newMigrationFakeClient(t, migrationCluster(), node)

	owner := &weka.WekaManualOperation{ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: migrationTestNamespace}}
	op := newMigrationOp(t, c, owner, nil)
	op.cluster = migrationCluster()
	op.results.Containers = []MigrateToDriveSharingContainerState{{
		Node: migrationTestNode, Container: "drive-0",
		Phase: MigrateDriveSharingPhaseInFlight, SubPhase: MigrateDriveSharingSubPhaseSigning,
		Serials: []string{"S1"}, SignAttempts: 1,
	}}

	asMigrationWaitError(t, op.advanceInFlight(context.Background(), 0))

	if op.results.Containers[0].SubPhase != MigrateDriveSharingSubPhaseProxy {
		t.Errorf("subPhase = %q, want Proxy: the serials are already shared", op.results.Containers[0].SubPhase)
	}
	if op.results.Containers[0].SignAttempts != 1 {
		t.Errorf("signAttempts = %d, want 1 (no second sign may be created)", op.results.Containers[0].SignAttempts)
	}
	children := &weka.WekaManualOperationList{}
	if err := c.List(context.Background(), children); err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(children.Items) != 0 {
		t.Errorf("created %d sign operations, want none", len(children.Items))
	}
}

// The SignDone latch must survive the child's self-delete: once a child reported Done while
// serials were still missing, a later cycle finding the child gone must stay parked rather than
// read the absence as "never signed" and create a fresh child over drives already handed out.
func TestAdvanceSigningNeverRecreatesAChildAfterADoneLatch(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	c := newMigrationFakeClient(t, migrationCluster(), migrationNode(nil))
	owner := &weka.WekaManualOperation{ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: migrationTestNamespace}}
	op := newMigrationOp(t, c, owner, nil)
	op.cluster = migrationCluster()
	op.results.Containers = []MigrateToDriveSharingContainerState{{
		Node: migrationTestNode, Container: "drive-0",
		Phase: MigrateDriveSharingPhaseInFlight, SubPhase: MigrateDriveSharingSubPhaseSigning,
		Serials: []string{"S1"},
	}}

	asMigrationWaitError(t, op.advanceInFlight(context.Background(), 0))

	child := &weka.WekaManualOperation{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: migrationTestNamespace, Name: "migrate-sign-drive-0"}, child); err != nil {
		t.Fatalf("expected the child sign-drives operation to be created: %v", err)
	}

	child.Status.Status = "Done"
	if err := c.Update(context.Background(), child); err != nil {
		t.Fatalf("update child: %v", err)
	}
	asMigrationWaitError(t, op.advanceInFlight(context.Background(), 0))
	if !op.results.Containers[0].SignDone {
		t.Fatal("SignDone must latch once a Done child leaves serials missing")
	}

	if err := c.Delete(context.Background(), child); err != nil {
		t.Fatalf("delete child: %v", err)
	}

	asMigrationWaitError(t, op.advanceInFlight(context.Background(), 0))

	if err := c.Get(context.Background(), client.ObjectKey{Namespace: migrationTestNamespace, Name: "migrate-sign-drive-0"}, &weka.WekaManualOperation{}); !apierrors.IsNotFound(err) {
		t.Errorf("get child after latch: err = %v, want NotFound (no recreate)", err)
	}
	if !strings.Contains(op.results.Containers[0].Reason, "S1") {
		t.Errorf("reason = %q, want it to still name the missing serial", op.results.Containers[0].Reason)
	}
}

// findReplacement prefers the exact node match. With no match it falls back to an unclaimed,
// scheduled sharing container created after this entry started (count-based sizing can place the
// replacement on a spare node); older containers and unscheduled ones are never adopted.
func TestFindReplacementPrefersDrainedNodeThenNewerElsewhere(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	started := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	newer := metav1.NewTime(started.Add(time.Minute))
	older := metav1.NewTime(started.Add(-time.Minute))
	mk := func(name, node string, created metav1.Time) weka.WekaContainer {
		return weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: migrationTestNamespace, CreationTimestamp: created},
			Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, ContainerCapacity: 1000, NodeAffinity: weka.NodeName(node)},
			Status:     weka.WekaContainerStatus{Status: weka.Running},
		}
	}
	unscheduled := mk("drive-10", "", newer)

	find := func(t *testing.T, containers ...weka.WekaContainer) *weka.WekaContainer {
		op := newMigrationOp(t, newMigrationFakeClient(t, migrationCluster()), &weka.WekaManualOperation{}, containers)
		op.cluster = migrationCluster()
		state := &MigrateToDriveSharingContainerState{Node: migrationTestNode, Container: "drive-0", StartedAt: &started}
		got, err := op.findReplacement(context.Background(), state)
		if err != nil {
			t.Fatalf("findReplacement: %v", err)
		}
		return got
	}

	t.Run("exact node match wins over a newer container elsewhere", func(t *testing.T) {
		got := find(t, mk("drive-11", "node-b", newer), mk("drive-9", string(migrationTestNode), newer), unscheduled)
		if got == nil || got.Name != "drive-9" {
			t.Errorf("replacement = %+v, want drive-9", got)
		}
	})

	t.Run("falls back to a newer scheduled container elsewhere", func(t *testing.T) {
		got := find(t, mk("drive-11", "node-b", newer), unscheduled)
		if got == nil || got.Name != "drive-11" {
			t.Errorf("replacement = %+v, want drive-11", got)
		}
	})

	t.Run("never adopts an older container elsewhere or an unscheduled one", func(t *testing.T) {
		if got := find(t, mk("drive-11", "node-b", older), unscheduled); got != nil {
			t.Errorf("replacement = %+v, want nil", got)
		}
	})

	t.Run("a replacement elsewhere completes the node with a warning event", func(t *testing.T) {
		elsewhere := mk("drive-11", "node-b", newer)
		elsewhere.Status.Allocations = &weka.ContainerAllocations{VirtualDrives: []weka.VirtualDrive{{VirtualUUID: "v1", PhysicalUUID: "u1", CapacityGiB: 1000}}}
		recorder := events.NewFakeRecorder(20)
		op := newMigrationOp(t, newMigrationFakeClient(t, migrationCluster()), &weka.WekaManualOperation{},
			[]weka.WekaContainer{elsewhere})
		op.recorder = recorder
		op.cluster = migrationCluster()
		op.results.Total = 1
		op.results.Containers = []MigrateToDriveSharingContainerState{{
			Node: migrationTestNode, Container: "drive-0", StartedAt: &started,
			Phase: MigrateDriveSharingPhaseInFlight, SubPhase: MigrateDriveSharingSubPhaseRejoining,
		}}

		asMigrationWaitError(t, op.advanceInFlight(context.Background(), 0))

		state := op.results.Containers[0]
		if state.Phase != MigrateDriveSharingPhaseDone || state.Replacement != "drive-11" {
			t.Fatalf("state = %+v, want Done with replacement drive-11", state)
		}
		var elsewhereEvents int
		for done := false; !done; {
			select {
			case event := <-recorder.Events:
				if strings.Contains(event, migrateDriveSharingEventReasonReplacementElsewhere) {
					elsewhereEvents++
				}
			default:
				done = true
			}
		}
		if elsewhereEvents != 2 {
			t.Errorf("ReplacementElsewhere events = %d, want 2 (operation + cluster)", elsewhereEvents)
		}
	})
}

// The weka.io/drives status must be recomputed even when this call's serials are not present in
// either annotation, so a stale value left by a prior call whose status write failed still gets
// fixed on a later, unrelated call rather than waiting for its own annotation to change.
func TestRemoveFullDrivesFromNodeAlwaysRecomputesTheStatus(t *testing.T) {
	node := migrationNode(map[string]string{
		consts.AnnotationWekaFullDrives: `[{"serial":"S1","capacity_gib":10},{"serial":"S2","capacity_gib":10}]`,
	})
	node.Status.Capacity = corev1.ResourceList{consts.ResourceDrives: resource.MustParse("5")}
	node.Status.Allocatable = corev1.ResourceList{consts.ResourceDrives: resource.MustParse("5")}
	c := newMigrationFakeClient(t, node)

	// S9 is in neither annotation, so both annotation writes are skipped entirely this call.
	if err := RemoveFullDrivesFromNode(context.Background(), c, migrationTestNode, []string{"S9"}); err != nil {
		t.Fatalf("RemoveFullDrivesFromNode: %v", err)
	}

	live := &corev1.Node{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: migrationTestNode}, live); err != nil {
		t.Fatalf("get node: %v", err)
	}
	want := resource.MustParse("2")
	if got := live.Status.Capacity[consts.ResourceDrives]; got.Cmp(want) != 0 {
		t.Errorf("%s = %s, want %s (recomputed from the annotations even though neither changed)",
			consts.ResourceDrives, got.String(), want.String())
	}
}

// A node whose drives are only in the legacy annotation must not gain an empty weka-full-drives
// annotation: its mere presence flips the guards that treat the node as never signed.
func TestRemoveFullDrivesFromNodeLeavesALegacyOnlyNodeWithoutAFullDrivesAnnotation(t *testing.T) {
	node := migrationNode(map[string]string{
		consts.AnnotationWekaDrives: `["S1","S2"]`,
	})
	c := newMigrationFakeClient(t, node)

	if err := RemoveFullDrivesFromNode(context.Background(), c, migrationTestNode, []string{"S1"}); err != nil {
		t.Fatalf("RemoveFullDrivesFromNode: %v", err)
	}

	live := &corev1.Node{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: migrationTestNode}, live); err != nil {
		t.Fatalf("get node: %v", err)
	}
	if _, ok := live.Annotations[consts.AnnotationWekaFullDrives]; ok {
		t.Errorf("%s = %q, want it still absent", consts.AnnotationWekaFullDrives, live.Annotations[consts.AnnotationWekaFullDrives])
	}
	if live.Annotations[consts.AnnotationWekaDrives] != `["S2"]` {
		t.Errorf("%s = %q, want S1 removed", consts.AnnotationWekaDrives, live.Annotations[consts.AnnotationWekaDrives])
	}
}

// A shared sign-drives policy is the documented follow-up between tenants and cannot reclaim drives
// as exclusive, so it must not park the next tenant's campaign.
func TestPlanIgnoresASharedSignDrivesPolicy(t *testing.T) {
	withOperatorNamespace(t, migrationTestNamespace)

	policy := &weka.WekaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "sign-shared", Namespace: migrationTestNamespace},
		Spec: weka.WekaPolicySpec{
			Type:    weka.WekaPolicyTypeSignDrives,
			Payload: weka.PolicyPayload{SignDrives: &weka.SignDrivesPayload{Type: "all-not-root", Shared: true}},
		},
	}
	c := newMigrationFakeClient(t, migrationCluster(), migrationNode(nil), policy)
	op := newMigrationOp(t, c, &weka.WekaManualOperation{}, nil)

	if err := op.refuseIfSignPolicyMatches(context.Background(), []weka.NodeName{migrationTestNode}); err != nil {
		t.Fatalf("shared policy must not block the campaign: %v", err)
	}
}

// Without a stripe width provisioned capacity cannot be inflated to raw; the check must not pass.
func TestCheckCapacityFailsClosedWithoutAStripeWidth(t *testing.T) {
	op := newMigrationOp(t, newMigrationFakeClient(t), &weka.WekaManualOperation{}, nil)
	op.wekaStatus = func(context.Context, *weka.WekaCluster) (services.WekaStatusResponse, error) {
		return services.WekaStatusResponse{Capacity: services.WekaStatusCapacity{
			TotalBytes: 100 * gibBytes, UnprovisionedBytes: 50 * gibBytes,
		}}, nil
	}

	if err := op.checkCapacity(context.Background(), migrationCluster()); err == nil {
		t.Fatal("expected an error when weka status has no stripe width")
	}
	if op.results.CapacityChecked {
		t.Error("CapacityChecked latched without a stripe width")
	}
}
