package shutdown

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/paths"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// fakeRunner is a minimal process.CommandRunner test double: it records every call and
// delegates the result to resultFn, if set.
type fakeRunner struct {
	mu       sync.Mutex
	calls    []process.Command
	resultFn func(c process.Command) (process.Result, error)
}

func (f *fakeRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if f.resultFn != nil {
		return f.resultFn(c)
	}
	return process.Result{}, nil
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeRunner) lastCall() process.Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func psResult(name, status string) process.Result {
	b, _ := json.Marshal([]map[string]interface{}{{"name": name, "runStatus": status}})
	return process.Result{Stdout: b}
}

func TestIsContainerRunning(t *testing.T) {
	cases := []struct {
		name     string
		resultFn func(process.Command) (process.Result, error)
		forced   bool
		want     bool
	}{
		{"query failure graceful means still running", func(process.Command) (process.Result, error) {
			return process.Result{}, errors.New("boom")
		}, false, true},
		{"query failure forced means not running", func(process.Command) (process.Result, error) {
			return process.Result{}, errors.New("boom")
		}, true, false},
		{"status stopped means not running", func(process.Command) (process.Result, error) {
			return psResult("foo", "Stopped"), nil
		}, false, false},
		{"status running means running", func(process.Command) (process.Result, error) {
			return psResult("foo", "Running"), nil
		}, false, true},
		{"container absent means not running", func(process.Command) (process.Result, error) {
			return psResult("bar", "Running"), nil
		}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{resultFn: tc.resultFn}
			if got := isContainerRunning(context.Background(), runner, "foo", tc.forced); got != tc.want {
				t.Fatalf("isContainerRunning() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStopLoopUsesGracefulFlagThenStopsOnStopped(t *testing.T) {
	var psCalls int
	runner := &fakeRunner{}
	runner.resultFn = func(c process.Command) (process.Result, error) {
		if c.Path == "weka" {
			psCalls++
			if psCalls == 1 {
				return psResult("foo", "Running"), nil
			}
			return psResult("foo", "Stopped"), nil
		}
		return process.Result{}, nil
	}
	fc := clock.NewFake(time.Unix(0, 0))

	done := make(chan error, 1)
	go func() { done <- StopLoop(context.Background(), runner, fc, "foo", false) }()

	waitUntil(t, time.Second, func() bool { return runner.callCount() == 2 }) // ps(running) + stop
	fc.Advance(stopRetryInterval)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StopLoop() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StopLoop did not return")
	}

	if got := runner.callCount(); got != 3 { // ps(running) + stop + ps(stopped)
		t.Fatalf("callCount = %d, want 3", got)
	}
	stopCmd := runner.calls[1]
	if stopCmd.Path != "timeout" || stopCmd.Args[len(stopCmd.Args)-1] != "-g" {
		t.Fatalf("unexpected stop command: %+v", stopCmd)
	}
}

func TestStopLoopUsesForceFlag(t *testing.T) {
	var psCalls int
	runner := &fakeRunner{}
	runner.resultFn = func(c process.Command) (process.Result, error) {
		if c.Path == "weka" {
			psCalls++
			if psCalls == 1 {
				return psResult("foo", "Running"), nil
			}
			return psResult("foo", "Stopped"), nil
		}
		return process.Result{}, nil
	}
	fc := clock.NewFake(time.Unix(0, 0))

	done := make(chan error, 1)
	go func() { done <- StopLoop(context.Background(), runner, fc, "foo", true) }()

	waitUntil(t, time.Second, func() bool { return runner.callCount() == 2 })
	fc.Advance(stopRetryInterval)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StopLoop() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StopLoop did not return")
	}

	stopCmd := runner.calls[1]
	if stopCmd.Path != "timeout" || stopCmd.Args[len(stopCmd.Args)-1] != "--force" {
		t.Fatalf("unexpected stop command: %+v", stopCmd)
	}
}

func TestWatchForceIssuesExactlyOneForceStop(t *testing.T) {
	dir := t.TempDir()
	roots := paths.Roots{Tmp: dir}
	runner := &fakeRunner{}
	fc := clock.NewFake(time.Unix(0, 0))

	done := make(chan error, 1)
	go func() {
		done <- WatchForce(context.Background(), ForceWatchInput{Runner: runner, Clock: fc, Paths: roots, Name: "foo"})
	}()

	time.Sleep(50 * time.Millisecond)
	if got := runner.callCount(); got != 0 {
		t.Fatalf("force stop issued before marker present: %d calls", got)
	}

	if err := os.WriteFile(filepath.Join(dir, ".allow-force-stop"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fc.Advance(approvalPollInterval)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WatchForce() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WatchForce did not return after marker written")
	}

	if got := runner.callCount(); got != 1 {
		t.Fatalf("callCount = %d, want 1", got)
	}
	last := runner.lastCall()
	if last.Path != "weka" || last.Args[len(last.Args)-1] != "--force" {
		t.Fatalf("unexpected force command: %+v", last)
	}
}

func TestWatchForceCtxCancelReturnsNilWithoutForcing(t *testing.T) {
	dir := t.TempDir()
	roots := paths.Roots{Tmp: dir}
	runner := &fakeRunner{}
	fc := clock.NewFake(time.Unix(0, 0))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- WatchForce(ctx, ForceWatchInput{Runner: runner, Clock: fc, Paths: roots, Name: "foo"})
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	fc.Advance(approvalPollInterval)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WatchForce() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WatchForce did not return after ctx cancel")
	}
	if got := runner.callCount(); got != 0 {
		t.Fatalf("force stop issued despite ctx cancel: %d calls", got)
	}
}
