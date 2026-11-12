package wekacontainer

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/pkg/util/channelerr"
	"github.com/weka/weka-operator/pkg/util/podexec"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

type fakePsExec struct {
	calls  int
	stdout string
	err    error
}

func (f *fakePsExec) ExecNamed(_ context.Context, _ string, _ []string) (bytes.Buffer, bytes.Buffer, error) {
	f.calls++
	return *bytes.NewBufferString(f.stdout), bytes.Buffer{}, f.err
}

func (f *fakePsExec) Exec(ctx context.Context, cmd []string) (bytes.Buffer, bytes.Buffer, error) {
	return f.ExecNamed(ctx, "", cmd)
}

func (f *fakePsExec) ExecSensitive(ctx context.Context, name string, cmd []string) (bytes.Buffer, bytes.Buffer, error) {
	return f.ExecNamed(ctx, name, cmd)
}

func (f *fakePsExec) ExecSensitiveWithStdin(ctx context.Context, name string, cmd []string, _ string) (bytes.Buffer, bytes.Buffer, error) {
	return f.ExecNamed(ctx, name, cmd)
}

type fakePsExecService struct{ exec *fakePsExec }

func (f fakePsExecService) GetExecutor(context.Context, *weka.WekaContainer) (podexec.Exec, error) {
	return f.exec, nil
}

func (f fakePsExecService) GetExecutorWithTimeout(context.Context, *weka.WekaContainer, *time.Duration) (podexec.Exec, error) {
	return f.exec, nil
}

func podWithWekaContainer(state v1.ContainerState) *v1.Pod {
	return &v1.Pod{Status: v1.PodStatus{
		Phase:             v1.PodRunning,
		ContainerStatuses: []v1.ContainerStatus{{Name: consts.WekaContainerName, State: state}},
	}}
}

var runningState = v1.ContainerState{Running: &v1.ContainerStateRunning{}}

func newLocalStatusLoop(t *testing.T, status weka.ContainerStatus, ps *fakePsExec) (*containerReconcilerLoop, *events.FakeRecorder) {
	t.Helper()
	config.Config.OperatorPodNamespace = "ns"
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := services.RefreshSettings(context.Background(), fake.NewClientBuilder().WithScheme(scheme).Build()); err != nil {
		t.Fatal(err)
	}
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns", UID: types.UID(t.Name())},
		Spec:       weka.WekaContainerSpec{WekaContainerName: "client", Mode: weka.WekaContainerModeClient},
		Status:     weka.WekaContainerStatus{Status: status},
	}
	t.Cleanup(func() { lastLocalPsRead.Delete(container.UID) })
	recorder := events.NewFakeRecorder(10)
	return &containerReconcilerLoop{
		Client:        fake.NewClientBuilder().WithScheme(scheme).WithObjects(container).WithStatusSubresource(container).Build(),
		ExecService:   fakePsExecService{ps},
		Recorder:      recorder,
		ThrottlingMap: throttling.NewSyncMapThrottler(),
		container:     container,
		pod:           podWithWekaContainer(runningState),
	}, recorder
}

func lastRead(uid types.UID) (time.Time, bool) {
	v, ok := lastLocalPsRead.Load(uid)
	if !ok {
		return time.Time{}, false
	}
	return v.(time.Time), true
}

func TestIsChannelFailureRequiresRunningWekaContainer(t *testing.T) {
	channel := lifecycle.NewWaitError(channelerr.Wrap(errors.New("unable to upgrade connection: container not found")))
	for name, tt := range map[string]struct {
		pod  *v1.Pod
		want bool
	}{
		"running":       {podWithWekaContainer(runningState), true},
		"no pod":        {nil, true},
		"crash loop":    {podWithWekaContainer(v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}), false},
		"terminated":    {podWithWekaContainer(v1.ContainerState{Terminated: &v1.ContainerStateTerminated{ExitCode: 1}}), false},
		"no status yet": {&v1.Pod{Status: v1.PodStatus{Phase: v1.PodRunning}}, false},
	} {
		r := &containerReconcilerLoop{pod: tt.pod}
		if got := r.isChannelFailure(channel); got != tt.want {
			t.Errorf("%s: isChannelFailure = %v, want %v", name, got, tt.want)
		}
	}
	r := &containerReconcilerLoop{pod: podWithWekaContainer(runningState)}
	if r.isChannelFailure(lifecycle.NewWaitError(errors.New("plain"))) {
		t.Error("plain error counted as a channel failure")
	}
}

func TestLocalStatusChannelErrorWaitsWithoutSideEffects(t *testing.T) {
	ps := &fakePsExec{err: channelerr.Wrap(errors.New("stream failed"))}
	r, recorder := newLocalStatusLoop(t, weka.Running, ps)

	err := r.reconcileWekaLocalStatus(context.Background())

	var waitErr *lifecycle.WaitError
	if !errors.As(err, &waitErr) {
		t.Fatalf("got %v, want a wait error", err)
	}
	if ps.calls != 1 {
		t.Fatalf("exec calls = %d, want 1 (no drivers check)", ps.calls)
	}
	if statusErr := r.setErrorStatus(context.Background(), "ReconcileWekaLocalStatus", err); statusErr != nil {
		t.Fatal(statusErr)
	}
	if r.container.Status.Status != weka.Running {
		t.Fatalf("status = %s, want Running", r.container.Status.Status)
	}
	if len(recorder.Events) != 0 {
		t.Fatalf("unexpected events: %d", len(recorder.Events))
	}
	if _, ok := lastRead(r.container.UID); !ok {
		t.Fatal("first failure did not seed the last-read clock")
	}
}

