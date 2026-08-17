package wekacluster

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/capacityplanner"
	globalconfig "github.com/weka/weka-operator/internal/config"
)

// withGCTimeout sets UnschedulablePlannerContainerGCTimeout for the test and restores it on cleanup.
func withGCTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := globalconfig.Config.UnschedulablePlannerContainerGCTimeout
	globalconfig.Config.UnschedulablePlannerContainerGCTimeout = d
	t.Cleanup(func() { globalconfig.Config.UnschedulablePlannerContainerGCTimeout = prev })
}

// pinnedAutoFullDrivesContainer builds a namespaced, named auto-full-drives (exclusive, NumDrives-sized) drive
// container, unlike autoFullDrivesDriveContainer (funcs_upgrade_test.go) which leaves NumDrives at 0.
func pinnedAutoFullDrivesContainer(name, node string, numDrives int, age time.Duration, scheduled bool) *weka.WekaContainer {
	c := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
	}
	c.Spec.Mode = weka.WekaContainerModeDrive
	c.Spec.NumDrives = numDrives
	c.Spec.NodeAffinity = weka.NodeName(node)
	if scheduled {
		c.Status.NodeAffinity = weka.NodeName(node)
	}
	return c
}

// pinnedClusterCapacityContainer builds a namespaced clusterCapacity drive container with a controllable
// age/scheduled state.
func pinnedClusterCapacityContainer(name, node string, capGiB int, age time.Duration, scheduled bool) *weka.WekaContainer {
	c := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
	}
	c.Spec.Mode = weka.WekaContainerModeDrive
	c.Spec.ContainerCapacity = capGiB
	c.Spec.NodeAffinity = weka.NodeName(node)
	if scheduled {
		c.Status.NodeAffinity = weka.NodeName(node)
	}
	return c
}

// pinnedComputeContainer builds a namespaced compute container pinned via Spec.NodeAffinity together with its
// pod. scheduled=true sets Status.NodeAffinity and a Running pod; scheduled=false leaves Status.NodeAffinity
// empty and gives the pod a confirmed PodScheduled=False/Unschedulable condition. node="" returns an unpinned
// container and a nil pod.
func pinnedComputeContainer(name, node string, age time.Duration, scheduled bool) (*weka.WekaContainer, *v1.Pod) {
	c := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
	}
	c.Spec.Mode = weka.WekaContainerModeCompute
	c.Spec.NodeAffinity = weka.NodeName(node)
	if node == "" {
		return c, nil
	}
	if scheduled {
		c.Status.NodeAffinity = weka.NodeName(node)
	}

	pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	if scheduled {
		pod.Spec.NodeName = node
		pod.Status.Phase = v1.PodRunning
		pod.Status.Conditions = []v1.PodCondition{{Type: v1.PodScheduled, Status: v1.ConditionTrue}}
	} else {
		pod.Status.Phase = v1.PodPending
		// age drives the pod condition's LastTransitionTime, not just CreationTimestamp, so the GC's timeout
		// reads off the scheduler's verdict.
		pod.Status.Conditions = []v1.PodCondition{unschedulableCondition(age)}
	}
	return c, pod
}

// unschedulableCondition builds a PodScheduled=False/Reason=Unschedulable condition whose verdict landed
// unschedulableFor ago. The Message is what the GC reports, so it carries realistic scheduler text.
func unschedulableCondition(unschedulableFor time.Duration) v1.PodCondition {
	return v1.PodCondition{
		Type:               v1.PodScheduled,
		Status:             v1.ConditionFalse,
		Reason:             "Unschedulable",
		Message:            "0/3 nodes are available: insufficient cpu",
		LastTransitionTime: metav1.NewTime(time.Now().Add(-unschedulableFor)),
	}
}

// unschedulablePod builds a Pending pod carrying a confirmed PodScheduled=False/Unschedulable condition,
// named to match a drive container so GarbageCollectUnschedulablePlannerContainers's pod fetch (by container
// name/namespace) finds it. unschedulableFor should mirror the paired container's age.
func unschedulablePod(name string, unschedulableFor time.Duration) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Status: v1.PodStatus{
			Phase:      v1.PodPending,
			Conditions: []v1.PodCondition{unschedulableCondition(unschedulableFor)},
		},
	}
}

