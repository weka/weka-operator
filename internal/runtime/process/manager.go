package process

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"

	"github.com/weka/weka-operator/internal/runtime/clock"
)

// entry is one managed child. Daemons reuse the same entry across restarts, keeping a stable
// position in Manager.procs for reverse-order shutdown.
type entry struct {
	name string
	// restart, cmd, pid and finished are read/written under Manager.mu; finished marks a
	// StartDaemon/StartProcess entry that has stopped for good, so its now-stale pid must not
	// shadow a real orphan of the same pid for the reaper (entries stay registered until
	// Shutdown, for stable reverse-order termination).
	restart  bool
	cmd      *exec.Cmd
	pid      int
	finished bool
	done     chan struct{}
	err      error
}

func (e *entry) finish(err error) {
	e.err = err
	close(e.done)
}

// Handle is a reference to a process (and, for daemons, its supervisor) started by Manager.
type Handle struct {
	Name string
	Done <-chan struct{}

	e *entry
}

// Err returns the final error once Done is closed, or nil if the process is still running,
// exited cleanly, or the Handle was built without an entry (e.g. a test double).
func (h *Handle) Err() error {
	select {
	case <-h.Done:
		if h.e == nil {
			return nil
		}
		return h.e.err
	default:
		return nil
	}
}

// Manager owns every subprocess the runtime creates, plus the Linux orphan reaper.
type Manager struct {
	mu              sync.Mutex
	procs           []*entry // registration order
	servicesEnabled bool
	clock           clock.Clock
	reaper          *reaper
}

// Launcher is the subset of Manager that workflows use to register long-lived services
// (daemons and unwaited background processes) without depending on the concrete type.
type Launcher interface {
	StartDaemon(ctx context.Context, name string, c Command) (*Handle, error)
	StartProcess(ctx context.Context, name string, c Command) (*Handle, error)
}

var _ Launcher = (*Manager)(nil)

// NewManager creates a Manager and starts its orphan reaper (Linux only; a no-op elsewhere).
func NewManager(ctx context.Context) *Manager {
	return newManager(ctx, clock.System)
}

// NewManagerWithClock is NewManager with an injectable Clock, for deterministic tests of the
// daemon restart cadence.
func NewManagerWithClock(ctx context.Context, c clock.Clock) *Manager {
	return newManager(ctx, c)
}

func newManager(ctx context.Context, c clock.Clock) *Manager {
	m := &Manager{servicesEnabled: true, clock: c}
	m.reaper = newReaper(m)
	go m.reaper.run(ctx)
	return m
}

// DisableRestarts stops any future daemon relaunch. Idempotent; also called by Shutdown.
func (m *Manager) DisableRestarts() {
	m.mu.Lock()
	m.servicesEnabled = false
	m.mu.Unlock()
}

func buildCmd(c *Command) (cmd *exec.Cmd, stdout, stderr *bytes.Buffer) {
	cmd = exec.Command(c.Path, c.Args...) //nolint:gosec // args are controlled by internal callers
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.Stdin = c.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var outBuf, errBuf bytes.Buffer
	stdout, stderr = &outBuf, &errBuf
	if c.Output == Inherit {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	} else {
		cmd.Stdout = stdout
		cmd.Stderr = stderr
	}
	return cmd, stdout, stderr
}

// buildBackgroundCmd is buildCmd for daemons and background processes. Nothing reads their output,
// and a capture pipe would make Wait block until every descendant holding it exits, such as the
// envoy container the agent leaves running, so shutdown would hang until the pod is killed.
func buildBackgroundCmd(c *Command) *exec.Cmd {
	cmd, _, _ := buildCmd(c)
	if c.Output == Capture {
		cmd.Stdout, cmd.Stderr = nil, nil
	}
	return cmd
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// terminate sends SIGTERM to pid's process group. Never escalates to SIGKILL: a process that
// ignores SIGTERM is left running (see the TODO on Shutdown). ESRCH means the group is already
// gone, which is not an error the caller can act on.
func terminate(pid int) error {
	if pid == 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

// Run starts c, waits for it to finish, and returns its captured output. Cancelling ctx sends
// SIGTERM to the process group and waits for the same exit; the process is never re-waited.
// Command is passed by value here to satisfy the CommandRunner interface; internal hot-path
// helpers below take *Command instead.
//
//nolint:gocritic
func (m *Manager) Run(ctx context.Context, c Command) (Result, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "cmd", "command", render(&c))
	defer logger.End()

	logExec := c.Log == LogAll || c.Log == LogExecution
	logOutput := c.Log == LogAll || c.Log == LogOutput

	if logExec {
		logger.Info("running command", "command", render(&c))
	}

	cmd, stdout, stderr := buildCmd(&c)

	m.mu.Lock()
	startErr := cmd.Start()
	var e *entry
	if startErr == nil {
		e = &entry{cmd: cmd, pid: cmd.Process.Pid, done: make(chan struct{})}
		m.procs = append(m.procs, e)
	}
	m.mu.Unlock()

	if startErr != nil {
		return Result{}, &ExecError{Cmd: render(&c), Kind: FailureLaunch, Err: startErr}
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		if err := terminate(e.pid); err != nil {
			logger.Error(err, "failed to signal process group", "pid", e.pid)
		}
		waitErr = <-waitCh
	}

	m.mu.Lock()
	m.removeEntry(e)
	m.mu.Unlock()

	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: exitCodeOf(waitErr)}

	if logOutput {
		if len(result.Stdout) > 0 {
			logger.Info("stdout", "output", string(result.Stdout))
		}
		if len(result.Stderr) > 0 {
			logger.Info("stderr", "output", string(result.Stderr))
		}
	}
	if logExec {
		logger.Info("command finished", "command", render(&c), "code", result.ExitCode)
	}

	if cerr := ctx.Err(); cerr != nil {
		return result, &ExecError{Cmd: render(&c), Kind: FailureCancelled, Result: result, Err: cerr}
	}
	if waitErr != nil {
		return result, &ExecError{Cmd: render(&c), Kind: FailureExit, Result: result, Err: waitErr}
	}
	return result, nil
}

