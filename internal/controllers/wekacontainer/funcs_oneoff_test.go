package wekacontainer

import (
	"slices"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/controllers/operations"
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