// newUpgradeLoopWithPods is newUpgradeLoop plus pod objects, needed for compute-GC tests:
// GarbageCollectUnschedulablePlannerContainers fetches a compute container's pod separately (the
// wekaClusterReconcilerLoop has no r.pod field like the wekacontainer package does). Nil pods are skipped so
// callers can pass the nil second value pinnedComputeContainer returns for an unpinned container.
func newUpgradeLoopWithPods(t *testing.T, cluster *weka.WekaCluster, containers []*weka.WekaContainer, pods []*v1.Pod) *wekaClusterReconcilerLoop {
	t.Helper()
	objs := make([]client.Object, 0, len(containers)+len(pods)+1)
	objs = append(objs, cluster)
	for _, c := range containers {
		objs = append(objs, c)
	}
	for _, p := range pods {
		if p == nil {
			continue
		}
		objs = append(objs, p)
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

// fetchContainerState re-reads a container's Spec.State: SetContainerStateDeleting applies via a merge
// patch, not an in-memory mutation, so the post-call state must be read back through the client.
func fetchContainerState(t *testing.T, loop *wekaClusterReconcilerLoop, name string) weka.ContainerState {
	t.Helper()
	var got weka.WekaContainer
	if err := loop.getClient().Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, &got); err != nil {
		t.Fatalf("fetch container %s: %v", name, err)
	}
	return got.Spec.State
}

// An unscheduled auto-full-drives container older than the GC timeout, with a confirmed scheduling failure,
// must be marked ContainerStateDeleting, exactly like clusterCapacity already was.
func TestGarbageCollectUnschedulableDriveContainers_AutoFullDrivesOldUnscheduledIsDeleted(t *testing.T) {
	withGCTimeout(t, 2*time.Minute)
	c := pinnedAutoFullDrivesContainer("drive-old-unscheduled", "node-1", 4, 10*time.Minute, false)
	pod := unschedulablePod(c.Name, 10*time.Minute)
	loop := newUpgradeLoopWithPods(t, testClusterFor(t), []*weka.WekaContainer{c}, []*v1.Pod{pod})

	if err := loop.GarbageCollectUnschedulablePlannerContainers(context.Background()); err != nil {
		t.Fatalf("GarbageCollectUnschedulablePlannerContainers: %v", err)
	}
	if got := fetchContainerState(t, loop, c.Name); got != weka.ContainerStateDeleting {
		t.Errorf("state = %q, want %q: an old, confirmed-unschedulable auto-full-drives container must now be GC'd", got, weka.ContainerStateDeleting)
	}
}

// TestGarbageCollectUnschedulableDriveContainers_AutoFullDrivesYoungUnscheduledNotDeleted proves the timeout is
// still honored: an unscheduled auto-full-drives container younger than it must be left alone.
func TestGarbageCollectUnschedulableDriveContainers_AutoFullDrivesYoungUnscheduledNotDeleted(t *testing.T) {
	withGCTimeout(t, 2*time.Minute)
	c := pinnedAutoFullDrivesContainer("drive-young-unscheduled", "node-1", 4, 30*time.Second, false)
	loop := newUpgradeLoop(t, testClusterFor(t), []*weka.WekaContainer{c})

	if err := loop.GarbageCollectUnschedulablePlannerContainers(context.Background()); err != nil {
		t.Fatalf("GarbageCollectUnschedulablePlannerContainers: %v", err)
	}
	if got := fetchContainerState(t, loop, c.Name); got == weka.ContainerStateDeleting {
		t.Errorf("state = %q, want unchanged: an auto-full-drives container unscheduled for less than the GC timeout must not be deleted", got)
	}
}

// TestGarbageCollectUnschedulableDriveContainers_AutoFullDrivesScheduledNotDeletedRegardlessOfAge proves a
// once-scheduled auto-full-drives container is never targeted by this GC, no matter how old.
func TestGarbageCollectUnschedulableDriveContainers_AutoFullDrivesScheduledNotDeletedRegardlessOfAge(t *testing.T) {
	withGCTimeout(t, 2*time.Minute)
	c := pinnedAutoFullDrivesContainer("drive-old-scheduled", "node-1", 4, 24*time.Hour, true)
	loop := newUpgradeLoop(t, testClusterFor(t), []*weka.WekaContainer{c})

	if err := loop.GarbageCollectUnschedulablePlannerContainers(context.Background()); err != nil {
		t.Fatalf("GarbageCollectUnschedulablePlannerContainers: %v", err)
	}
	if got := fetchContainerState(t, loop, c.Name); got == weka.ContainerStateDeleting {
		t.Errorf("state = %q, want unchanged: a scheduled auto-full-drives container must never be GC'd regardless of age", got)
	}
}

// TestGarbageCollectUnschedulableDriveContainers_ClusterCapacityBehaviorUnchanged pins down that widening the
// guard to also catch auto full drives left clusterCapacity's behavior unchanged, modulo the new confirmed-
// scheduling-failure gate: oldUnscheduled now needs an Unschedulable pod condition to be reaped.
func TestGarbageCollectUnschedulableDriveContainers_ClusterCapacityBehaviorUnchanged(t *testing.T) {
	withGCTimeout(t, 2*time.Minute)
	oldUnscheduled := pinnedClusterCapacityContainer("cc-old-unscheduled", "node-1", 1024, 10*time.Minute, false)
	youngUnscheduled := pinnedClusterCapacityContainer("cc-young-unscheduled", "node-1", 1024, 30*time.Second, false)
	oldScheduled := pinnedClusterCapacityContainer("cc-old-scheduled", "node-1", 1024, 24*time.Hour, true)
	pod := unschedulablePod(oldUnscheduled.Name, 10*time.Minute)
	loop := newUpgradeLoopWithPods(t, testClusterFor(t), []*weka.WekaContainer{oldUnscheduled, youngUnscheduled, oldScheduled}, []*v1.Pod{pod})

	if err := loop.GarbageCollectUnschedulablePlannerContainers(context.Background()); err != nil {
		t.Fatalf("GarbageCollectUnschedulablePlannerContainers: %v", err)
	}
	if got := fetchContainerState(t, loop, oldUnscheduled.Name); got != weka.ContainerStateDeleting {
		t.Errorf("old unscheduled clusterCapacity container state = %q, want %q", got, weka.ContainerStateDeleting)
	}
	if got := fetchContainerState(t, loop, youngUnscheduled.Name); got == weka.ContainerStateDeleting {
		t.Errorf("young unscheduled clusterCapacity container state = %q, want unchanged", got)
	}
	if got := fetchContainerState(t, loop, oldScheduled.Name); got == weka.ContainerStateDeleting {
		t.Errorf("old scheduled clusterCapacity container state = %q, want unchanged", got)
	}
}

// TestGarbageCollectUnschedulablePlannerContainers_ComputeOldUnschedulablePinnedIsDeleted is the compute happy
// path: pinned, old enough, and carrying a confirmed scheduling failure, so it is reaped and the planner can
// place those cores on a node that can actually take them.
func TestGarbageCollectUnschedulablePlannerContainers_ComputeOldUnschedulablePinnedIsDeleted(t *testing.T) {
	withGCTimeout(t, 2*time.Minute)
	c, pod := pinnedComputeContainer("compute-old-unscheduled", "node-1", 10*time.Minute, false)
	loop := newUpgradeLoopWithPods(t, testClusterFor(t), []*weka.WekaContainer{c}, []*v1.Pod{pod})

	if err := loop.GarbageCollectUnschedulablePlannerContainers(context.Background()); err != nil {
		t.Fatalf("GarbageCollectUnschedulablePlannerContainers: %v", err)
	}
	if got := fetchContainerState(t, loop, c.Name); got != weka.ContainerStateDeleting {
		t.Errorf("state = %q, want %q: an old, confirmed-unschedulable, pinned compute container must be GC'd", got, weka.ContainerStateDeleting)
	}
}

// TestGarbageCollectUnschedulablePlannerContainers_ComputeYoungUnscheduledNotDeleted proves the timeout is
// still honored for compute: a confirmed-unschedulable pinned compute container younger than it must be
// left alone.
func TestGarbageCollectUnschedulablePlannerContainers_ComputeYoungUnscheduledNotDeleted(t *testing.T) {
	withGCTimeout(t, 2*time.Minute)
	c, pod := pinnedComputeContainer("compute-young-unscheduled", "node-1", 30*time.Second, false)
	loop := newUpgradeLoopWithPods(t, testClusterFor(t), []*weka.WekaContainer{c}, []*v1.Pod{pod})

	if err := loop.GarbageCollectUnschedulablePlannerContainers(context.Background()); err != nil {
		t.Fatalf("GarbageCollectUnschedulablePlannerContainers: %v", err)
	}
	if got := fetchContainerState(t, loop, c.Name); got == weka.ContainerStateDeleting {
		t.Errorf("state = %q, want unchanged: a compute container unschedulable for less than the GC timeout must not be deleted", got)
	}
}

// TestGarbageCollectUnschedulablePlannerContainers_OldContainerFreshlyUnschedulableNotDeleted pins the clock
// the timeout runs on: the scheduler's verdict, not the container's creation. A container that has existed for
// hours but whose pod became unschedulable seconds ago has not had its grace period yet.
func TestGarbageCollectUnschedulablePlannerContainers_OldContainerFreshlyUnschedulableNotDeleted(t *testing.T) {
	withGCTimeout(t, 2*time.Minute)
	for _, tt := range []struct {
		name      string
		container *weka.WekaContainer
	}{
		{"drive", pinnedAutoFullDrivesContainer("drive-old-fresh-fail", "node-1", 4, 24*time.Hour, false)},
		{"compute", func() *weka.WekaContainer {
			c, _ := pinnedComputeContainer("compute-old-fresh-fail", "node-1", 24*time.Hour, false)
			return c
		}()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pod := unschedulablePod(tt.container.Name, 5*time.Second)
			loop := newUpgradeLoopWithPods(t, testClusterFor(t), []*weka.WekaContainer{tt.container}, []*v1.Pod{pod})

			if err := loop.GarbageCollectUnschedulablePlannerContainers(context.Background()); err != nil {
				t.Fatalf("GarbageCollectUnschedulablePlannerContainers: %v", err)
			}
			if got := fetchContainerState(t, loop, tt.container.Name); got == weka.ContainerStateDeleting {
				t.Errorf("state = %q, want unchanged: a %s container unschedulable for only 5s must not be deleted, "+
					"however old the container itself is", got, tt.name)
			}
		})
	}
}

