package process

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/clock"
)

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	m := NewManager(context.Background())
	res, err := m.Run(context.Background(), Command{Path: "sh", Args: []string{"-c", "echo out; echo err >&2"}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if string(res.Stdout) != "out\n" {
		t.Fatalf("Stdout = %q, want %q", res.Stdout, "out\n")
	}
	if string(res.Stderr) != "err\n" {
		t.Fatalf("Stderr = %q, want %q", res.Stderr, "err\n")
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
}

func TestRunReturnsExecErrorOnNonZeroExit(t *testing.T) {
	m := NewManager(context.Background())
	_, err := m.Run(context.Background(), Command{Path: "sh", Args: []string{"-c", "exit 7"}})
	var execErr *ExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("err = %v, want *ExecError", err)
	}
	if execErr.Kind != FailureExit {
		t.Fatalf("Kind = %v, want FailureExit", execErr.Kind)
	}
	if execErr.Result.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", execErr.Result.ExitCode)
	}
}

func TestRunCancellationTerminatesAndReportsCancelled(t *testing.T) {
	m := NewManager(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := m.Run(ctx, Command{Path: "sleep", Args: []string{"30"}})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		var execErr *ExecError
		if !errors.As(err, &execErr) {
			t.Fatalf("err = %v, want *ExecError", err)
		}
		if execErr.Kind != FailureCancelled {
			t.Fatalf("Kind = %v, want FailureCancelled", execErr.Kind)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("errors.Is(err, context.Canceled) = false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation and SIGTERM")
	}
}

func TestStartProcessHandleCompletesCleanly(t *testing.T) {
	m := NewManager(context.Background())
	h, err := m.StartProcess(context.Background(), "sleeper", Command{Path: "sh", Args: []string{"-c", "exit 0"}})
	if err != nil {
		t.Fatalf("StartProcess() error = %v", err)
	}
	select {
	case <-h.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("process did not finish")
	}
	if err := h.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
}

func TestStartProcessDoesNotRestart(t *testing.T) {
	m := NewManager(context.Background())
	h, err := m.StartProcess(context.Background(), "onceonly", Command{Path: "sh", Args: []string{"-c", "exit 1"}})
	if err != nil {
		t.Fatalf("StartProcess() error = %v", err)
	}
	select {
	case <-h.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("process did not finish")
	}
	if err := h.Err(); err == nil {
		t.Fatal("Err() = nil, want the exit error")
	}
}

func TestStartProcessMarksEntryFinished(t *testing.T) {
	m := NewManager(context.Background())
	h, err := m.StartProcess(context.Background(), "onceonly", Command{Path: "sh", Args: []string{"-c", "exit 0"}})
	if err != nil {
		t.Fatalf("StartProcess() error = %v", err)
	}
	select {
	case <-h.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("process did not finish")
	}

	// The entry stays registered (for stable reverse-order Shutdown) but must be marked
	// finished so the reaper doesn't let its now-stale pid shadow a real orphan.
	m.mu.Lock()
	finished := h.e.finished
	m.mu.Unlock()
	if !finished {
		t.Fatal("entry.finished = false after process exit, want true")
	}
}

func TestStartDaemonRestartsOnFixedCadence(t *testing.T) {
	fc := clock.NewFake(time.Unix(0, 0))
	m := NewManagerWithClock(context.Background(), fc)

	h, err := m.StartDaemon(context.Background(), "flaky", Command{Path: "sh", Args: []string{"-c", "exit 1"}})
	if err != nil {
		t.Fatalf("StartDaemon() error = %v", err)
	}

	waitForPID := func(prev int) int {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			m.mu.Lock()
			pid := h.e.pid
			m.mu.Unlock()
			if pid != prev {
				return pid
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for restart past pid %d", prev)
		return 0
	}

	m.mu.Lock()
	firstPID := h.e.pid
	m.mu.Unlock()

	// Give the first exit+Wait time to land, then advance the fake clock past the
	// fixed 3s restart cadence and confirm a relaunch happens.
	time.Sleep(200 * time.Millisecond)
	fc.Advance(3 * time.Second)
	secondPID := waitForPID(firstPID)
	if secondPID == firstPID {
		t.Fatal("daemon did not restart")
	}

	m.DisableRestarts()
	fc.Advance(3 * time.Second)
	select {
	case <-h.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon supervisor did not stop after DisableRestarts")
	}
}

func TestShutdownTerminatesAllAndWaits(t *testing.T) {
	m := NewManager(context.Background())
	h1, err := m.StartProcess(context.Background(), "a", Command{Path: "sleep", Args: []string{"30"}})
	if err != nil {
		t.Fatalf("StartProcess(a) error = %v", err)
	}
	h2, err := m.StartProcess(context.Background(), "b", Command{Path: "sleep", Args: []string{"30"}})
	if err != nil {
		t.Fatalf("StartProcess(b) error = %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- m.Shutdown(context.Background()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return")
	}

	for _, h := range []*Handle{h1, h2} {
		select {
		case <-h.Done:
		default:
			t.Fatalf("%s: Done not closed after Shutdown", h.Name)
		}
	}
}