func (m *Manager) removeEntry(e *entry) {
	for i, cur := range m.procs {
		if cur == e {
			m.procs = append(m.procs[:i], m.procs[i+1:]...)
			return
		}
	}
}

// StartDaemon launches a supervised background process that is relaunched on a fixed 3-second
// cadence when it exits, for as long as restarts are enabled. It returns only the initial
// launch error; later relaunch failures are logged and end that supervisor alone.
// Command is passed by value here per the documented API; internal hot-path helpers below
// take *Command instead.
//
//nolint:gocritic
func (m *Manager) StartDaemon(ctx context.Context, name string, c Command) (*Handle, error) {
	return m.start(ctx, name, &c, true)
}

// StartProcess launches a background process that is not restarted when it exits.
// Command is passed by value here per the documented API; internal hot-path helpers below
// take *Command instead.
//
//nolint:gocritic
func (m *Manager) StartProcess(ctx context.Context, name string, c Command) (*Handle, error) {
	return m.start(ctx, name, &c, false)
}

func (m *Manager) start(ctx context.Context, name string, c *Command, restart bool) (*Handle, error) {
	_, logger := instrumentation.CreateLogSpan(ctx, "process.supervise", "name", name)

	cmd := buildBackgroundCmd(c)

	m.mu.Lock()
	startErr := cmd.Start()
	if startErr != nil {
		m.mu.Unlock()
		logger.End()
		return nil, &ExecError{Cmd: render(c), Kind: FailureLaunch, Err: startErr}
	}
	e := &entry{name: name, restart: restart, cmd: cmd, pid: cmd.Process.Pid, done: make(chan struct{})}
	m.procs = append(m.procs, e)
	m.mu.Unlock()

	logger.Info("process started", "name", name, "pid", e.pid)

	go m.supervise(ctx, logger, e, c)

	return &Handle{Name: name, Done: e.done, e: e}, nil
}

// supervise owns e.cmd/e.pid after the initial launch in start: it waits for the child, and
// while restarts remain enabled, relaunches it on a fixed 3-second cadence. Daemons/background
// processes have no path to surface captured output (Handle exposes only Name/Done/Err), so
// they get no capture pipes at all; only Run captures output.
func (m *Manager) supervise(ctx context.Context, logger *instrumentation.SpanLogger, e *entry, c *Command) {
	defer logger.End()
	cmd := e.cmd

	for {
		waitErr := cmd.Wait()
		logger.Info("process exited", "name", e.name, "pid", e.pid, "code", exitCodeOf(waitErr), "err", waitErr)

		m.mu.Lock()
		shouldRestart := e.restart && m.servicesEnabled
		if !shouldRestart {
			e.finished = true
		}
		m.mu.Unlock()
		if !shouldRestart {
			e.finish(waitErr)
			return
		}

		if err := clock.Sleep(ctx, m.clock, 3*time.Second); err != nil {
			m.mu.Lock()
			e.finished = true
			m.mu.Unlock()
			e.finish(err)
			return
		}

		m.mu.Lock()
		if !m.servicesEnabled {
			e.finished = true
			m.mu.Unlock()
			e.finish(waitErr)
			return
		}
		newCmd := buildBackgroundCmd(c)
		startErr := newCmd.Start()
		if startErr != nil {
			e.finished = true
			m.mu.Unlock()
			logger.Error(startErr, "replacement launch failed", "name", e.name)
			e.finish(startErr)
			return
		}
		e.cmd, e.pid = newCmd, newCmd.Process.Pid
		m.mu.Unlock()

		logger.Info("process restarted", "name", e.name, "pid", e.pid)
		cmd = newCmd
	}
}

// Shutdown disables restarts, then terminates every managed process group in reverse
// registration order, waiting for each direct child before moving to the next, and finally
// stops the reaper.
//
// TODO: termination can block indefinitely on a process that ignores SIGTERM.
func (m *Manager) Shutdown(ctx context.Context) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "process.shutdown")
	defer logger.End()

	m.DisableRestarts()

	m.mu.Lock()
	procs := make([]*entry, len(m.procs))
	copy(procs, m.procs)
	m.mu.Unlock()

	for _, e := range slices.Backward(procs) {
		m.mu.Lock()
		pid := e.pid
		m.mu.Unlock()
		if err := terminate(pid); err != nil {
			logger.Error(err, "failed to signal process group", "name", e.name, "pid", pid)
		}
		<-e.done
	}

	m.reaper.stopAndJoin()
	return nil
}
