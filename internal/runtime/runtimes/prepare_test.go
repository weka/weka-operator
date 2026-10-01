package runtimes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/lifecycle"
	"github.com/weka/weka-operator/internal/runtime/paths"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// launchCall records one Launcher.Start{Daemon,Process} invocation.
type launchCall struct {
	kind string // "daemon" or "process"
	name string
	cmd  process.Command
}

// fakeLauncher is a process.Launcher test double: it records launches and returns a Handle
// backed by an open, never-closed Done channel, so Handle.Err() never blocks callers.
type fakeLauncher struct {
	calls []launchCall
}

func (f *fakeLauncher) StartDaemon(_ context.Context, name string, c process.Command) (*process.Handle, error) {
	f.calls = append(f.calls, launchCall{kind: "daemon", name: name, cmd: c})
	return &process.Handle{Name: name, Done: make(chan struct{})}, nil
}

func (f *fakeLauncher) StartProcess(_ context.Context, name string, c process.Command) (*process.Handle, error) {
	f.calls = append(f.calls, launchCall{kind: "process", name: name, cmd: c})
	return &process.Handle{Name: name, Done: make(chan struct{})}, nil
}

// recordingRunner records issued commands and always reports success with an empty result,
// unless respond is set, in which case respond gets first refusal on each command. Guarded by
// mu since some tests run it concurrently from both the stop loop and a takeover/force watcher.
type recordingRunner struct {
	mu       sync.Mutex
	commands []process.Command
	respond  func(process.Command) (process.Result, bool, error)
}

func (f *recordingRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, c)
	if f.respond != nil {
		if res, handled, err := f.respond(c); handled {
			return res, err
		}
	}
	return process.Result{}, nil
}

// commandsSnapshot returns a copy of the commands recorded so far. Safe to call while another
// goroutine may still be issuing commands through Run.
func (f *recordingRunner) commandsSnapshot() []process.Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]process.Command(nil), f.commands...)
}

// TestRunWekaShutdown_NoAgentLaunched_SkipsStop verifies Shutdown never touches the runner
// when the guarded Start never got far enough to launch an agent.
func TestRunWekaShutdown_NoAgentLaunched_SkipsStop(t *testing.T) {
	runner := &recordingRunner{}
	ctx := context.Background()
	deps := &Deps{Runner: runner, Clock: clock.System, Paths: paths.Roots{}, Coord: testCoordinator(ctx)}

	// "data-services" has StopPolicy approval=false, so this never reaches AwaitApproval either.
	if err := runWekaShutdown(ctx, deps, "data-services", "name", "pod", false); err != nil {
		t.Fatalf("runWekaShutdown() error = %v", err)
	}
	if len(runner.commands) != 0 {
		t.Errorf("commands = %v, want none", runner.commands)
	}
}

// TestRunWekaShutdown_AgentLaunched_CallsStopLoop verifies Shutdown does stop the container
// once Start recorded that an agent was launched.
func TestRunWekaShutdown_AgentLaunched_CallsStopLoop(t *testing.T) {
	runner := &recordingRunner{}
	ctx := context.Background()
	deps := &Deps{Runner: runner, Clock: clock.System, Paths: paths.Roots{}, Coord: testCoordinator(ctx)}

	if err := runWekaShutdown(ctx, deps, "data-services", "name", "pod", true); err != nil {
		t.Fatalf("runWekaShutdown() error = %v", err)
	}
	if len(runner.commands) == 0 {
		t.Fatal("expected at least one command (the running check), got none")
	}
}

// stoppedRunner reports the container running until a "--force" stop has been recorded
// (StopLoop's own forced retry, or a direct ForceStop call), then reports it stopped: matching
// what "weka local ps --json" would see once a stop has actually landed. A plain graceful ("-g")
// attempt does NOT flip it, so tests can exercise escalation before the container "stops".
func stoppedRunner() *recordingRunner {
	r := &recordingRunner{}
	r.respond = func(c process.Command) (process.Result, bool, error) {
		if c.Path != "weka" || len(c.Args) < 2 || c.Args[0] != "local" || c.Args[1] != "ps" {
			return process.Result{}, false, nil
		}
		status := "Running"
		for _, cmd := range r.commands { // safe: only ever called while f.mu is held by Run
			if strings.Contains(strings.Join(cmd.Args, " "), "--force") {
				status = "Stopped"
				break
			}
		}
		return process.Result{Stdout: []byte(`[{"name":"name","runStatus":"` + status + `"}]`)}, true, nil
	}
	return r
}

