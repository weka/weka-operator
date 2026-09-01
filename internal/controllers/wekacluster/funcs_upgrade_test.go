package wekacluster

import (
	"context"
	"strings"
	"testing"

	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// fakeManagerWithClient, unlike fakeManagerNilClient (funcs_fd_planning_test.go), returns a real fake
// client — needed since HandleSpecUpdates performs actual Get/Patch/Status().Patch calls.
type fakeManagerWithClient struct {
	manager.Manager
	c client.Client
}

func (f fakeManagerWithClient) GetClient() client.Client { return f.c }

// newFakeClient builds a scheme-registered fake client seeded with objs, with WekaContainer wired for
// status subresource patches (HandleSpecUpdates ends with a Status().Patch) and WekaCluster wired for
// status subresource updates (handleUpgrade ends with a Status().Update).
func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1.AddToScheme: %v", err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("weka.AddToScheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&weka.WekaContainer{}, &weka.WekaCluster{}).
		Build()
}

// newUpgradeLoop builds a wekaClusterReconcilerLoop wired with a real fake client seeded with cluster and
// containers, ready to exercise HandleSpecUpdates end-to-end. Throttler is a real (not nil) SyncMapThrottler
// since FetchCluster — which normally wires it — is never called by tests that build the loop directly, and
// RecordEventThrottled panics on a nil Throttler.
func newUpgradeLoop(t *testing.T, cluster *weka.WekaCluster, containers []*weka.WekaContainer) *wekaClusterReconcilerLoop {
	t.Helper()
	objs := make([]client.Object, 0, len(containers)+1)
	objs = append(objs, cluster)
	for _, c := range containers {
		objs = append(objs, c)
	}
	fakeClient := newFakeClient(t, objs...)
	return &wekaClusterReconcilerLoop{
		Manager:    fakeManagerWithClient{c: fakeClient},
		cluster:    cluster,
		containers: containers,
		Recorder:   record.NewFakeRecorder(32),
		Throttler:  throttling.NewSyncMapThrottler(),
	}
}

// autoFullDrivesDriveContainer builds a namespaced, named auto-full-drives drive container pre-set with
// realistic per-node-planner-assigned multi-core cores/hugepages values, owned by the cluster.
// A deferred compute-hugepages reference must not fail HandleSpecUpdates: failing would abort the drive-side
// propagation that allocates the drives the reference is waiting on, deadlocking the raise.
func TestHandleSpecUpdates_DeferredComputeHugepagesDoNotBlockDrivePropagation(t *testing.T) {
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cl1", Namespace: "default", UID: "cluster-uid"},
	}
	// Count-based full drives: numDrives raised to 5 while the only drive container still holds 3, so the
	// compute capacity reference does not exist yet.
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{ComputeContainers: 1, DriveContainers: 1, NumDrives: 5}
	cluster.Spec.DriversDistService = "https://drivers.example/new"

	drive := autoFullDrivesDriveContainer("drive-1", 1, 2064, 664, 0)
	drive.Spec.NumDrives = 3
	drive.Status.Allocations = &weka.ContainerAllocations{
		Drives: []string{"serial-a", "serial-b", "serial-c"}, // 3 allocated, template wants 5
	}
	compute := &weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: "compute-1", Namespace: "default"}}
	compute.Spec.Mode = weka.WekaContainerModeCompute
	compute.Spec.NumCores = 1
	compute.Spec.Hugepages = 9090

	r := newUpgradeLoop(t, cluster, []*weka.WekaContainer{drive, compute})

	// The step must not fail: a deferral is a wait, not an error.
	if err := r.HandleSpecUpdates(context.Background()); err != nil {
		t.Fatalf("HandleSpecUpdates returned an error on a deferrable capacity reference: %v", err)
	}

	gotDrive := &weka.WekaContainer{}
	if err := r.getClient().Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "drive-1"}, gotDrive); err != nil {
		t.Fatalf("Get drive: %v", err)
	}
	if gotDrive.Spec.NumDrives != 5 {
		t.Errorf("drive NumDrives = %d, want 5 — the drive role must advance so the drives get allocated; "+
			"blocking it is what deadlocks the raise", gotDrive.Spec.NumDrives)
	}

	gotCompute := &weka.WekaContainer{}
	if err := r.getClient().Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "compute-1"}, gotCompute); err != nil {
		t.Fatalf("Get compute: %v", err)
	}
	if gotCompute.Spec.Hugepages != 9090 {
		t.Errorf("compute Hugepages = %d, want 9090 unchanged — a deferred role must keep its previous "+
			"sizing, never a zero or half-updated one", gotCompute.Spec.Hugepages)
	}
	if gotCompute.Status.LastAppliedSpec != "" {
		t.Errorf("compute LastAppliedSpec = %q, want empty — recording the hash on a deferred container "+
			"means the skipped sizing is never retried", gotCompute.Status.LastAppliedSpec)
	}
}

