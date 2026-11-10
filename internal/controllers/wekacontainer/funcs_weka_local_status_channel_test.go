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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services"
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
		Spec:       weka.WekaContainerSpec{WekaContainerName: "client"},
		Status:     weka.WekaContainerStatus{Status: status},
	}
	t.Cleanup(func() { channelFailures.clear(container.UID) })
	recorder := events.NewFakeRecorder(10)
	return &containerReconcilerLoop{
		Client:        fake.NewClientBuilder().WithScheme(scheme).WithObjects(container).WithStatusSubresource(container).Build(),
		ExecService:   fakePsExecService{ps},
		Recorder:      recorder,
		ThrottlingMap: throttling.NewSyncMapThrottler(),
		container:     container,
	}, recorder
}

func TestLocalStatusChannelErrorWaitsWithoutSideEffects(t *testing.T) {
	ps := &fakePsExec{err: &podexec.ChannelError{Err: errors.New("stream failed")}}
	r, recorder := newLocalStatusLoop(t, weka.Running, ps)

	err := r.reconcileWekaLocalStatus(context.Background())

	var waitErr *lifecycle.WaitError
	if !errors.As(err, &waitErr) {
		t.Fatalf("got %v, want a wait error", err)
	}
	if ps.calls != 1 {
		t.Fatalf("exec calls = %d, want 1 (no drivers check)", ps.calls)
	}
	if r.container.Status.Status != weka.Running {
		t.Fatalf("status = %s, want Running", r.container.Status.Status)
	}
	if len(recorder.Events) != 0 {
		t.Fatalf("unexpected events: %d", len(recorder.Events))
	}
}

func TestLocalStatusUnknownAfterTimeoutOnceAndClearedBySuccess(t *testing.T) {
	ps := &fakePsExec{err: &podexec.ChannelError{Err: errors.New("stream failed")}}
	r, recorder := newLocalStatusLoop(t, weka.Running, ps)
	ctx := context.Background()

	_ = r.reconcileWekaLocalStatus(ctx)
	channelFailures.streaks.Store(r.container.UID, failureStreak{first: time.Now().Add(-10 * time.Minute), last: time.Now()})

	for i := 0; i < 2; i++ {
		_ = r.reconcileWekaLocalStatus(ctx)
	}
	if r.container.Status.Status != runStatusUnknown {
		t.Fatalf("status = %s, want Unknown", r.container.Status.Status)
	}
	if len(recorder.Events) != 1 {
		t.Fatalf("events = %d, want exactly 1", len(recorder.Events))
	}

	ps.err = nil
	ps.stdout = `[{"name":"client","runStatus":"Running","isRunning":true,"internalStatus":{"display_status":"READY"}}]`
	if err := r.reconcileWekaLocalStatus(ctx); err != nil {
		t.Fatal(err)
	}
	if r.container.Status.Status != weka.Running {
		t.Fatalf("status after recovery = %s, want Running", r.container.Status.Status)
	}
	if _, ok := channelFailures.streaks.Load(r.container.UID); ok {
		t.Fatal("failure streak not cleared by a successful read")
	}
}

func TestSetErrorStatusIgnoresChannelCause(t *testing.T) {
	r, recorder := newLocalStatusLoop(t, weka.Running, &fakePsExec{})
	channel := &podexec.ChannelError{Err: errors.New("stream failed")}

	if err := r.setErrorStatus(context.Background(), "step", lifecycle.NewWaitError(channel)); err != nil {
		t.Fatal(err)
	}
	if r.container.Status.Status != weka.Running || len(recorder.Events) != 0 {
		t.Fatalf("channel error changed status to %s or emitted events", r.container.Status.Status)
	}

	if err := r.setErrorStatus(context.Background(), "step", apierrors.NewNotFound(schema.GroupResource{}, "x")); err != nil {
		t.Fatal(err)
	}
	if r.container.Status.Status != weka.Error {
		t.Fatalf("status = %s, want Error for a non-channel failure", r.container.Status.Status)
	}
}

func TestChannelFailureStreakRestartsAfterGap(t *testing.T) {
	tr := &failureTracker{}
	uid := types.UID("u")
	now := time.Now()
	tr.streaks.Store(uid, failureStreak{first: now.Add(-time.Hour), last: now.Add(-3 * time.Minute)})
	if got := tr.failure(uid, now); !got.Equal(now) {
		t.Fatalf("stale streak not restarted: since = %v", got)
	}
	if got := tr.failure(uid, now.Add(time.Minute)); !got.Equal(now) {
		t.Fatalf("recent streak restarted: since = %v", got)
	}
}