// TestGarbageCollectUnschedulablePlannerContainers_EventCarriesSchedulerMessage proves the event reports the
// scheduler's own per-node explanation. The condition's Reason is always the literal "Unschedulable" on this
// path, so reporting that instead would duplicate the event reason and tell an operator nothing.
func TestGarbageCollectUnschedulablePlannerContainers_EventCarriesSchedulerMessage(t *testing.T) {
	withGCTimeout(t, 2*time.Minute)
	c := pinnedAutoFullDrivesContainer("drive-msg", "node-1", 4, 10*time.Minute, false)
	pod := unschedulablePod(c.Name, 10*time.Minute)
	loop := newUpgradeLoopWithPods(t, testClusterFor(t), []*weka.WekaContainer{c}, []*v1.Pod{pod})

	if err := loop.GarbageCollectUnschedulablePlannerContainers(context.Background()); err != nil {
		t.Fatalf("GarbageCollectUnschedulablePlannerContainers: %v", err)
	}

	recorder, ok := loop.Recorder.(*record.FakeRecorder)
	if !ok {
		t.Fatalf("Recorder is %T, want *record.FakeRecorder", loop.Recorder)
	}
	close(recorder.Events)
	var found string
	for e := range recorder.Events {
		if strings.Contains(e, "UnschedulableDriveContainer") {
			found = e
		}
	}
	if found == "" {
		t.Fatal("no UnschedulableDriveContainer event recorded")
	}
	if !strings.Contains(found, "insufficient cpu") {
		t.Errorf("event %q must carry the scheduler's Message (\"insufficient cpu\"), the only actionable text on this path", found)
	}
}

// TestGarbageCollectUnschedulablePlannerContainers_ComputeScheduledNotDeletedRegardlessOfAge proves a
// compute container whose pod actually bound and is running is never targeted by this GC, no matter how
// old, since there is no confirmed scheduling failure to act on.
func TestGarbageCollectUnschedulablePlannerContainers_ComputeScheduledNotDeletedRegardlessOfAge(t *testing.T) {
	withGCTimeout(t, 2*time.Minute)
	c, pod := pinnedComputeContainer("compute-old-scheduled", "node-1", 24*time.Hour, true)
	loop := newUpgradeLoopWithPods(t, testClusterFor(t), []*weka.WekaContainer{c}, []*v1.Pod{pod})

	if err := loop.GarbageCollectUnschedulablePlannerContainers(context.Background()); err != nil {
		t.Fatalf("GarbageCollectUnschedulablePlannerContainers: %v", err)
	}
	if got := fetchContainerState(t, loop, c.Name); got == weka.ContainerStateDeleting {
		t.Errorf("state = %q, want unchanged: a compute container whose pod is actually running must never be GC'd regardless of age", got)
	}
}

// TestGarbageCollectUnschedulablePlannerContainers_ComputeUnpinnedNotDeleted proves an unpinned compute
// container (Spec.NodeAffinity == "") is never touched by this GC: deletePodIfUnschedulable
// (flow_active_state.go) already owns that case at the pod granularity, and handling it here too would
// double-reap the same stall.
func TestGarbageCollectUnschedulablePlannerContainers_ComputeUnpinnedNotDeleted(t *testing.T) {
	withGCTimeout(t, 2*time.Minute)
	c, pod := pinnedComputeContainer("compute-unpinned", "", 24*time.Hour, false)
	loop := newUpgradeLoopWithPods(t, testClusterFor(t), []*weka.WekaContainer{c}, []*v1.Pod{pod})

	if err := loop.GarbageCollectUnschedulablePlannerContainers(context.Background()); err != nil {
		t.Fatalf("GarbageCollectUnschedulablePlannerContainers: %v", err)
	}
	if got := fetchContainerState(t, loop, c.Name); got == weka.ContainerStateDeleting {
		t.Errorf("state = %q, want unchanged: an unpinned compute container must be left to deletePodIfUnschedulable, not this GC", got)
	}
}

// TestGarbageCollectUnschedulablePlannerContainers_DriveUnpinnedNotDeleted is the drive-side counterpart of
// ComputeUnpinnedNotDeleted: the pin, not the capacity flavor, qualifies a container for this GC. An unpinned
// drive container is deletePodIfUnschedulable's case. NumDrives is set to prove the old flavor check alone
// would have matched it.
func TestGarbageCollectUnschedulablePlannerContainers_DriveUnpinnedNotDeleted(t *testing.T) {
	withGCTimeout(t, 2*time.Minute)
	c := pinnedAutoFullDrivesContainer("drive-unpinned", "", 4, 24*time.Hour, false)
	pod := unschedulablePod(c.Name, 24*time.Hour)
	loop := newUpgradeLoopWithPods(t, testClusterFor(t), []*weka.WekaContainer{c}, []*v1.Pod{pod})

	if err := loop.GarbageCollectUnschedulablePlannerContainers(context.Background()); err != nil {
		t.Fatalf("GarbageCollectUnschedulablePlannerContainers: %v", err)
	}
	if got := fetchContainerState(t, loop, c.Name); got == weka.ContainerStateDeleting {
		t.Errorf("state = %q, want unchanged: an unpinned drive container must be left to deletePodIfUnschedulable, not this GC", got)
	}
}

// runningComputeContainer builds a minimal compute container already Status.Status == Running. It is scenery
// for the test below: the condition that once suppressed the reap.
func runningComputeContainer(name string) *weka.WekaContainer {
	c := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
	}
	c.Spec.Mode = weka.WekaContainerModeCompute
	c.Status.Status = weka.Running
	return c
}

// TestGarbageCollectUnschedulablePlannerContainers_ComputeReapedRegardlessOfRunningCount pins that compute is
// reaped on the same terms as drives, with no "only once it blocks cluster formation" escape: autoKeptCompute
// counts an unscheduled pinned container's cores into keptCores unconditionally, so leaving it in place would
// hide a permanent compute shortfall behind a ratio that looks satisfied on paper.
func TestGarbageCollectUnschedulablePlannerContainers_ComputeReapedRegardlessOfRunningCount(t *testing.T) {
	withGCTimeout(t, 2*time.Minute)
	running := runningComputeContainer("compute-running")
	c, pod := pinnedComputeContainer("compute-old-unscheduled", "node-2", 10*time.Minute, false)
	loop := newUpgradeLoopWithPods(t, testClusterFor(t), []*weka.WekaContainer{running, c}, []*v1.Pod{pod})

	if err := loop.GarbageCollectUnschedulablePlannerContainers(context.Background()); err != nil {
		t.Fatalf("GarbageCollectUnschedulablePlannerContainers: %v", err)
	}
	if got := fetchContainerState(t, loop, c.Name); got != weka.ContainerStateDeleting {
		t.Errorf("state = %q, want %q: a confirmed-unschedulable pinned compute container must be reaped even "+
			"while other compute is Running — its cores are counted but never served", got, weka.ContainerStateDeleting)
	}
	if got := fetchContainerState(t, loop, running.Name); got == weka.ContainerStateDeleting {
		t.Errorf("state = %q: the healthy Running compute container must not be touched", got)
	}
}