// upgradeLoopEvents drains the loop's fake recorder.
func upgradeLoopEvents(t *testing.T, r *wekaClusterReconcilerLoop) []string {
	t.Helper()
	rec, ok := r.Recorder.(*record.FakeRecorder)
	if !ok {
		t.Fatalf("Recorder is %T, want *record.FakeRecorder", r.Recorder)
	}
	close(rec.Events)
	var out []string
	for e := range rec.Events {
		out = append(out, e)
	}
	return out
}

// A raised spec.dynamicTemplate.numDrives must reach the drive containers. The drive role's hugepages are
// already computed from the template's numDrives, so a container left at the old count reserves for drives it
// never takes — the reservation and the drive count must move together, in one patch.
func TestHandleSpecUpdates_PropagatesRaisedNumDrives(t *testing.T) {
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cl1", Namespace: "default", UID: "cluster-uid"},
	}
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{NumDrives: 8, DriveCapacity: 3500}

	container := autoFullDrivesDriveContainer("drive-1", 5, 8520, 1520, 0)
	container.Spec.NumDrives = 6

	r := newUpgradeLoop(t, cluster, []*weka.WekaContainer{container})
	if err := r.HandleSpecUpdates(context.Background()); err != nil {
		t.Fatalf("HandleSpecUpdates: %v", err)
	}

	got := &weka.WekaContainer{}
	if err := r.getClient().Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "drive-1"}, got); err != nil {
		t.Fatalf("Get container: %v", err)
	}
	if got.Spec.NumDrives != 8 {
		t.Errorf("NumDrives = %d, want 8 — a raised template numDrives must propagate to the container",
			got.Spec.NumDrives)
	}

	events := upgradeLoopEvents(t, r)
	joined := strings.Join(events, "\n")
	if !strings.Contains(joined, "CapacityGrowthApplied") || !strings.Contains(joined, "drives to 8") {
		t.Errorf("events %q must announce the drive raise via CapacityGrowthApplied", joined)
	}
	if !strings.Contains(joined, "pod must be recreated") {
		t.Errorf("events %q must state that a pod recreation is owed", joined)
	}
}

// The drives-only case: numDrives rises while the capacity-derived core count does not. A drives-only raise
// must still rewrite the hugepages reservation and emit the growth event.
func TestHandleSpecUpdates_PropagatesDrivesOnlyRaise(t *testing.T) {
	prevDrive := globalconfig.Config.HugepagesUpdate.Drive
	globalconfig.Config.HugepagesUpdate.Drive = false // isolate: no independent hugepages propagation path
	t.Cleanup(func() { globalconfig.Config.HugepagesUpdate.Drive = prevDrive })

	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cl1", Namespace: "default", UID: "cluster-uid"},
	}
	// driveCores pinned, so the derived core count cannot move; only numDrives does.
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{NumDrives: 7, DriveCapacity: 3500, DriveCores: 5}

	container := autoFullDrivesDriveContainer("drive-1", 5, 8520, 1520, 0)
	container.Spec.NumDrives = 6

	r := newUpgradeLoop(t, cluster, []*weka.WekaContainer{container})
	if err := r.HandleSpecUpdates(context.Background()); err != nil {
		t.Fatalf("HandleSpecUpdates: %v", err)
	}

	got := &weka.WekaContainer{}
	if err := r.getClient().Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "drive-1"}, got); err != nil {
		t.Fatalf("Get container: %v", err)
	}
	if got.Spec.NumCores != 5 {
		t.Errorf("NumCores = %d, want 5 unchanged — this case raises drives only", got.Spec.NumCores)
	}
	if got.Spec.NumDrives != 7 {
		t.Errorf("NumDrives = %d, want 7", got.Spec.NumDrives)
	}
	if got.Spec.Hugepages == 8520 {
		t.Errorf("Hugepages still %d — a drives-only raise must still rewrite the reservation, which grows "+
			"by 200 MiB per added drive", got.Spec.Hugepages)
	}

	joined := strings.Join(upgradeLoopEvents(t, r), "\n")
	if !strings.Contains(joined, "drives to 7") {
		t.Errorf("events %q must announce a drives-only raise; it was silent before", joined)
	}
}

