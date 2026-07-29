package wekacontainer

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

// newTestPod builds a minimal pod with the given creation time and phase, for the
// pure podNotRunningReason / podStuckSince / podStuckTimeoutElapsed helpers below.
func newTestPod(creationTime metav1.Time, phase v1.PodPhase) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: creationTime},
		Status:     v1.PodStatus{Phase: phase},
	}
}

func TestAppendMissingDrivesToBlocked(t *testing.T) {
	t.Run("defers when kernel view is incomplete", func(t *testing.T) {
		op := &operations.DriveNodeResults{
			RawDrives:          []operations.DriveRawInfo{{SerialId: "B1"}},
			KernelViewComplete: false,
		}
		blocked, missing := appendMissingDrivesToBlocked([]string{"A1", "A2"}, op, []string{"X"})
		if len(missing) != 0 {
			t.Fatalf("expected defer (no additions), got %v", missing)
		}
		if !slices.Equal(blocked, []string{"X"}) {
			t.Fatalf("expected blocked unchanged [X], got %v", blocked)
		}
	})

	t.Run("blocks missing serials when kernel view is complete", func(t *testing.T) {
		op := &operations.DriveNodeResults{
			RawDrives: []operations.DriveRawInfo{
				{SerialId: "B1"},
				{SerialId: "B2"},
			},
			KernelViewComplete: true,
		}
		blocked, missing := appendMissingDrivesToBlocked([]string{"A1", "A2", "B1"}, op, nil)
		slices.Sort(missing)
		if want := []string{"A1", "A2"}; !slices.Equal(missing, want) {
			t.Fatalf("expected missing=%v, got %v", want, missing)
		}
		slices.Sort(blocked)
		if want := []string{"A1", "A2"}; !slices.Equal(blocked, want) {
			t.Fatalf("expected blocked=%v, got %v", want, blocked)
		}
	})

	t.Run("no-op when all annotation serials are kernel-visible", func(t *testing.T) {
		op := &operations.DriveNodeResults{
			RawDrives: []operations.DriveRawInfo{
				{SerialId: "A1"},
				{SerialId: "A2"},
			},
			KernelViewComplete: true,
		}
		blocked, missing := appendMissingDrivesToBlocked([]string{"A1", "A2"}, op, []string{"X"})
		if len(missing) != 0 {
			t.Fatalf("expected no additions, got %v", missing)
		}
		if len(blocked) != 1 || blocked[0] != "X" {
			t.Fatalf("expected blocked=[X], got %v", blocked)
		}
	})

	t.Run("dedupes against existing blocked serials", func(t *testing.T) {
		op := &operations.DriveNodeResults{
			RawDrives:          []operations.DriveRawInfo{{SerialId: "B1"}},
			KernelViewComplete: true,
		}
		blocked, missing := appendMissingDrivesToBlocked([]string{"A1", "A2"}, op, []string{"A1"})
		if !slices.Equal(missing, []string{"A2"}) {
			t.Fatalf("expected missing=[A2], got %v", missing)
		}
		slices.Sort(blocked)
		if want := []string{"A1", "A2"}; !slices.Equal(blocked, want) {
			t.Fatalf("expected blocked=%v, got %v", want, blocked)
		}
	})

	t.Run("ignores empty serials in input", func(t *testing.T) {
		op := &operations.DriveNodeResults{
			RawDrives:          []operations.DriveRawInfo{{SerialId: ""}, {SerialId: "B1"}},
			KernelViewComplete: true,
		}
		blocked, missing := appendMissingDrivesToBlocked([]string{"", "A1"}, op, nil)
		if !slices.Equal(missing, []string{"A1"}) {
			t.Fatalf("expected missing=[A1], got %v", missing)
		}
		if !slices.Equal(blocked, []string{"A1"}) {
			t.Fatalf("expected blocked=[A1], got %v", blocked)
		}
	})

	t.Run("empty inputs", func(t *testing.T) {
		op := &operations.DriveNodeResults{}
		blocked, missing := appendMissingDrivesToBlocked(nil, op, nil)
		if len(missing) != 0 || len(blocked) != 0 {
			t.Fatalf("expected empty outputs, got blocked=%v missing=%v", blocked, missing)
		}
	})
}