// TestGarbageCollectUnschedulablePlannerContainers_ComputePendingWithoutUnschedulableReasonNotDeleted proves
// the GC requires a confirmed scheduling failure, not merely a pod that hasn't run yet: a Pending pod with no
// PodScheduled=False/Unschedulable condition must be left alone, so the GC never fights a maintenance window
// or a merely-slow scheduler.
func TestGarbageCollectUnschedulablePlannerContainers_ComputePendingWithoutUnschedulableReasonNotDeleted(t *testing.T) {
	withGCTimeout(t, 2*time.Minute)
	c, _ := pinnedComputeContainer("compute-pending-no-reason", "node-1", 10*time.Minute, false)
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: c.Name, Namespace: "default"},
		Status:     v1.PodStatus{Phase: v1.PodPending}, // no PodScheduled condition at all yet
	}
	loop := newUpgradeLoopWithPods(t, testClusterFor(t), []*weka.WekaContainer{c}, []*v1.Pod{pod})

	if err := loop.GarbageCollectUnschedulablePlannerContainers(context.Background()); err != nil {
		t.Fatalf("GarbageCollectUnschedulablePlannerContainers: %v", err)
	}
	if got := fetchContainerState(t, loop, c.Name); got == weka.ContainerStateDeleting {
		t.Errorf("state = %q, want unchanged: a merely-Pending pod without a confirmed Unschedulable condition must not be GC'd", got)
	}
}

// TestGarbageCollectUnschedulablePlannerContainers_ComputePreviouslyScheduledNotDeleted proves the shared
// "never scheduled" skip protects a compute container that bound once and later lost its pod, even when its
// current pod shows a confirmed scheduling failure old enough to clear the timeout. Status.NodeAffinity being
// set means the container carries real cluster state and needs deactivation, not a GC reap.
func TestGarbageCollectUnschedulablePlannerContainers_ComputePreviouslyScheduledNotDeleted(t *testing.T) {
	withGCTimeout(t, 2*time.Minute)
	c, pod := pinnedComputeContainer("compute-previously-scheduled", "node-1", 10*time.Minute, false)
	c.Status.NodeAffinity = "node-1" // bound once: carries cluster state, must never be reaped by this GC
	loop := newUpgradeLoopWithPods(t, testClusterFor(t), []*weka.WekaContainer{c}, []*v1.Pod{pod})

	if err := loop.GarbageCollectUnschedulablePlannerContainers(context.Background()); err != nil {
		t.Fatalf("GarbageCollectUnschedulablePlannerContainers: %v", err)
	}
	if got := fetchContainerState(t, loop, c.Name); got == weka.ContainerStateDeleting {
		t.Errorf("state = %q, want unchanged: a compute container that bound once (Status.NodeAffinity set) must never be GC'd, regardless of its current pod's scheduling state, age, or running-compute headroom", got)
	}
}

// TestGarbageCollectUnschedulablePlannerContainers_FailingContainerDoesNotBlockOthers proves a delete failure
// on one container does not abort the whole GC pass: a later, reapable container must still be reaped, and
// the failure must still surface in the returned error. The failing container's delete-patch is forced into a
// conflict by corrupting its in-memory ResourceVersion, mirroring a concurrent write racing this GC.
func TestGarbageCollectUnschedulablePlannerContainers_FailingContainerDoesNotBlockOthers(t *testing.T) {
	withGCTimeout(t, 2*time.Minute)
	failing := pinnedAutoFullDrivesContainer("drive-failing", "node-1", 4, 10*time.Minute, false)
	ok := pinnedAutoFullDrivesContainer("drive-ok", "node-2", 4, 10*time.Minute, false)
	failingPod := unschedulablePod(failing.Name, 10*time.Minute)
	okPod := unschedulablePod(ok.Name, 10*time.Minute)
	loop := newUpgradeLoopWithPods(t, testClusterFor(t), []*weka.WekaContainer{failing, ok}, []*v1.Pod{failingPod, okPod})

	// Overwriting the ResourceVersion after seeding makes the delete patch a conflict, like a concurrent write.
	failing.ResourceVersion = "stale-does-not-match"

	err := loop.GarbageCollectUnschedulablePlannerContainers(context.Background())
	if err == nil {
		t.Fatal("GarbageCollectUnschedulablePlannerContainers: want an error naming the failing container, got nil")
	}
	if !strings.Contains(err.Error(), failing.Name) {
		t.Errorf("error %q does not name the failing container %q", err.Error(), failing.Name)
	}
	if got := fetchContainerState(t, loop, ok.Name); got != weka.ContainerStateDeleting {
		t.Errorf("state = %q, want %q: a later container must still be reaped even though an earlier one failed to delete", got, weka.ContainerStateDeleting)
	}
}

// testClusterFor builds a minimal, namespaced WekaCluster to seed newUpgradeLoop's fake client.
func testClusterFor(t *testing.T) *weka.WekaCluster {
	t.Helper()
	return &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me", Namespace: "default"}}
}

// autoFullDrivesComputeCluster builds a minimal auto-full-drives cluster: the empty dynamic template is what
// routes buildPlannerComputeContainers (shared by both planner modes) down the layout-hugepages branch.
func autoFullDrivesComputeCluster(t *testing.T) *weka.WekaCluster {
	t.Helper()
	c := testClusterFor(t)
	c.Spec.Dynamic = &weka.WekaClusterTemplate{}
	return c
}

// An auto-full-drives compute container must be created with the planner's hugepages, not the capacity-blind
// `3000 * cores` fallback: at compute-creation time the drive containers' Status.Allocations isn't populated
// yet, so ComputeCapacityFromAutoFullDrivesDriveContainers collapses to that fallback and never recomputes
// afterward. Lab-verified: fallback gave 39832/42896 MiB at 13/14 cores where the planner's figure was 41350/43114.
func TestBuildClusterCapacityComputeContainers_AutoFullDrivesTakesHugepagesFromPlannerLayout(t *testing.T) {
	const (
		cores               = 13
		plannedHugepagesMiB = 41350 // the planner's own greenfield figure for the lab fleet
	)
	cluster := autoFullDrivesComputeCluster(t)
	loop := newUpgradeLoop(t, cluster, nil)

	layout := []capacityplanner.ComputeContainerSpec{
		{Node: "node-1", NumCores: cores, HugepagesMiB: plannedHugepagesMiB},
	}

	built, skipped := loop.buildPlannerComputeContainers(context.Background(), layout, nil)
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v, want none — the planner already supplied this container's hugepages, so "+
			"nothing about drive-container allocation readiness may block its creation", skipped)
	}
	if len(built) != 1 {
		t.Fatalf("built %d container(s), want 1", len(built))
	}

	got := built[0]
	if got.Spec.NumCores != cores {
		t.Errorf("Spec.NumCores = %d, want %d (straight from the layout entry)", got.Spec.NumCores, cores)
	}
	if string(got.Spec.NodeAffinity) != "node-1" {
		t.Errorf("Spec.NodeAffinity = %q, want node-1", got.Spec.NodeAffinity)
	}
	if got.Spec.Hugepages != plannedHugepagesMiB {
		t.Errorf("Spec.Hugepages = %d, want %d — the planner's per-container figure must be used verbatim; "+
			"it already includes the DPDK per-core term (ComputeContainerHugepagesMiB mirrors "+
			"GetContainerHugepages's dpdk tail), so it must be neither recomputed nor topped up",
			got.Spec.Hugepages, plannedHugepagesMiB)
	}
	if floor := 3000 * cores; got.Spec.Hugepages <= floor {
		t.Errorf("Spec.Hugepages = %d, want strictly > the capacity-blind fallback floor %d (3000*%d) — "+
			"landing at or below it is the exact signature of the bug", got.Spec.Hugepages, floor, cores)
	}
}