// numDrives is increase-only: weka cannot hand a drive back without a rebuild, so a template below the
// container's current count must leave it alone rather than shrink a running container.
func TestHandleSpecUpdates_NeverLowersNumDrives(t *testing.T) {
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cl1", Namespace: "default", UID: "cluster-uid"},
	}
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{NumDrives: 4, DriveCapacity: 3500}

	container := autoFullDrivesDriveContainer("drive-1", 6, 10384, 1984, 0)
	container.Spec.NumDrives = 8

	r := newUpgradeLoop(t, cluster, []*weka.WekaContainer{container})
	if err := r.HandleSpecUpdates(context.Background()); err != nil {
		t.Fatalf("HandleSpecUpdates: %v", err)
	}

	got := &weka.WekaContainer{}
	if err := r.getClient().Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "drive-1"}, got); err != nil {
		t.Fatalf("Get container: %v", err)
	}
	if got.Spec.NumDrives != 8 {
		t.Errorf("NumDrives = %d, want 8 unchanged — numDrives must never be lowered on a running container",
			got.Spec.NumDrives)
	}
}

// The planner owns drive sizing for auto-full-drives and clusterCapacity, so this path must not touch their
// numDrives any more than it touches their cores or hugepages.
func TestHandleSpecUpdates_PlannerManagedNumDrivesNotPropagated(t *testing.T) {
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cl1", Namespace: "default", UID: "cluster-uid"},
	}
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{NumDrives: 8} // no counts, no capacity => daemonset

	container := autoFullDrivesDriveContainer("drive-1", 6, 9984, 1584, 0)
	container.Spec.NumDrives = 6

	r := newUpgradeLoop(t, cluster, []*weka.WekaContainer{container})
	if err := r.HandleSpecUpdates(context.Background()); err != nil {
		t.Fatalf("HandleSpecUpdates: %v", err)
	}

	got := &weka.WekaContainer{}
	if err := r.getClient().Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "drive-1"}, got); err != nil {
		t.Fatalf("Get container: %v", err)
	}
	if got.Spec.NumDrives != 6 {
		t.Errorf("NumDrives = %d, want 6 unchanged — the planner owns sizing for auto-full-drives",
			got.Spec.NumDrives)
	}
}

// Only the drive role carries a drive count; a compute container must never receive one.
func TestHandleSpecUpdates_NumDrivesNotPropagatedToComputeRole(t *testing.T) {
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cl1", Namespace: "default", UID: "cluster-uid"},
	}
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{NumDrives: 8, DriveCapacity: 3500}

	compute := &weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: "compute-1", Namespace: "default"}}
	compute.Spec.Mode = weka.WekaContainerModeCompute
	compute.Spec.NumCores = 4

	r := newUpgradeLoop(t, cluster, []*weka.WekaContainer{compute})
	if err := r.HandleSpecUpdates(context.Background()); err != nil {
		t.Fatalf("HandleSpecUpdates: %v", err)
	}

	got := &weka.WekaContainer{}
	if err := r.getClient().Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "compute-1"}, got); err != nil {
		t.Fatalf("Get container: %v", err)
	}
	if got.Spec.NumDrives != 0 {
		t.Errorf("compute NumDrives = %d, want 0 — only the drive role has a drive count", got.Spec.NumDrives)
	}
}

func autoFullDrivesDriveContainer(name string, cores, hugepages, hugepagesOffset, dpdkBaseMemoryMb int) *weka.WekaContainer {
	c := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
	}
	c.Spec.Mode = weka.WekaContainerModeDrive
	c.Spec.NumCores = cores
	c.Spec.Hugepages = hugepages
	c.Spec.HugepagesOffset = hugepagesOffset
	c.Spec.DpdkBaseMemoryMb = dpdkBaseMemoryMb
	return c
}

// TestNewUpdatableClusterSpec_AutoFullDrivesDoesNotErrorOnHugepages guards that an auto-full-drives cluster
// (NumDrives==0) does not fall into GetContainerHugepages's full-drives path and error on "numDrives must be > 0".
func TestNewUpdatableClusterSpec_AutoFullDrivesDoesNotErrorOnHugepages(t *testing.T) {
	spec := &weka.WekaClusterSpec{
		Dynamic: &weka.WekaClusterTemplate{},
	}
	containers := []*weka.WekaContainer{autoFullDrivesDriveContainer("drive-1", 4, 8192, 1024, 0)}

	updatable, err := NewUpdatableClusterSpec(context.Background(), nil, spec, &metav1.ObjectMeta{}, containers)
	if err != nil {
		t.Fatalf("NewUpdatableClusterSpec on auto-full-drives spec: want no error, got %v", err)
	}

	// Confirms the computation was actually skipped (zero-value), not that the error just went elsewhere.
	if updatable.ComputeHugepages != (allocator.ContainerHugepages{}) {
		t.Errorf("want zero-value ComputeHugepages for auto full drives (planner-sized, discarded anyway), got %+v", updatable.ComputeHugepages)
	}
	if updatable.DriveHugepages != (allocator.ContainerHugepages{}) {
		t.Errorf("want zero-value DriveHugepages for auto full drives (planner-sized, discarded anyway), got %+v", updatable.DriveHugepages)
	}
}