// TestRunWekaShutdown_TakeoverDuringApprovalWait_ForceStops covers a takeover recorded while
// an approval-gated mode is still waiting: the force stop fires immediately, in parallel with
// the wait, and the wait is only released afterward by an allow-stop instruction.
func TestRunWekaShutdown_TakeoverDuringApprovalWait_ForceStops(t *testing.T) {
	runner := stoppedRunner()
	fc := clock.NewFake(time.Unix(0, 0))
	tmp := t.TempDir()
	ctx := context.Background()
	coord := testCoordinator(ctx)
	deps := &Deps{Runner: runner, Clock: fc, Paths: paths.Roots{Tmp: tmp}, Coord: coord}

	done := make(chan error, 1)
	go func() {
		done <- runWekaShutdown(ctx, deps, "compute", "name", "pod", true) // approval=true, force=false
	}()

	// Closing takeoverCh happens-before any later receive on it, so no synchronization is
	// needed before requesting it: the watcher goroutine sees it whenever it runs its select.
	coord.RequestShutdown(lifecycle.ReasonTakeover)
	waitUntilTrue(t, func() bool { return findForceStopCall(runner) }) // force stop landed while still waiting

	if err := os.WriteFile(filepath.Join(tmp, ".allow-stop"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	advanceFakeClockUntilDone(t, fc, done)

	if n := countForceStopCalls(runner); n != 1 {
		t.Fatalf("force stop calls = %d, want exactly 1", n)
	}
}

// TestRunWekaShutdown_TakeoverBeforeShutdown_ForceStopsImmediately covers a takeover already
// recorded (during Start) by the time Shutdown runs: the force stop must not wait on anything.
func TestRunWekaShutdown_TakeoverBeforeShutdown_ForceStopsImmediately(t *testing.T) {
	runner := stoppedRunner()
	fc := clock.NewFake(time.Unix(0, 0))
	ctx := context.Background()
	coord := testCoordinator(ctx)
	coord.RequestShutdown(lifecycle.ReasonTakeover)
	deps := &Deps{Runner: runner, Clock: fc, Paths: paths.Roots{}, Coord: coord}

	done := make(chan error, 1)
	go func() {
		done <- runWekaShutdown(ctx, deps, "data-services", "name", "pod", true) // approval=false, force=true
	}()

	advanceFakeClockUntilDone(t, fc, done)

	if n := countForceStopCalls(runner); n != 1 {
		t.Fatalf("force stop calls = %d, want exactly 1", n)
	}
}

// TestRunWekaShutdown_ForceInstructionDuringGracefulLoop_Escalates covers a graceful stop
// (a mode absent from StopPolicy's table: approval=false, force=false) that is escalated
// mid-flight by an allow-force-stop instruction, mirroring the operator writing it after a
// graceful stop is already in flight.
func TestRunWekaShutdown_ForceInstructionDuringGracefulLoop_Escalates(t *testing.T) {
	runner := stoppedRunner()
	fc := clock.NewFake(time.Unix(0, 0))
	tmp := t.TempDir()
	ctx := context.Background()
	deps := &Deps{Runner: runner, Clock: fc, Paths: paths.Roots{Tmp: tmp}, Coord: testCoordinator(ctx)}

	done := make(chan error, 1)
	go func() {
		done <- runWekaShutdown(ctx, deps, "discovery", "name", "pod", true) // approval=false, force=false
	}()

	waitUntilTrue(t, func() bool { return len(runner.commandsSnapshot()) > 0 }) // StopLoop's first running-check landed
	if err := os.WriteFile(filepath.Join(tmp, ".allow-force-stop"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	advanceFakeClockUntilDone(t, fc, done)

	if n := countForceStopCalls(runner); n != 1 {
		t.Fatalf("force stop calls = %d, want exactly 1", n)
	}
}

// waitUntilTrue polls cond until it is true, failing the test after one second.
func waitUntilTrue(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

// findForceStopCall reports whether a "weka local stop <name> --force" command was recorded.
func findForceStopCall(runner *recordingRunner) bool {
	return countForceStopCalls(runner) > 0
}

// countForceStopCalls counts "weka local stop <name> --force" commands recorded so far.
func countForceStopCalls(runner *recordingRunner) int {
	n := 0
	for _, c := range runner.commandsSnapshot() {
		if c.Path == "weka" && len(c.Args) > 0 && strings.Contains(strings.Join(c.Args, " "), "--force") {
			n++
		}
	}
	return n
}

// advanceFakeClockUntilDone repeatedly advances the fake clock until done fires, so any number
// of clock.Sleep/Poll calls parked on it get unblocked without deadlocking the test.
func advanceFakeClockUntilDone(t *testing.T, fc *clock.Fake, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("runWekaShutdown() error = %v", err)
			}
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("runWekaShutdown did not return before timeout")
		}
		fc.Advance(time.Second)
		time.Sleep(time.Millisecond)
	}
}