// TestBuildClusterCapacityComputeContainers_AutoFullDrivesUserPinnedHugepagesStillWins guards that an explicit
// dynamicTemplate.computeHugepages still outranks the planner's derived figure.
func TestBuildClusterCapacityComputeContainers_AutoFullDrivesUserPinnedHugepagesStillWins(t *testing.T) {
	const userPinned = 12345
	cluster := autoFullDrivesComputeCluster(t)
	cluster.Spec.Dynamic.ComputeHugepages = userPinned
	loop := newUpgradeLoop(t, cluster, nil)

	layout := []capacityplanner.ComputeContainerSpec{{Node: "node-1", NumCores: 13, HugepagesMiB: 41350}}

	built, skipped := loop.buildPlannerComputeContainers(context.Background(), layout, nil)
	if len(built) != 1 {
		t.Fatalf("built %d container(s), want 1 (skipped: %v)", len(built), skipped)
	}
	if got := built[0].Spec.Hugepages; got != userPinned {
		t.Errorf("Spec.Hugepages = %d, want the user-pinned %d — the planner must never override an "+
			"explicit dynamicTemplate.computeHugepages", got, userPinned)
	}
}

// existingComputeContainer builds a namespaced, node-pinned compute container at a given size, standing in
// for a live auto-full-drives compute container the planner may decide to grow. Hugepages apply as a ratchet:
// a rise is written (the running pod would otherwise be under-reserved until recreated), a fall never is (the
// pod's immutable limit already holds the larger value, and writing the lower figure would tell the planner
// capacity was freed that wasn't).
func TestApplyPlannerComputeGrowth_AppliesHugepagesOnlyChanges(t *testing.T) {
	up := existingComputeContainer("hp-up", "node-1", 6, 18000, 584)
	down := existingComputeContainer("hp-down", "node-2", 6, 22000, 584)
	same := existingComputeContainer("hp-same", "node-3", 6, 20000, 584)

	cluster := autoFullDrivesComputeCluster(t)
	loop := newUpgradeLoop(t, cluster, []*weka.WekaContainer{up, down, same})

	plan := &capacityplanner.CapacityPlan{
		ComputeContainers: 3,
		ComputeLayout: []capacityplanner.ComputeContainerSpec{
			{Node: "node-1", NumCores: 6, HugepagesMiB: 21890},
			{Node: "node-2", NumCores: 6, HugepagesMiB: 18362},
			{Node: "node-3", NumCores: 6, HugepagesMiB: 20000},
		},
	}

	if err := loop.applyPlannerComputeGrowth(context.Background(), plan); err != nil {
		t.Fatalf("applyPlannerComputeGrowth: %v", err)
	}

	if up.Spec.NumCores != 6 || up.Spec.Hugepages != 21890 {
		t.Errorf("hp-up = %d cores / %d hugepages, want 6 / 21890 — a risen capacity term must be applied "+
			"even though cores did not move", up.Spec.NumCores, up.Spec.Hugepages)
	}
	if down.Spec.NumCores != 6 || down.Spec.Hugepages != 22000 {
		t.Errorf("hp-down = %d cores / %d hugepages, want 6 / 22000 unchanged — a fallen capacity term is "+
			"never written back; the pod already holds the larger limit and keeps it until it is next "+
			"recreated for its own reasons", down.Spec.NumCores, down.Spec.Hugepages)
	}
	if same.Spec.NumCores != 6 || same.Spec.Hugepages != 20000 {
		t.Errorf("hp-same = %d cores / %d hugepages, want 6 / 20000 unchanged — an entry equal to the "+
			"container is a no-op", same.Spec.NumCores, same.Spec.Hugepages)
	}

	recorder, ok := loop.Recorder.(*record.FakeRecorder)
	if !ok {
		t.Fatalf("Recorder is %T, want *record.FakeRecorder", loop.Recorder)
	}
	close(recorder.Events)
	var events []string
	for e := range recorder.Events {
		events = append(events, e)
	}
	if len(events) != 1 {
		t.Fatalf("events = %v, want exactly 1 (hp-down and hp-same are both no-ops)", events)
	}
	if !strings.Contains(events[0], "Warning CapacityGrowthApplied") {
		t.Errorf("event %q, want the Warning CapacityGrowthApplied for hp-up", events[0])
	}
	if strings.Contains(events[0], "CapacityReservationReduced") {
		t.Errorf("event %q must not be a CapacityReservationReduced — that reason is retired, a fall is "+
			"never applied", events[0])
	}
}

// TestApplyPlannerComputeGrowth_RatchetsCoresAndHugepagesIndependently covers a layout entry that raises cores
// while its freshly computed hugepages figure comes out below what the container already reserves: cores must
// still advance, but hugepages must keep the container's own higher figure. Both figures are realizable planner
// output (capacityplanner.ComputeContainerHugepagesMiB: max(capacityBased+1700*cores, 3000*cores) + 64*cores):
//   - existing, 6 cores: max(29300+1700*6, 3000*6) + 64*6 = 39500 + 384 = 39884.
//   - layout, 10 cores: max(10000+1700*10, 3000*10) + 64*10 = 30000 + 640 = 30640.
func TestApplyPlannerComputeGrowth_RatchetsCoresAndHugepagesIndependently(t *testing.T) {
	const (
		existingHugepages = 39884
		newCores          = 10
		layoutHugepages   = 30640 // < existingHugepages, despite belonging to the higher core count
	)
	mixed := existingComputeContainer("mixed", "node-1", 6, existingHugepages, 584)

	cluster := autoFullDrivesComputeCluster(t)
	loop := newUpgradeLoop(t, cluster, []*weka.WekaContainer{mixed})

	plan := &capacityplanner.CapacityPlan{
		ComputeContainers: 1,
		ComputeLayout: []capacityplanner.ComputeContainerSpec{
			{Node: "node-1", NumCores: newCores, HugepagesMiB: layoutHugepages},
		},
	}

	if err := loop.applyPlannerComputeGrowth(context.Background(), plan); err != nil {
		t.Fatalf("applyPlannerComputeGrowth: %v", err)
	}

	if mixed.Spec.NumCores != newCores {
		t.Errorf("mixed.Spec.NumCores = %d, want %d — the layout's cores are a real rise and must be applied",
			mixed.Spec.NumCores, newCores)
	}
	if mixed.Spec.Hugepages != existingHugepages {
		t.Errorf("mixed.Spec.Hugepages = %d, want %d retained — the layout's lower figure must not "+
			"overwrite the container's own higher one just because cores also changed",
			mixed.Spec.Hugepages, existingHugepages)
	}
	// The ratchet takes max(hp.Hugepages, existing), so the retained value can never fall below the layout's
	// own figure for the new core count.
	if mixed.Spec.Hugepages < layoutHugepages {
		t.Errorf("mixed.Spec.Hugepages = %d, below the layout's own %d-core figure of %d",
			mixed.Spec.Hugepages, newCores, layoutHugepages)
	}

	recorder, ok := loop.Recorder.(*record.FakeRecorder)
	if !ok {
		t.Fatalf("Recorder is %T, want *record.FakeRecorder", loop.Recorder)
	}
	close(recorder.Events)
	var events []string
	for e := range recorder.Events {
		events = append(events, e)
	}
	if len(events) != 1 || !strings.Contains(events[0], "Warning CapacityGrowthApplied") {
		t.Fatalf("events = %v, want exactly 1 Warning CapacityGrowthApplied (the cores rise)", events)
	}
}