func TestChannelFailureInAnyStepTurnsUnknownOnceAfterTimeout(t *testing.T) {
	r, recorder := newLocalStatusLoop(t, weka.Running, &fakePsExec{})
	ctx := context.Background()
	stepErr := lifecycle.NewWaitError(channelerr.Wrap(errors.New("unable to upgrade connection: Internal error")))

	lastLocalPsRead.Store(r.container.UID, time.Now().Add(-4*time.Minute))
	if err := r.setErrorStatus(ctx, "ensurePod", stepErr); err != nil {
		t.Fatal(err)
	}
	if r.container.Status.Status != weka.Running {
		t.Fatalf("status before timeout = %s, want Running", r.container.Status.Status)
	}

	lastLocalPsRead.Store(r.container.UID, time.Now().Add(-10*time.Minute))
	for _, step := range []string{"ensurePod", "ReconcileManagementIPs"} {
		if err := r.setErrorStatus(ctx, step, stepErr); err != nil {
			t.Fatal(err)
		}
	}
	if r.container.Status.Status != runStatusUnknown {
		t.Fatalf("status = %s, want Unknown", r.container.Status.Status)
	}
	if len(recorder.Events) != 1 {
		t.Fatalf("events = %d, want exactly 1", len(recorder.Events))
	}
}

func TestSuccessfulLocalPsRefreshesClock(t *testing.T) {
	ps := &fakePsExec{stdout: `[{"name":"client","runStatus":"Running","isRunning":true,"internalStatus":{"display_status":"READY"}}]`}
	r, _ := newLocalStatusLoop(t, runStatusUnknown, ps)
	old := time.Now().Add(-10 * time.Minute)
	lastLocalPsRead.Store(r.container.UID, old)

	if err := r.reconcileWekaLocalStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.container.Status.Status != weka.Running {
		t.Fatalf("status after recovery = %s, want Running", r.container.Status.Status)
	}
	if got, _ := lastRead(r.container.UID); !got.After(old) {
		t.Fatal("successful read did not refresh the clock")
	}

	stepErr := lifecycle.NewWaitError(channelerr.Wrap(errors.New("unable to upgrade connection: Internal error")))
	if err := r.setErrorStatus(context.Background(), "ensurePod", stepErr); err != nil {
		t.Fatal(err)
	}
	if r.container.Status.Status != weka.Running {
		t.Fatalf("status after a fresh read and one failure = %s, want Running", r.container.Status.Status)
	}
}

func TestStaleReadAndOneChannelFailureTurnsUnknownImmediately(t *testing.T) {
	r, recorder := newLocalStatusLoop(t, weka.Running, &fakePsExec{})
	lastLocalPsRead.Store(r.container.UID, time.Now().Add(-time.Hour))

	stepErr := lifecycle.NewWaitError(channelerr.Wrap(errors.New("unable to upgrade connection: Internal error")))
	if err := r.setErrorStatus(context.Background(), "ensurePod", stepErr); err != nil {
		t.Fatal(err)
	}
	if r.container.Status.Status != runStatusUnknown {
		t.Fatalf("status = %s, want Unknown", r.container.Status.Status)
	}
	if len(recorder.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(recorder.Events))
	}
}

func TestChannelFailureOnNonWekaContainerModeIsIgnored(t *testing.T) {
	r, recorder := newLocalStatusLoop(t, weka.Running, &fakePsExec{})
	r.container.Spec.Mode = weka.WekaContainerModeAdhocOp
	lastLocalPsRead.Store(r.container.UID, time.Now().Add(-time.Hour))

	stepErr := lifecycle.NewWaitError(channelerr.Wrap(errors.New("unable to upgrade connection: Internal error")))
	if err := r.setErrorStatus(context.Background(), "ensurePod", stepErr); err != nil {
		t.Fatal(err)
	}
	if r.container.Status.Status != weka.Running {
		t.Fatalf("status = %s, want Running", r.container.Status.Status)
	}
	if len(recorder.Events) != 0 {
		t.Fatalf("events = %d, want 0", len(recorder.Events))
	}
}

func TestContainerSideFailureTakesErrorPathAndLeavesClock(t *testing.T) {
	r, recorder := newLocalStatusLoop(t, weka.Running, &fakePsExec{})
	r.pod = podWithWekaContainer(v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}})
	old := time.Now().Add(-10 * time.Minute)
	lastLocalPsRead.Store(r.container.UID, old)

	stepErr := lifecycle.NewWaitError(channelerr.Wrap(errors.New("unable to upgrade connection: container not found")))
	if err := r.setErrorStatus(context.Background(), "ReconcileWekaLocalStatus", stepErr); err != nil {
		t.Fatal(err)
	}
	if r.container.Status.Status != weka.Error {
		t.Fatalf("status = %s, want Error", r.container.Status.Status)
	}
	if len(recorder.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(recorder.Events))
	}
	if got, ok := lastRead(r.container.UID); !ok || !got.Equal(old) {
		t.Fatal("container-side failure touched the last-read clock")
	}
}