func TestPodNotRunningReason(t *testing.T) {
	baseTime := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	t.Run("unschedulable", func(t *testing.T) {
		pod := newTestPod(baseTime, v1.PodPending)
		pod.Status.Conditions = []v1.PodCondition{
			{
				Type:    v1.PodScheduled,
				Status:  v1.ConditionFalse,
				Reason:  "Unschedulable",
				Message: "0/3 nodes are available: insufficient cpu",
			},
		}
		reason, detail := podNotRunningReason(pod)
		if reason != "Unschedulable" {
			t.Fatalf("expected reason Unschedulable, got %s", reason)
		}
		if detail != "0/3 nodes are available: insufficient cpu" {
			t.Fatalf("expected detail from condition message, got %q", detail)
		}
	})

	t.Run("ImagePullBackOff on init container wins over main container", func(t *testing.T) {
		pod := newTestPod(baseTime, v1.PodPending)
		pod.Status.InitContainerStatuses = []v1.ContainerStatus{
			{State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}},
		}
		pod.Status.ContainerStatuses = []v1.ContainerStatus{
			{State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "ContainerCreating"}}},
		}
		reason, _ := podNotRunningReason(pod)
		if reason != "ImagePullBackOff" {
			t.Fatalf("expected init container reason to win, got %s", reason)
		}
	})

	t.Run("ImagePullBackOff on main container with message", func(t *testing.T) {
		pod := newTestPod(baseTime, v1.PodPending)
		pod.Status.ContainerStatuses = []v1.ContainerStatus{
			{State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{
				Reason:  "ImagePullBackOff",
				Message: `Back-off pulling image "foo"`,
			}}},
		}
		reason, detail := podNotRunningReason(pod)
		if reason != "ImagePullBackOff" {
			t.Fatalf("expected reason ImagePullBackOff, got %s", reason)
		}
		if detail != `Back-off pulling image "foo"` {
			t.Fatalf("expected detail from Waiting.Message, got %q", detail)
		}
	})

	t.Run("CrashLoopBackOff", func(t *testing.T) {
		pod := newTestPod(baseTime, v1.PodRunning)
		pod.Status.ContainerStatuses = []v1.ContainerStatus{
			{State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
		}
		reason, _ := podNotRunningReason(pod)
		if reason != "CrashLoopBackOff" {
			t.Fatalf("expected reason CrashLoopBackOff, got %s", reason)
		}
	})

	t.Run("plain Pending with no statuses", func(t *testing.T) {
		pod := newTestPod(baseTime, v1.PodPending)
		reason, detail := podNotRunningReason(pod)
		if reason != "Pending" {
			t.Fatalf("expected reason Pending, got %s", reason)
		}
		if detail != "" {
			t.Fatalf("expected empty detail, got %q", detail)
		}
	})

	t.Run("Failed with pod-level Evicted reason", func(t *testing.T) {
		pod := newTestPod(baseTime, v1.PodFailed)
		pod.Status.Reason = "Evicted"
		pod.Status.Message = "The node was low on resource: memory"
		reason, detail := podNotRunningReason(pod)
		if reason != "Evicted" {
			t.Fatalf("expected reason Evicted, got %s", reason)
		}
		if detail != "The node was low on resource: memory" {
			t.Fatalf("expected detail from pod.Status.Message, got %q", detail)
		}
	})

	t.Run("Failed with container Terminated OOMKilled", func(t *testing.T) {
		pod := newTestPod(baseTime, v1.PodFailed)
		pod.Status.ContainerStatuses = []v1.ContainerStatus{
			{State: v1.ContainerState{Terminated: &v1.ContainerStateTerminated{
				Reason:  "OOMKilled",
				Message: "container was OOM killed",
			}}},
		}
		reason, detail := podNotRunningReason(pod)
		if reason != "OOMKilled" {
			t.Fatalf("expected reason OOMKilled, got %s", reason)
		}
		if detail != "container was OOM killed" {
			t.Fatalf("expected detail from Terminated.Message, got %q", detail)
		}
	})
}