func existingComputeContainer(name, node string, cores, hugepages, hugepagesOffset int) *weka.WekaContainer {
	c := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
	}
	c.Spec.Mode = weka.WekaContainerModeCompute
	c.Spec.NumCores = cores
	c.Spec.Hugepages = hugepages
	c.Spec.HugepagesOffset = hugepagesOffset
	c.Spec.NodeAffinity = weka.NodeName(node)
	c.Status.NodeAffinity = weka.NodeName(node)
	return c
}

// TestApplyAutoFullDrivesComputeGrowth_GrowsToLayoutAndNeverShrinks covers the apply half of the auto-full-drives
// grow-existing-compute fix: on a converged fleet every eligible node already hosts a compute container, so
// planComputeAutoFullDrives can only close a drive-core deficit by growing one in place. Exercises all three
// outcomes: below-target grows, at-target is a no-op, above-target is left alone.
func TestApplyAutoFullDrivesComputeGrowth_GrowsToLayoutAndNeverShrinks(t *testing.T) {
	grow := existingComputeContainer("grow-me", "node-1", 6, 18000, 200)
	frozen := existingComputeContainer("frozen-me", "node-2", 6, 18000, 200)
	shrink := existingComputeContainer("shrink-me", "node-3", 12, 36000, 200)

	cluster := autoFullDrivesComputeCluster(t)
	loop := newUpgradeLoop(t, cluster, []*weka.WekaContainer{grow, frozen, shrink})

	plan := &capacityplanner.CapacityPlan{
		ComputeContainers: 3,
		ComputeLayout: []capacityplanner.ComputeContainerSpec{
			{Node: "node-1", NumCores: 10, HugepagesMiB: 31700},
			{Node: "node-2", NumCores: 6, HugepagesMiB: 18000},
			{Node: "node-3", NumCores: 5, HugepagesMiB: 15000},
		},
	}

	if err := loop.applyPlannerComputeGrowth(context.Background(), plan); err != nil {
		t.Fatalf("applyAutoFullDrivesComputeGrowth: %v", err)
	}

	if grow.Spec.NumCores != 10 || grow.Spec.Hugepages != 31700 {
		t.Errorf("grow-me = %d cores / %d hugepages, want 10 / 31700 — a container below its layout target "+
			"must be grown to it, with the layout's own hugepages", grow.Spec.NumCores, grow.Spec.Hugepages)
	}
	if frozen.Spec.NumCores != 6 || frozen.Spec.Hugepages != 18000 {
		t.Errorf("frozen-me = %d cores / %d hugepages, want 6 / 18000 unchanged — a frozen layout entry is "+
			"a no-op", frozen.Spec.NumCores, frozen.Spec.Hugepages)
	}
	if shrink.Spec.NumCores != 12 || shrink.Spec.Hugepages != 36000 {
		t.Errorf("shrink-me = %d cores / %d hugepages, want 12 / 36000 unchanged — this path only ever "+
			"grows; shrinking a live compute container would pull cores out from under a running weka "+
			"process", shrink.Spec.NumCores, shrink.Spec.Hugepages)
	}

	// Exactly one CapacityGrowthApplied Warning, naming the recreate requirement, for the one container
	// that actually changed.
	recorder, ok := loop.Recorder.(*record.FakeRecorder)
	if !ok {
		t.Fatalf("Recorder is %T, want *record.FakeRecorder", loop.Recorder)
	}
	close(recorder.Events)
	var events []string
	for e := range recorder.Events {
		events = append(events, e)
	}
	if len(events) != 1 {
		t.Fatalf("events = %v, want exactly 1 (only grow-me changed)", events)
	}
	for _, want := range []string{"Warning", "CapacityGrowthApplied", "pod must be recreated"} {
		if !strings.Contains(events[0], want) {
			t.Errorf("event %q does not contain %q", events[0], want)
		}
	}
}

// TestApplyAutoFullDrivesComputeGrowth_ContainerOutsideLayoutUntouched proves a compute container whose node the
// planner did not lay out is never resized off some other node's target.
func TestApplyAutoFullDrivesComputeGrowth_ContainerOutsideLayoutUntouched(t *testing.T) {
	stray := existingComputeContainer("stray", "node-9", 4, 12000, 200)
	loop := newUpgradeLoop(t, autoFullDrivesComputeCluster(t), []*weka.WekaContainer{stray})

	plan := &capacityplanner.CapacityPlan{
		ComputeContainers: 1,
		ComputeLayout:     []capacityplanner.ComputeContainerSpec{{Node: "node-1", NumCores: 10, HugepagesMiB: 31700}},
	}
	if err := loop.applyPlannerComputeGrowth(context.Background(), plan); err != nil {
		t.Fatalf("applyAutoFullDrivesComputeGrowth: %v", err)
	}
	if stray.Spec.NumCores != 4 || stray.Spec.Hugepages != 12000 {
		t.Errorf("stray = %d cores / %d hugepages, want 4 / 12000 unchanged — node-9 is absent from the "+
			"layout, so there is no target to grow it to", stray.Spec.NumCores, stray.Spec.Hugepages)
	}
}

// growableAutoFullDrivesDriveContainer builds a namespaced, node-pinned auto-full-drives drive container at a
// given drives/cores size, the shape applyPlannerDriveGrowth edits in place.
func growableAutoFullDrivesDriveContainer(name, node string, numDrives, cores int) *weka.WekaContainer {
	c := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
	}
	c.Spec.Mode = weka.WekaContainerModeDrive
	c.Spec.NumDrives = numDrives
	c.Spec.NumCores = cores
	c.Spec.NodeAffinity = weka.NodeName(node)
	c.Status.NodeAffinity = weka.NodeName(node)
	return c
}

// newAutoFullDrivesGrowthLoop is newUpgradeLoop plus what the growth apply+announce path needs: a non-nil Throttler
// (RecordEventThrottled would nil-panic) and a recorder deep enough for the per-container
// CapacityGrowthApplied events plus the cluster-level one — FakeRecorder drops silently once full.
func newAutoFullDrivesGrowthLoop(t *testing.T, containers []*weka.WekaContainer) *wekaClusterReconcilerLoop {
	t.Helper()
	loop := newUpgradeLoop(t, autoFullDrivesComputeCluster(t), containers)
	loop.Throttler = throttling.NewSyncMapThrottler()
	loop.Recorder = record.NewFakeRecorder(16)
	return loop
}

// drainLoopEvents drains the loop's fake recorder once — call it once per loop and filter the result with
// eventsMatching — draining is destructive, so a second drain always looks empty.
func drainLoopEvents(t *testing.T, loop *wekaClusterReconcilerLoop) []string {
	t.Helper()
	rec, ok := loop.Recorder.(*record.FakeRecorder)
	if !ok {
		t.Fatalf("Recorder is %T, want *record.FakeRecorder", loop.Recorder)
	}
	return drainEvents(rec)
}