// TestHandleSpecUpdates_AutoFullDrivesDriveContainerHugepagesNotOverwritten proves an auto-full-drives
// drive container's planner-assigned multi-core hugepages/cores survive HandleSpecUpdates unchanged.
//
// HugepagesUpdate.Drive is forced true so ShouldPropagateHugepages()/Offset() is true for the drive role,
// the condition under which this branch would otherwise clobber the values.
func TestHandleSpecUpdates_AutoFullDrivesDriveContainerHugepagesNotOverwritten(t *testing.T) {
	prevDrive := globalconfig.Config.HugepagesUpdate.Drive
	globalconfig.Config.HugepagesUpdate.Drive = true
	t.Cleanup(func() { globalconfig.Config.HugepagesUpdate.Drive = prevDrive })

	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cl1", Namespace: "default", UID: "cluster-uid"},
	}
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{}

	const (
		wantCores           = 4
		wantHugepages       = 8192
		wantHugepagesOffset = 1024
		wantDpdk            = 128
	)
	container := autoFullDrivesDriveContainer("drive-1", wantCores, wantHugepages, wantHugepagesOffset, wantDpdk)

	r := newUpgradeLoop(t, cluster, []*weka.WekaContainer{container})

	if err := r.HandleSpecUpdates(context.Background()); err != nil {
		t.Fatalf("HandleSpecUpdates: %v", err)
	}

	got := &weka.WekaContainer{}
	if err := r.getClient().Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "drive-1"}, got); err != nil {
		t.Fatalf("Get container after HandleSpecUpdates: %v", err)
	}

	if got.Spec.NumCores != wantCores {
		t.Errorf("NumCores: want unchanged %d, got %d", wantCores, got.Spec.NumCores)
	}
	if got.Spec.Hugepages != wantHugepages {
		t.Errorf("Hugepages: want unchanged %d, got %d (planner-assigned value was overwritten)", wantHugepages, got.Spec.Hugepages)
	}
	if got.Spec.HugepagesOffset != wantHugepagesOffset {
		t.Errorf("HugepagesOffset: want unchanged %d, got %d (planner-assigned value was overwritten)", wantHugepagesOffset, got.Spec.HugepagesOffset)
	}
	if got.Spec.DpdkBaseMemoryMb != wantDpdk {
		t.Errorf("DpdkBaseMemoryMb: want unchanged %d, got %d", wantDpdk, got.Spec.DpdkBaseMemoryMb)
	}
}

// TestHandleSpecUpdates_StaticClusterStillPropagatesHugepages proves a static (non-auto-full-drives,
// non-clusterCapacity) cluster still raises cores and propagates template-derived hugepages.
func TestHandleSpecUpdates_StaticClusterStillPropagatesHugepages(t *testing.T) {
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cl1", Namespace: "default", UID: "cluster-uid"},
	}
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{
		NumDrives:     3,
		DriveCapacity: 1024,
		DriveCores:    2, // template default: static clusters are sized from DriveCores
	}

	// Container starts at 1 core (below the template default of 2) with stale hugepages, matching the
	// pre-upgrade state HandleSpecUpdates is meant to reconcile forward.
	container := autoFullDrivesDriveContainer("drive-1", 1, 100, 10, 0)

	r := newUpgradeLoop(t, cluster, []*weka.WekaContainer{container})

	if err := r.HandleSpecUpdates(context.Background()); err != nil {
		t.Fatalf("HandleSpecUpdates: %v", err)
	}

	got := &weka.WekaContainer{}
	if err := r.getClient().Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "drive-1"}, got); err != nil {
		t.Fatalf("Get container after HandleSpecUpdates: %v", err)
	}

	if got.Spec.NumCores != 2 {
		t.Errorf("NumCores: want raised to template default 2, got %d (static-cluster propagation regressed)", got.Spec.NumCores)
	}
	// Expected: CalculateDriveHugepages/Offset(NumDrives=3, Cores.Drive=2) = 3400/600, plus default DPDK
	// base memory (+128) = 3528/728 — far from the stale seeded values (100/10), so an exact match proves
	// propagation ran rather than coincidence.
	if got.Spec.Hugepages != 3528 {
		t.Errorf("Hugepages: want propagated template-derived value 3528, got %d (static-cluster propagation regressed)", got.Spec.Hugepages)
	}
	if got.Spec.HugepagesOffset != 728 {
		t.Errorf("HugepagesOffset: want propagated template-derived value 728, got %d (static-cluster propagation regressed)", got.Spec.HugepagesOffset)
	}
}