func TestIsStartingUpReason(t *testing.T) {
	startingUp := []string{"Pending", "ContainerCreating", "PodInitializing"}
	for _, reason := range startingUp {
		if !isStartingUpReason(reason) {
			t.Errorf("expected %s to be a starting-up reason", reason)
		}
	}

	notStartingUp := []string{"ImagePullBackOff", "Unschedulable", "Succeeded", "CrashLoopBackOff"}
	for _, reason := range notStartingUp {
		if isStartingUpReason(reason) {
			t.Errorf("expected %s to not be a starting-up reason", reason)
		}
	}
}

func TestPodCrashLoopingAfterFailure(t *testing.T) {
	t.Run("CrashLoopBackOff after a failed run", func(t *testing.T) {
		pod := &v1.Pod{Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{
			{
				State:                v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: v1.ContainerState{Terminated: &v1.ContainerStateTerminated{ExitCode: 1}},
			},
		}}}
		if !podCrashLoopingAfterFailure(pod) {
			t.Fatal("expected crash-looping after a failed run to be true")
		}
	})

	t.Run("CrashLoopBackOff after a successful run is not reaped", func(t *testing.T) {
		// Adhoc-op pods do not set RestartPolicy, so they get the API default Always: a
		// one-off command that succeeds is restarted too and is eventually reported as
		// CrashLoopBackOff as well. Must not be treated as a failure here.
		pod := &v1.Pod{Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{
			{
				State:                v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: v1.ContainerState{Terminated: &v1.ContainerStateTerminated{ExitCode: 0}},
			},
		}}}
		if podCrashLoopingAfterFailure(pod) {
			t.Fatal("expected successful-restart CrashLoopBackOff to be false")
		}
	})

	t.Run("CrashLoopBackOff with no LastTerminationState", func(t *testing.T) {
		pod := &v1.Pod{Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{
			{State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
		}}}
		if podCrashLoopingAfterFailure(pod) {
			t.Fatal("expected no LastTerminationState to be false")
		}
	})

	t.Run("plain Running container", func(t *testing.T) {
		pod := &v1.Pod{Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{
			{State: v1.ContainerState{Running: &v1.ContainerStateRunning{}}},
		}}}
		if podCrashLoopingAfterFailure(pod) {
			t.Fatal("expected a plain running container to be false")
		}
	})
}

func TestPodStuckSince(t *testing.T) {
	creationTime := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	t.Run("never-scheduled Pending pod uses creation time", func(t *testing.T) {
		pod := newTestPod(creationTime, v1.PodPending)
		if got := podStuckSince(pod); !got.Equal(creationTime.Time) {
			t.Fatalf("expected creation time %v, got %v", creationTime.Time, got)
		}
	})

	t.Run("Failed pod uses the ContainersReady transition time", func(t *testing.T) {
		transition := metav1.NewTime(creationTime.Add(time.Hour))
		pod := newTestPod(creationTime, v1.PodFailed)
		pod.Status.Conditions = []v1.PodCondition{
			{Type: v1.ContainersReady, Status: v1.ConditionFalse, LastTransitionTime: transition},
		}
		if got := podStuckSince(pod); !got.Equal(transition.Time) {
			t.Fatalf("expected transition time %v, got %v", transition.Time, got)
		}
	})

	t.Run("Running crash-looping pod does not use the flapping condition", func(t *testing.T) {
		recent := metav1.NewTime(creationTime.Add(time.Minute))
		pod := newTestPod(creationTime, v1.PodRunning)
		pod.Status.Conditions = []v1.PodCondition{
			{Type: v1.ContainersReady, Status: v1.ConditionFalse, LastTransitionTime: recent},
		}
		if got := podStuckSince(pod); !got.Equal(creationTime.Time) {
			t.Fatalf("expected creation time (flapping condition ignored for non-terminal phase), got %v", got)
		}
	})

	t.Run("Failed pod with no ContainersReady condition falls back to creation time", func(t *testing.T) {
		pod := newTestPod(creationTime, v1.PodFailed)
		if got := podStuckSince(pod); !got.Equal(creationTime.Time) {
			t.Fatalf("expected creation time %v, got %v", creationTime.Time, got)
		}
	})
}