// eventsMatching filters already-drained events down to those whose text contains substr.
func eventsMatching(events []string, substr string) []string {
	var out []string
	for _, ev := range events {
		if strings.Contains(ev, substr) {
			out = append(out, ev)
		}
	}
	return out
}

// TestApplyAutoFullDrivesGrowth_AnnouncesOnlyGrowthItApplied is the regression test for an event that announced a
// plan the operator immediately declined (lab: growth events named containers whose specs never actually
// changed). Two containers exercise both halves: one below target must be grown and named, one already at
// target must be skipped and not named.
func TestApplyAutoFullDrivesGrowth_AnnouncesOnlyGrowthItApplied(t *testing.T) {
	grow := growableAutoFullDrivesDriveContainer("grow-me", "node-1", 5, 5)
	atTarget := growableAutoFullDrivesDriveContainer("at-target", "node-2", 6, 6)
	loop := newAutoFullDrivesGrowthLoop(t, []*weka.WekaContainer{grow, atTarget})

	plan := &capacityplanner.CapacityPlan{
		Grow: []capacityplanner.ContainerGrowth{
			{Name: "grow-me", NewNumDrives: 6, NewCores: 6, NewTlcGiB: 6000},
			{Name: "at-target", NewNumDrives: 6, NewCores: 6, NewTlcGiB: 6000},
		},
	}
	if err := loop.applyAndAnnounceGrowth(context.Background(), plan); err != nil {
		t.Fatalf("applyAndAnnounceGrowth: %v", err)
	}

	if grow.Spec.NumDrives != 6 || grow.Spec.NumCores != 6 {
		t.Errorf("grow-me = %d drive(s)/%d core(s), want 6/6 — a container below its target must be grown",
			grow.Spec.NumDrives, grow.Spec.NumCores)
	}
	if atTarget.Spec.NumDrives != 6 || atTarget.Spec.NumCores != 6 {
		t.Errorf("at-target = %d drive(s)/%d core(s), want 6/6 unchanged — this path only ever grows",
			atTarget.Spec.NumDrives, atTarget.Spec.NumCores)
	}

	got := eventsMatching(drainLoopEvents(t, loop), "AutoFullDrivesGrowthDetected")
	if len(got) != 1 {
		t.Fatalf("got %d AutoFullDrivesGrowthDetected event(s), want exactly 1; got: %v", len(got), got)
	}
	// The message must identify what grew, to what, and that a restart is owed.
	for _, want := range []string{"grow-me", "node-1", "6 drive(s)/6 core(s)", "growth applied", "must be recreated"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("AutoFullDrivesGrowthDetected message missing %q: %s", want, got[0])
		}
	}
	if strings.Contains(got[0], "at-target") {
		t.Errorf("AutoFullDrivesGrowthDetected names at-target, which was skipped as already at its target — the "+
			"event must report only what was written: %s", got[0])
	}
}

// TestApplyAutoFullDrivesGrowth_SilentWhenNothingIsApplied: the planner offers growth, every entry is declined, and
// the cluster event log must stay quiet. Both decline paths are exercised.
func TestApplyAutoFullDrivesGrowth_SilentWhenNothingIsApplied(t *testing.T) {
	for _, tc := range []struct {
		name       string
		containers []*weka.WekaContainer
		grow       []capacityplanner.ContainerGrowth
	}{
		{
			name:       "container already at target",
			containers: []*weka.WekaContainer{growableAutoFullDrivesDriveContainer("at-target", "node-1", 6, 6)},
			grow:       []capacityplanner.ContainerGrowth{{Name: "at-target", NewNumDrives: 6, NewCores: 6}},
		},
		{
			name:       "container absent from the reconcile",
			containers: []*weka.WekaContainer{growableAutoFullDrivesDriveContainer("present", "node-1", 6, 6)},
			grow:       []capacityplanner.ContainerGrowth{{Name: "ghost", NewNumDrives: 6, NewCores: 6}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loop := newAutoFullDrivesGrowthLoop(t, tc.containers)
			if err := loop.applyAndAnnounceGrowth(context.Background(), &capacityplanner.CapacityPlan{Grow: tc.grow}); err != nil {
				t.Fatalf("applyAndAnnounceGrowth: %v", err)
			}
			if got := eventsMatching(drainLoopEvents(t, loop), "AutoFullDrivesGrowth"); len(got) != 0 {
				t.Errorf("got %d AutoFullDrivesGrowth* event(s), want 0 — nothing was written, so there is nothing "+
					"to announce and no failure to report: %v", len(got), got)
			}
		})
	}
}

// TestApplyAutoFullDrivesGrowth_ReportsPartialApplyWhenBatchAborts covers an Update failure aborting the remaining
// entries: whatever was already written must still be announced, and a total abort must leave a trace
// rather than being silent. The failure is induced by a container in r.containers never seeded into the
// fake client, so client.Update returns NotFound.
func TestApplyAutoFullDrivesGrowth_ReportsPartialApplyWhenBatchAborts(t *testing.T) {
	t.Run("partial apply is announced", func(t *testing.T) {
		grow := growableAutoFullDrivesDriveContainer("grow-me", "node-1", 5, 5)
		orphan := growableAutoFullDrivesDriveContainer("orphan", "node-2", 5, 5)
		loop := newAutoFullDrivesGrowthLoop(t, []*weka.WekaContainer{grow})
		loop.containers = append(loop.containers, orphan) // in the reconcile, absent from the API server

		plan := &capacityplanner.CapacityPlan{Grow: []capacityplanner.ContainerGrowth{
			{Name: "grow-me", NewNumDrives: 6, NewCores: 6},
			{Name: "orphan", NewNumDrives: 6, NewCores: 6},
		}}
		if err := loop.applyAndAnnounceGrowth(context.Background(), plan); err == nil {
			t.Fatal("applyAndAnnounceGrowth returned nil, want the Update failure to propagate")
		}

		events := drainLoopEvents(t, loop)
		got := eventsMatching(events, "AutoFullDrivesGrowthDetected")
		if len(got) != 1 {
			t.Fatalf("got %d AutoFullDrivesGrowthDetected event(s), want 1 — the entry written before the abort must "+
				"still be reported; got: %v", len(got), got)
		}
		if !strings.Contains(got[0], "grow-me") || strings.Contains(got[0], "orphan") {
			t.Errorf("AutoFullDrivesGrowthDetected must name grow-me and not orphan: %s", got[0])
		}
		if deferred := eventsMatching(events, "AutoFullDrivesGrowthDeferred"); len(deferred) != 0 {
			t.Errorf("got %d AutoFullDrivesGrowthDeferred event(s), want 0 — growth did partially apply: %v",
				len(deferred), deferred)
		}
	})

	t.Run("total failure is reported as deferred", func(t *testing.T) {
		orphan := growableAutoFullDrivesDriveContainer("orphan", "node-1", 5, 5)
		loop := newAutoFullDrivesGrowthLoop(t, nil)
		loop.containers = []*weka.WekaContainer{orphan}

		plan := &capacityplanner.CapacityPlan{Grow: []capacityplanner.ContainerGrowth{
			{Name: "orphan", NewNumDrives: 6, NewCores: 6},
		}}
		if err := loop.applyAndAnnounceGrowth(context.Background(), plan); err == nil {
			t.Fatal("applyAndAnnounceGrowth returned nil, want the Update failure to propagate")
		}

		events := drainLoopEvents(t, loop)
		if got := eventsMatching(events, "AutoFullDrivesGrowthDetected"); len(got) != 0 {
			t.Errorf("got %d AutoFullDrivesGrowthDetected event(s), want 0 — nothing was written: %v", len(got), got)
		}
		got := eventsMatching(events, "AutoFullDrivesGrowthDeferred")
		if len(got) != 1 {
			t.Fatalf("got %d AutoFullDrivesGrowthDeferred event(s), want 1 — an abort that applies nothing must not "+
				"be silent on the WekaCluster; got: %v", len(got), got)
		}
		if !strings.Contains(got[0], "Warning") {
			t.Errorf("AutoFullDrivesGrowthDeferred must be a Warning: %s", got[0])
		}
	})
}

