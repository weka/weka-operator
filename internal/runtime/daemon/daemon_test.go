package daemon_test

import (
	"context"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/daemon"
)

func TestSupervisor_Empty(t *testing.T) {
	ctx := context.Background()
	sv := daemon.NewSupervisor()
	if err := sv.Run(ctx); err != nil {
		t.Errorf("empty supervisor returned error: %v", err)
	}
}

func TestSupervisor_StopsOnContextCancel(t *testing.T) {
	sv := daemon.NewSupervisor()
	sv.Add("long-sleep", func() *exec.Cmd {
		return exec.Command("sleep", "60")
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sv.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("supervisor did not stop within 2s after context cancel")
	}
}

func TestSupervisor_RestartsOnExit(t *testing.T) {
	var callCount atomic.Int64

	sv := daemon.NewSupervisor()
	sv.Add("quick-exit", func() *exec.Cmd {
		callCount.Add(1)
		return exec.Command("true") // exits immediately with code 0
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go sv.Run(ctx) //nolint:errcheck

	// Wait until the factory has been called at least 3 times (initial + 2 restarts).
	// Each restart waits 1s backoff, so allow 5s total.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if callCount.Load() >= 3 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancel()

	if n := callCount.Load(); n < 3 {
		t.Errorf("factory called %d times, want ≥3 (process not being restarted)", n)
	}
}

func TestSupervisor_MultipleProcesses(t *testing.T) {
	sv := daemon.NewSupervisor()
	sv.Add("sleep-a", func() *exec.Cmd { return exec.Command("sleep", "60") })
	sv.Add("sleep-b", func() *exec.Cmd { return exec.Command("sleep", "60") })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sv.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("supervisor with two processes did not stop within 2s after context cancel")
	}
}