func TestPodStuckTimeoutElapsed(t *testing.T) {
	now := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	const timeout = 10 * time.Minute
	const startingTimeout = 30 * time.Minute

	t.Run("ContainerCreating at 15m: within the 30m starting timeout", func(t *testing.T) {
		pod := newTestPod(metav1.NewTime(now.Add(-15*time.Minute)), v1.PodPending)
		pod.Status.ContainerStatuses = []v1.ContainerStatus{
			{State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "ContainerCreating"}}},
		}
		if podStuckTimeoutElapsed(pod, now, timeout, startingTimeout) {
			t.Fatal("expected not elapsed at 15m against the 30m starting timeout")
		}
	})

	t.Run("ContainerCreating at 35m: past the 30m starting timeout", func(t *testing.T) {
		pod := newTestPod(metav1.NewTime(now.Add(-35*time.Minute)), v1.PodPending)
		pod.Status.ContainerStatuses = []v1.ContainerStatus{
			{State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "ContainerCreating"}}},
		}
		if !podStuckTimeoutElapsed(pod, now, timeout, startingTimeout) {
			t.Fatal("expected elapsed at 35m against the 30m starting timeout")
		}
	})

	t.Run("ImagePullBackOff at 15m: past the 10m hard-failure timeout", func(t *testing.T) {
		pod := newTestPod(metav1.NewTime(now.Add(-15*time.Minute)), v1.PodPending)
		pod.Status.ContainerStatuses = []v1.ContainerStatus{
			{State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}},
		}
		if !podStuckTimeoutElapsed(pod, now, timeout, startingTimeout) {
			t.Fatal("expected elapsed at 15m against the 10m hard-failure timeout")
		}
	})

	t.Run("ImagePullBackOff at 5m: within the 10m hard-failure timeout", func(t *testing.T) {
		pod := newTestPod(metav1.NewTime(now.Add(-5*time.Minute)), v1.PodPending)
		pod.Status.ContainerStatuses = []v1.ContainerStatus{
			{State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}},
		}
		if podStuckTimeoutElapsed(pod, now, timeout, startingTimeout) {
			t.Fatal("expected not elapsed at 5m against the 10m hard-failure timeout")
		}
	})
}

// --- updateProxyModeAnnotations ---------------------------------------------------------------
//
// These pin the consequential behaviours of the integrated function that the domain-layer tests
// cannot reach: that the blocked lists are actually threaded into the capacity computation, that
// persisted override rules are re-applied on the merge path, and that a report omitting Model does
// not erase a persisted one and thereby disarm model-based rules.
//
// All three assert through the extended resources rather than the annotation, deliberately. The
// function sets annotations and status on the same in-memory Node, then calls Status().Update
// followed by Update. The fake client copies the stored object back over the passed one on
// Status().Update, which reverts the pending annotation changes so the following Update persists the
// OLD annotations — the status half lands, the annotation half does not. A real API server does not
// behave that way for Nodes: kube's node status strategy resets Spec and Labels from the stored
// object but leaves Annotations alone, so annotation changes made alongside a status update do
// persist (verified live — a baseline shared sign populates weka.io/weka-shared-drives from absent,
// and this function is its only writer). So annotation content is not assertable here, and an
// assertion that expects the annotation to equal its original value would pass whether the write
// landed or not. Do not "fix" the call ordering on the strength of this fake-client artifact.
//
// The capacity resources are a faithful proxy anyway: they are computed from the same post-merge,
// post-override drive list that gets marshalled into the annotation, so a wrong drive list shows up
// as a wrong TLC/QLC split.

func newProxyModeTestLoop(t *testing.T, node *v1.Node) (*containerReconcilerLoop, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add corev1 to scheme: %v", err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add weka to scheme: %v", err)
	}
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "weka-sign-node1", Namespace: "default"},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&corev1.Node{}, &weka.WekaContainer{}).
		WithObjects(node, container).
		Build()
	return &containerReconcilerLoop{Client: c, container: container}, c
}

func mustMarshalJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	return string(b)
}

// capacityResources returns the node's stored TLC and QLC shared-drive capacity, asserting that
// .status.capacity and .status.allocatable agree — they are written together and a caller that
// updated only one would be a bug of the class already fixed once in UnblockSharedDrives.
func capacityResources(t *testing.T, c client.Client, nodeName string) (tlc, qlc int64) {
	t.Helper()
	got := &corev1.Node{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: nodeName}, got); err != nil {
		t.Fatalf("failed to get node: %v", err)
	}
	allocTLC := got.Status.Allocatable[consts.ResourceSharedDrivesCapacity]
	allocQLC := got.Status.Allocatable[consts.ResourcesSharedDrivesCapacityQLC]
	capTLC := got.Status.Capacity[consts.ResourceSharedDrivesCapacity]
	capQLC := got.Status.Capacity[consts.ResourcesSharedDrivesCapacityQLC]
	if allocTLC.Value() != capTLC.Value() || allocQLC.Value() != capQLC.Value() {
		t.Errorf("capacity and allocatable disagree: capacity %d/%d vs allocatable %d/%d",
			capTLC.Value(), capQLC.Value(), allocTLC.Value(), allocQLC.Value())
	}
	return allocTLC.Value(), allocQLC.Value()
}

// TestUpdateProxyModeAnnotations_BlockedDrivesExcludedFromCapacity is the highest-consequence
// assertion in this file. Capacity now excludes blocked drives, to agree with block_drives.go —
// which means a node with blocked drives advertises LESS weka.io/shared-drives-capacity than it did
// before, on upgrade. The exclusion has to be exercised end-to-end: a caller that forgot to thread
// the blocked lists through would still pass every domain-layer test.
func TestUpdateProxyModeAnnotations_BlockedDrivesExcludedFromCapacity(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Annotations: map[string]string{
				consts.AnnotationSharedDrives: mustMarshalJSON(t, []domain.SharedDriveInfo{
					{Serial: "SN1", PhysicalUUID: "u1", CapacityGiB: 100, Type: "TLC", Model: "M1"},
					{Serial: "SN2", PhysicalUUID: "u2", CapacityGiB: 200, Type: "TLC", Model: "M1"},
					{Serial: "SN3", PhysicalUUID: "u3", CapacityGiB: 400, Type: "QLC", Model: "M2"},
				}),
				// SN2 blocked by serial, SN3 by physical UUID — both lists must be honoured.
				consts.AnnotationBlockedDrives:              mustMarshalJSON(t, []string{"SN2"}),
				consts.AnnotationBlockedDrivesPhysicalUuids: mustMarshalJSON(t, []string{"u3"}),
			},
		},
	}
	r, c := newProxyModeTestLoop(t, node)

	opResult := &operations.DriveNodeResults{
		KernelViewComplete: true,
		RawDrives: []operations.DriveRawInfo{
			{SerialId: "SN1"}, {SerialId: "SN2"}, {SerialId: "SN3"},
		},
		ProxyDrives: []domain.SharedDriveInfo{
			{Serial: "SN1", PhysicalUUID: "u1", CapacityGiB: 100, Type: "TLC", Model: "M1"},
		},
	}

	if err := r.updateProxyModeAnnotations(context.Background(), node, opResult); err != nil {
		t.Fatalf("updateProxyModeAnnotations returned error: %v", err)
	}

	tlc, qlc := capacityResources(t, c, "node1")
	if tlc != 100 {
		t.Errorf("TLC capacity = %d, want 100 (SN2's 200 GiB is serial-blocked and must be excluded)", tlc)
	}
	if qlc != 0 {
		t.Errorf("QLC capacity = %d, want 0 (SN3's 400 GiB is physical-UUID-blocked and must be excluded)", qlc)
	}
}