// TestApplyAutoFullDrivesGrowth_ContinuesPastAFailingEntry pins the batch semantics: a container that cannot
// be written must not cancel growth of containers after it in plan.Grow. The failing entry is first, so an
// abort-on-first-error regression would show up as grow-me staying at 5/5.
func TestApplyAutoFullDrivesGrowth_ContinuesPastAFailingEntry(t *testing.T) {
	grow := growableAutoFullDrivesDriveContainer("grow-me", "node-2", 5, 5)
	orphan := growableAutoFullDrivesDriveContainer("orphan", "node-1", 5, 5) // never seeded into the fake client
	loop := newAutoFullDrivesGrowthLoop(t, []*weka.WekaContainer{grow})
	loop.containers = append([]*weka.WekaContainer{orphan}, loop.containers...)

	plan := &capacityplanner.CapacityPlan{Grow: []capacityplanner.ContainerGrowth{
		{Name: "orphan", NewNumDrives: 6, NewCores: 6},
		{Name: "grow-me", NewNumDrives: 6, NewCores: 6},
	}}
	err := loop.applyAndAnnounceGrowth(context.Background(), plan)
	if err == nil {
		t.Fatal("applyAndAnnounceGrowth returned nil, want the failing entry's error to still propagate")
	}
	if !strings.Contains(err.Error(), "orphan") {
		t.Errorf("returned error must identify the container that failed, got: %v", err)
	}

	if grow.Spec.NumDrives != 6 || grow.Spec.NumCores != 6 {
		t.Errorf("grow-me = %d drive(s)/%d core(s), want 6/6 — a failure on an EARLIER entry must not cancel "+
			"the growth of a later, healthy one", grow.Spec.NumDrives, grow.Spec.NumCores)
	}

	got := eventsMatching(drainLoopEvents(t, loop), "AutoFullDrivesGrowthDetected")
	if len(got) != 1 {
		t.Fatalf("got %d AutoFullDrivesGrowthDetected event(s), want 1 — grow-me was written: %v", len(got), got)
	}
	if !strings.Contains(got[0], "1 of 2") {
		t.Errorf("a partial batch must say how many entries were left behind, got: %s", got[0])
	}
	// The count is the contract; names in this event mean "this grew", so the failed one must stay unnamed.
	if strings.Contains(got[0], "orphan") {
		t.Errorf("AutoFullDrivesGrowthDetected must not name a container that did NOT grow: %s", got[0])
	}
}

// TestApplyAutoFullDrivesGrowth_UsesLatestServerStateNotTheReconcileCopy covers the re-read needed because
// r.containers (listed once per reconcile) is routinely stale by the time growth is written. Here the server
// copy is already at the growth target while the reconcile's copy still says 5; the re-read must notice there
// is nothing to do and stay silent, rather than re-writing off the stale copy.
func TestApplyAutoFullDrivesGrowth_UsesLatestServerStateNotTheReconcileCopy(t *testing.T) {
	onServer := growableAutoFullDrivesDriveContainer("grow-me", "node-1", 6, 6)
	loop := newAutoFullDrivesGrowthLoop(t, []*weka.WekaContainer{onServer})

	// The reconcile's in-memory copy is behind the server's: same name, pre-growth size.
	stale := growableAutoFullDrivesDriveContainer("grow-me", "node-1", 5, 5)
	loop.containers = []*weka.WekaContainer{stale}

	plan := &capacityplanner.CapacityPlan{Grow: []capacityplanner.ContainerGrowth{
		{Name: "grow-me", NewNumDrives: 6, NewCores: 6},
	}}
	if err := loop.applyAndAnnounceGrowth(context.Background(), plan); err != nil {
		t.Fatalf("applyAndAnnounceGrowth: %v", err)
	}

	if got := eventsMatching(drainLoopEvents(t, loop), "AutoFullDrivesGrowth"); len(got) != 0 {
		t.Errorf("got %d AutoFullDrivesGrowth* event(s), want 0 — the server copy was already at the target, so "+
			"nothing was written and there is nothing to announce: %v", len(got), got)
	}
}

// applyAndAnnounceGrowth mirrors what buildPlannerDriveContainers does in production for the daemonset mode:
// apply the growth, then announce what was actually written. The applier itself does not emit the
// cluster-level event, since only one of the two modes announces.
func (r *wekaClusterReconcilerLoop) applyAndAnnounceGrowth(ctx context.Context, plan *capacityplanner.CapacityPlan) error {
	applied, failed, err := r.applyPlannerDriveGrowth(ctx, sizingAutoFullDrives, plan)
	r.announceDriveGrowth(plan, applied, failed, err)
	return err
}

// TestApplyPlannerDriveGrowth_CoresOnlyGrowthIsModeSpecific covers the one place the two planner modes'
// growth rules differ: clusterCapacity gates a growth entry on capacity increasing and must skip a cores-only
// entry, while the daemonset mode ratchets cores toward the derived count independently of drives and must
// apply it.
func TestApplyPlannerDriveGrowth_CoresOnlyGrowthIsModeSpecific(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      plannerSizing
		wantCores int
	}{
		{"clusterCapacity skips a growth that only raises cores", sizingClusterCapacity, 2},
		{"auto full drives applies it", sizingAutoFullDrives, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := growableAutoFullDrivesDriveContainer("c1", "node-1", 4, 2)
			c.Spec.ContainerCapacity = 1000 // clusterCapacity's own dimension, already at target
			loop := newAutoFullDrivesGrowthLoop(t, []*weka.WekaContainer{c})

			plan := &capacityplanner.CapacityPlan{Grow: []capacityplanner.ContainerGrowth{{
				Name: "c1",
				// Capacity and drives both unchanged; only cores are higher.
				NewTlcGiB: 1000, NewNumDrives: 4, NewCores: 5,
			}}}
			applied, failed, err := loop.applyPlannerDriveGrowth(context.Background(), tc.mode, plan)
			if err != nil {
				t.Fatalf("applyPlannerDriveGrowth: %v", err)
			}
			if failed != 0 {
				t.Errorf("failed = %d, want 0: no update error occurred", failed)
			}

			if c.Spec.NumCores != tc.wantCores {
				t.Errorf("NumCores = %d, want %d", c.Spec.NumCores, tc.wantCores)
			}
			wantApplied := 0
			if tc.mode == sizingAutoFullDrives {
				wantApplied = 1
			}
			if len(applied) != wantApplied {
				t.Errorf("reported %d applied growth(s), want %d — the announcement must reflect what was written",
					len(applied), wantApplied)
			}
		})
	}
}
