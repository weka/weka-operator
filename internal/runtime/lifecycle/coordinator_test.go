package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/process"
)

type fakeRuntime struct {
	startFn    func(ctx context.Context) error
	shutdownFn func(ctx context.Context) error
}

func (f *fakeRuntime) Start(ctx context.Context) error { return f.startFn(ctx) }

func (f *fakeRuntime) Shutdown(ctx context.Context) error {
	if f.shutdownFn == nil {
		return nil
	}
	return f.shutdownFn(ctx)
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func newTestCoordinator(ctx context.Context) *Coordinator {
	return New(ctx, process.NewManager(context.Background()))
}

func TestOutcomeExitCode(t *testing.T) {
	boom := errors.New("boom")
	wrappedCanceled := fmt.Errorf("start: %w", context.Canceled)

	cases := []struct {
		name string
		o    Outcome
		want int
	}{
		{"no error", Outcome{}, 0},
		{"error without request", Outcome{StartupErr: boom}, 1},
		{"genuine error despite request", Outcome{StartupErr: boom, Requested: true}, 1},
		{"cancellation with request", Outcome{StartupErr: context.Canceled, Requested: true}, 0},
		{"wrapped cancellation with request", Outcome{StartupErr: wrappedCanceled, Requested: true}, 0},
		{"cancellation without request", Outcome{StartupErr: context.Canceled, Requested: false}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.o.ExitCode(); got != tc.want {
				t.Fatalf("ExitCode() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRequestShutdownIdempotentAndTakeoverSticky(t *testing.T) {
	coord := newTestCoordinator(context.Background())

	select {
	case <-coord.TakeoverDone():
		t.Fatalf("TakeoverDone() closed before any takeover request")
	default:
	}

	coord.RequestShutdown(ReasonSignal)
	coord.RequestShutdown(ReasonSignal)   // idempotent repeat
	coord.RequestShutdown(ReasonTakeover) // sticky even though a signal was recorded first

	select {
	case <-coord.TakeoverDone():
	default:
		t.Fatalf("TakeoverDone() not closed after a takeover request")
	}

	rt := &fakeRuntime{startFn: func(ctx context.Context) error { return ctx.Err() }}
	outcome := coord.Run(context.Background(), rt)

	if !outcome.Requested {
		t.Fatalf("Requested = false, want true")
	}
	if !outcome.Takeover {
		t.Fatalf("Takeover = false, want true")
	}
}

// TestTakeoverDoneStaysOpenAfterPlainSignal confirms a signal-only request never closes the
// takeover channel.
func TestTakeoverDoneStaysOpenAfterPlainSignal(t *testing.T) {
	coord := newTestCoordinator(context.Background())

	coord.RequestShutdown(ReasonSignal)

	select {
	case <-coord.TakeoverDone():
		t.Fatalf("TakeoverDone() closed after a plain signal request")
	default:
	}
}

// TestSignalDuringStartIsRecorded proves signals are captured before rt.Start is called: a real
// SIGTERM, sent only once Start is confirmed running, must still be recorded rather than killing
// the process with the default action or being missed.
func TestSignalDuringStartIsRecorded(t *testing.T) {
	coord := newTestCoordinator(context.Background())

	startedBarrier := make(chan struct{})
	rt := &fakeRuntime{
		startFn: func(ctx context.Context) error {
			close(startedBarrier)
			<-ctx.Done()
			return ctx.Err()
		},
	}

	go func() {
		<-startedBarrier
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Errorf("Kill(SIGTERM) error = %v", err)
		}
	}()

	outcome := coord.Run(context.Background(), rt)

	if !outcome.Requested {
		t.Fatalf("Requested = false, want true (signal delivered while Start was running)")
	}
	if !errors.Is(outcome.StartupErr, context.Canceled) {
		t.Fatalf("StartupErr = %v, want context.Canceled", outcome.StartupErr)
	}
	if outcome.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d, want 0", outcome.ExitCode())
	}
}

func TestStartupJoinedBeforeShutdown(t *testing.T) {
	coord := newTestCoordinator(context.Background())

	var order []string
	startDone := make(chan struct{})
	rt := &fakeRuntime{
		startFn: func(ctx context.Context) error {
			order = append(order, "start")
			close(startDone)
			return nil
		},
		shutdownFn: func(ctx context.Context) error {
			order = append(order, "shutdown")
			return nil
		},
	}

	go func() {
		<-startDone
		coord.RequestShutdown(ReasonSignal)
	}()

	outcome := coord.Run(context.Background(), rt)

	if !outcome.Requested {
		t.Fatalf("Requested = false, want true")
	}
	if want := []string{"start", "shutdown"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if outcome.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d, want 0", outcome.ExitCode())
	}
}

func TestGenuineStartupErrorWithoutRequestSkipsShutdown(t *testing.T) {
	coord := newTestCoordinator(context.Background())

	wantErr := errors.New("boom")
	shutdownCalled := false
	rt := &fakeRuntime{
		startFn:    func(ctx context.Context) error { return wantErr },
		shutdownFn: func(ctx context.Context) error { shutdownCalled = true; return nil },
	}

	outcome := coord.Run(context.Background(), rt)

	if outcome.Requested {
		t.Fatalf("Requested = true, want false")
	}
	if !errors.Is(outcome.StartupErr, wantErr) {
		t.Fatalf("StartupErr = %v, want %v", outcome.StartupErr, wantErr)
	}
	if outcome.ExitCode() != 1 {
		t.Fatalf("ExitCode() = %d, want 1", outcome.ExitCode())
	}
	if shutdownCalled {
		t.Fatalf("Shutdown was called, want not called")
	}
}

func TestRecordedRequestWinsOverStartupError(t *testing.T) {
	coord := newTestCoordinator(context.Background())

	ready := make(chan struct{})
	rt := &fakeRuntime{
		startFn: func(ctx context.Context) error {
			close(ready)
			<-ctx.Done()
			return ctx.Err()
		},
	}

	go func() {
		<-ready
		coord.RequestShutdown(ReasonSignal)
	}()

	outcome := coord.Run(context.Background(), rt)

	if !outcome.Requested {
		t.Fatalf("Requested = false, want true")
	}
	if !errors.Is(outcome.StartupErr, context.Canceled) {
		t.Fatalf("StartupErr = %v, want context.Canceled", outcome.StartupErr)
	}
	if outcome.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d, want 0", outcome.ExitCode())
	}
}

func TestLateRequestShutdownAfterFailureHasNoEffect(t *testing.T) {
	coord := newTestCoordinator(context.Background())

	shutdownCalled := false
	rt := &fakeRuntime{
		startFn:    func(ctx context.Context) error { return errors.New("boom") },
		shutdownFn: func(ctx context.Context) error { shutdownCalled = true; return nil },
	}

	outcome := coord.Run(context.Background(), rt)

	coord.RequestShutdown(ReasonSignal) // arrives after Run already committed to the failure path

	if shutdownCalled {
		t.Fatalf("Shutdown was called by a late request")
	}
	if outcome.Requested {
		t.Fatalf("Requested = true, want false")
	}
}

func TestFatalTaskErrorDuringRunningTriggersFailureCleanup(t *testing.T) {
	coord := newTestCoordinator(context.Background())

	taskErr := errors.New("task boom")
	shutdownCalled := false
	rt := &fakeRuntime{
		startFn: func(ctx context.Context) error {
			coord.Go("worker", func(ctx context.Context) error { return taskErr })
			return nil
		},
		shutdownFn: func(ctx context.Context) error { shutdownCalled = true; return nil },
	}

	outcome := coord.Run(context.Background(), rt)

	if outcome.Requested {
		t.Fatalf("Requested = true, want false")
	}
	if !errors.Is(outcome.StartupErr, taskErr) {
		t.Fatalf("StartupErr = %v, want to wrap %v", outcome.StartupErr, taskErr)
	}
	if outcome.ExitCode() != 1 {
		t.Fatalf("ExitCode() = %d, want 1", outcome.ExitCode())
	}
	if shutdownCalled {
		t.Fatalf("Shutdown was called, want not called on a fatal task error")
	}
	for _, err := range outcome.CleanupErrs {
		if errors.Is(err, taskErr) {
			t.Fatalf("CleanupErrs = %v, want the fatal task error not duplicated there", outcome.CleanupErrs)
		}
	}
}

func TestCleanupOrderAndContinuesAfterFailingCloser(t *testing.T) {
	coord := newTestCoordinator(context.Background())

	var order []string
	boom := errors.New("close boom")

	mk := func(name string, err error) closerFunc {
		return closerFunc(func() error {
			order = append(order, name)
			return err
		})
	}

	if err := coord.Register("first", mk("first", nil)); err != nil {
		t.Fatalf("Register(first) error = %v", err)
	}
	if err := coord.Register("second", mk("second", boom)); err != nil {
		t.Fatalf("Register(second) error = %v", err)
	}
	if err := coord.Register("third", mk("third", nil)); err != nil {
		t.Fatalf("Register(third) error = %v", err)
	}
	if err := coord.RegisterLast("gen-lock", mk("gen-lock", nil)); err != nil {
		t.Fatalf("RegisterLast error = %v", err)
	}

	rt := &fakeRuntime{startFn: func(ctx context.Context) error { return nil }}
	go coord.RequestShutdown(ReasonSignal)
	outcome := coord.Run(context.Background(), rt)

	want := []string{"third", "second", "first", "gen-lock"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("close order = %v, want %v", order, want)
	}
	if len(outcome.CleanupErrs) != 1 || !errors.Is(outcome.CleanupErrs[0], boom) {
		t.Fatalf("CleanupErrs = %v, want [%v]", outcome.CleanupErrs, boom)
	}
}

func TestRegisterAfterCleanupBeganReturnsError(t *testing.T) {
	coord := newTestCoordinator(context.Background())

	rt := &fakeRuntime{startFn: func(ctx context.Context) error { return nil }}
	go coord.RequestShutdown(ReasonSignal)
	coord.Run(context.Background(), rt)

	noop := closerFunc(func() error { return nil })
	if err := coord.Register("late", noop); err == nil {
		t.Fatalf("Register after cleanup began = nil error, want error")
	}
	if err := coord.RegisterLast("late-gen", noop); err == nil {
		t.Fatalf("RegisterLast after cleanup began = nil error, want error")
	}
}

// advanceUntil repeatedly advances fc by step, giving the background goroutine time to
// register its timer against the new deadline, until calls reaches want or the retry
// budget is exhausted.
func advanceUntil(fc *clock.Fake, step time.Duration, calls *int32, want int32) bool {
	for i := 0; i < 100; i++ {
		fc.Advance(step)
		if atomic.LoadInt32(calls) >= want {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return atomic.LoadInt32(calls) >= want
}

func TestGoPeriodicRunsAfterDelayAndInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coord := newTestCoordinator(ctx)

	fc := clock.NewFake(time.Now())
	var calls int32
	coord.GoPeriodic("periodic", fc, 10*time.Second, 5*time.Second, func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("boom") // a work error must stay non-fatal
	})

	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("calls = %d before delay elapsed, want 0", calls)
	}
	if !advanceUntil(fc, 10*time.Second, &calls, 1) {
		t.Fatalf("work did not run after initial delay, calls = %d", calls)
	}
	if !advanceUntil(fc, 5*time.Second, &calls, 2) {
		t.Fatalf("work did not run again after interval, calls = %d", calls)
	}

	select {
	case <-coord.fatalCh:
		t.Fatalf("periodic work error reported as fatal: %v", coord.fatalErr)
	default:
	}

	cancel()
	coord.taskWG.Wait()
}

func TestGoPeriodicStopsOnShutdownRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coord := newTestCoordinator(ctx)

	fc := clock.NewFake(time.Now())
	var calls int32
	// Interval far above what advanceUntil's repeated steps can reach, so only a shutdown
	// request can end the sleep.
	coord.GoPeriodic("periodic", fc, 10*time.Second, time.Hour, func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})
	if !advanceUntil(fc, 10*time.Second, &calls, 1) {
		t.Fatalf("work did not run after initial delay, calls = %d", calls)
	}

	coord.RequestShutdown(ReasonSignal)

	done := make(chan struct{})
	go func() { coord.taskWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown request did not wake the periodic sleep")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d after shutdown request, want 1", got)
	}
	if coord.tasksCtx.Err() != nil {
		t.Fatal("shutdown request cancelled the tasks context; only periodic sleeps should stop")
	}
}