// TestUpdateProxyModeAnnotations_ReAppliesPersistedOverrides covers the path that makes overrides
// survive later sign-drives runs: the rules live only in the node annotation, and this function must
// re-apply them to the whole merged drive set — including drives this run newly discovered — before
// computing capacities. Without it a re-sign would publish IU-derived types and silently undo an
// operator's override.
func TestUpdateProxyModeAnnotations_ReAppliesPersistedOverrides(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Annotations: map[string]string{
				consts.AnnotationSharedDrives: mustMarshalJSON(t, []domain.SharedDriveInfo{
					{Serial: "SN1", PhysicalUUID: "u1", CapacityGiB: 100, Type: "TLC", Model: "M1"},
				}),
				consts.AnnotationDriveTypeOverrides: mustMarshalJSON(t, []weka.DriveTypeOverrideRule{
					{Model: "M1", Type: "QLC"},
				}),
			},
		},
	}
	r, c := newProxyModeTestLoop(t, node)

	// The agent re-reports SN1 with its IU-derived TLC, plus a newly discovered SN2 of the same model.
	opResult := &operations.DriveNodeResults{
		KernelViewComplete: true,
		RawDrives:          []operations.DriveRawInfo{{SerialId: "SN1"}, {SerialId: "SN2"}},
		ProxyDrives: []domain.SharedDriveInfo{
			{Serial: "SN1", PhysicalUUID: "u1", CapacityGiB: 100, Type: "TLC", Model: "M1"},
			{Serial: "SN2", PhysicalUUID: "u2", CapacityGiB: 300, Type: "TLC", Model: "M1"},
		},
	}

	if err := r.updateProxyModeAnnotations(context.Background(), node, opResult); err != nil {
		t.Fatalf("updateProxyModeAnnotations returned error: %v", err)
	}

	// 0/400 proves both the pre-existing and the newly discovered drive were overridden to QLC.
	// 400/0 would mean the persisted rule was ignored and the reported IU-derived types won.
	tlc, qlc := capacityResources(t, c, "node1")
	if tlc != 0 || qlc != 400 {
		t.Errorf("capacity = %d TLC / %d QLC, want 0/400 (the persisted rule must be re-applied to the whole merged set, newly discovered drives included)", tlc, qlc)
	}
}

// TestUpdateProxyModeAnnotations_MergePreservesModel covers the field-wise merge. An agent that
// reports a drive without its Model must not erase the persisted one, because model-based overrides
// match on exactly that field — losing it silently disarms them, which is the failure this asserts
// against: if the Model is erased, the rule below stops matching and the drive stays TLC.
func TestUpdateProxyModeAnnotations_MergePreservesModel(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Annotations: map[string]string{
				consts.AnnotationSharedDrives: mustMarshalJSON(t, []domain.SharedDriveInfo{
					{Serial: "SN1", PhysicalUUID: "u1", CapacityGiB: 100, Type: "TLC", Model: "Micron_7450"},
				}),
				consts.AnnotationDriveTypeOverrides: mustMarshalJSON(t, []weka.DriveTypeOverrideRule{
					{Model: "Micron_7450", Type: "QLC"},
				}),
			},
		},
	}
	r, c := newProxyModeTestLoop(t, node)

	opResult := &operations.DriveNodeResults{
		KernelViewComplete: true,
		RawDrives:          []operations.DriveRawInfo{{SerialId: "SN1"}},
		// Model omitted, as an older node-agent or a failed sysfs lookup would report it.
		ProxyDrives: []domain.SharedDriveInfo{
			{Serial: "SN1", PhysicalUUID: "u1", CapacityGiB: 100, Type: "TLC"},
		},
	}

	if err := r.updateProxyModeAnnotations(context.Background(), node, opResult); err != nil {
		t.Fatalf("updateProxyModeAnnotations returned error: %v", err)
	}

	tlc, qlc := capacityResources(t, c, "node1")
	if tlc != 0 || qlc != 100 {
		t.Errorf("capacity = %d TLC / %d QLC, want 0/100: the model rule must still match, which it only can if the merge preserved the persisted Model against an empty incoming one", tlc, qlc)
	}
}
