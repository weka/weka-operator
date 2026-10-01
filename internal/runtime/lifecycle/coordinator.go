// Package lifecycle owns the pod-runtime coordinator: the shutdown-request record, background
// task joining, and the fixed-order final cleanup shared by every runtime mode.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"

	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// ModeRuntime is the contract every mode workflow implements. Start and Shutdown never run
// concurrently.
type ModeRuntime interface {
	Start(context.Context) error
	Shutdown(context.Context) error
}

// Reason records why shutdown was requested.
type Reason int

const (
	ReasonSignal   Reason = iota // SIGTERM/SIGINT
	ReasonTakeover               // a newer runtime generation was published
)

// Outcome is the coordinator's terminal verdict.
type Outcome struct {
	StartupErr  error   // the original startup error, if any
	ShutdownErr error   // mode-specific shutdown error, if any
	CleanupErrs []error // non-fatal cleanup failures, in the order observed
	Requested   bool    // a shutdown request was recorded
	Takeover    bool    // takeover was recorded (sticky)
}

// ExitCode is 1 when StartupErr is a genuine failure, 0 otherwise. A StartupErr caused only by a
// recorded shutdown request (context.Canceled) is not a genuine failure.
func (o Outcome) ExitCode() int {
	if o.StartupErr == nil {
		return 0
	}
	if o.Requested && errors.Is(o.StartupErr, context.Canceled) {
		return 0
	}
	return 1
}

type namedCloser struct {
	name   string
	closer io.Closer
}

// Coordinator owns contexts, shutdown requests, background tasks, and final cleanup.
type Coordinator struct {
	pm *process.Manager

	servicesCtx    context.Context
	servicesCancel context.CancelFunc
	tasksCtx       context.Context
	tasksCancel    context.CancelFunc
	periodicCtx    context.Context // cancelled on shutdown request; stops periodic sleeps
	periodicCancel context.CancelFunc
	startupCtx     context.Context
	startupCancel  context.CancelFunc
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc

	mu           sync.Mutex
	requested    bool
	takeover     bool
	reqOnce      sync.Once
	reqCh        chan struct{}
	takeoverOnce sync.Once
	takeoverCh   chan struct{}

	taskWG    sync.WaitGroup
	taskErrMu sync.Mutex
	taskErrs  []error
	fatalOnce sync.Once
	fatalCh   chan struct{}
	fatalErr  error

	resMu          sync.Mutex
	cleanupStarted bool
	resources      []namedCloser
	last           *namedCloser
}

// New creates a coordinator rooted at ctx, which carries logging and tracing values only; it is
// never cancelled by the coordinator.
func New(ctx context.Context, pm *process.Manager) *Coordinator {
	c := &Coordinator{
		pm:         pm,
		reqCh:      make(chan struct{}),
		fatalCh:    make(chan struct{}),
		takeoverCh: make(chan struct{}),
	}
	c.servicesCtx, c.servicesCancel = context.WithCancel(ctx)
	c.tasksCtx, c.tasksCancel = context.WithCancel(ctx)
	c.periodicCtx, c.periodicCancel = context.WithCancel(c.tasksCtx)
	c.startupCtx, c.startupCancel = context.WithCancel(ctx)
	c.shutdownCtx, c.shutdownCancel = context.WithCancel(ctx)
	return c
}

// ServicesContext is the context supervised daemons run under; it outlives Weka shutdown.
func (c *Coordinator) ServicesContext() context.Context { return c.servicesCtx }

// ShutdownContext is the context mode-specific Shutdown work runs under.
func (c *Coordinator) ShutdownContext() context.Context { return c.shutdownCtx }

// RequestShutdown records a shutdown request and cancels startup. It is idempotent, and a
// takeover request remains recorded even if a signal was recorded first.
func (c *Coordinator) RequestShutdown(r Reason) {
	c.mu.Lock()
	c.requested = true
	if r == ReasonTakeover {
		c.takeover = true
	}
	c.mu.Unlock()

	c.startupCancel()
	c.periodicCancel()
	c.reqOnce.Do(func() { close(c.reqCh) })
	if r == ReasonTakeover {
		c.takeoverOnce.Do(func() { close(c.takeoverCh) })
	}
}

// TakeoverDone is closed once a takeover has been recorded; sticky.
func (c *Coordinator) TakeoverDone() <-chan struct{} { return c.takeoverCh }

func (c *Coordinator) requestState() (requested, takeover bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requested, c.takeover
}

// Takeover reports whether a newer runtime generation requested shutdown.
func (c *Coordinator) Takeover() bool {
	_, takeover := c.requestState()
	return takeover
}

// Go registers and starts a named background task joined during final cleanup. A non-nil error
// returned while the coordinator is waiting for shutdown is treated as a fatal owned-task error,
// equivalent to a startup failure.
func (c *Coordinator) Go(name string, fn func(ctx context.Context) error) {
	c.taskWG.Go(func() {
		err := fn(c.tasksCtx)
		if err == nil {
			return
		}
		wrapped := fmt.Errorf("task %s: %w", name, err)
		c.taskErrMu.Lock()
		c.taskErrs = append(c.taskErrs, wrapped)
		c.taskErrMu.Unlock()
		c.fatalOnce.Do(func() {
			c.fatalErr = wrapped
			close(c.fatalCh)
		})
	})
}

// GoPeriodic registers a background task that waits delay, then calls work every interval. A
// shutdown request wakes the sleep and prevents further iterations; work already running keeps
// the tasks context and is cancelled only by final cleanup. A work error is logged, not fatal:
// one failed periodic pass must not tear down the runtime.
func (c *Coordinator) GoPeriodic(name string, clk clock.Clock, delay, interval time.Duration, work func(ctx context.Context) error) {
	c.Go(name, func(ctx context.Context) error {
		_, logger := instrumentation.CreateLogSpan(ctx, name)
		defer logger.End()

		if err := clock.Sleep(c.periodicCtx, clk, delay); err != nil {
			return nil
		}
		for {
			if err := work(ctx); err != nil {
				logger.Warn("periodic task failed (non-fatal)", "err", err)
			}
			if err := clock.Sleep(c.periodicCtx, clk, interval); err != nil {
				return nil
			}
		}
	})
}

// Register takes ownership of an acquired resource, released in reverse acquisition order during
// final cleanup. It returns an error once cleanup has already begun, so the caller can close the
// resource itself instead.
func (c *Coordinator) Register(name string, closer io.Closer) error {
	return c.register(name, closer, false)
}

// RegisterLast takes ownership of the generation lock, which is always released last.
func (c *Coordinator) RegisterLast(name string, closer io.Closer) error {
	return c.register(name, closer, true)
}

func (c *Coordinator) register(name string, closer io.Closer, last bool) error {
	c.resMu.Lock()
	defer c.resMu.Unlock()
	if c.cleanupStarted {
		return fmt.Errorf("lifecycle: cleanup already began, cannot register %q", name)
	}
	if last {
		if c.last != nil {
			return fmt.Errorf("lifecycle: RegisterLast already set to %q", c.last.name)
		}
		c.last = &namedCloser{name: name, closer: closer}
		return nil
	}
	c.resources = append(c.resources, namedCloser{name: name, closer: closer})
	return nil
}

// Run drives the full lifecycle and returns the exit decision. It never calls os.Exit.
func (c *Coordinator) Run(ctx context.Context, rt ModeRuntime) Outcome {
	_, logger := instrumentation.CreateLogSpan(ctx, "lifecycle.run")
	defer logger.End()

	defer c.startupCancel()
	defer c.shutdownCancel()
	defer c.tasksCancel()
	defer c.servicesCancel()

	// Installed before Start so a signal arriving during startup is recorded, not delivered with
	// the default action (which would kill the process before gated modes get to wait for approval).
	stopSignals := c.installSignals()

	startErr := rt.Start(c.startupCtx)

	var outcome Outcome
	requested, takeover := c.requestState()

	switch {
	case requested:
		// A recorded signal/takeover wins over an overlapping startup error: the error is kept
		// for reporting, but shutdown still runs.
		outcome.Requested = true
		outcome.Takeover = takeover
		outcome.StartupErr = startErr
		outcome.ShutdownErr = rt.Shutdown(c.shutdownCtx)
	case startErr != nil:
		// Without a recorded request, commit to failure cleanup: local cleanup only. Do not wait
		// for operator approval or issue additional Weka stop commands here; coordinated rollback
		// after a partial startup failure needs a separate operator/runtime design (see the open
		// item on this in doc/dev). Stopping the agent does not guarantee every Weka container
		// stopped.
		outcome.StartupErr = startErr
	default:
		select {
		case <-c.reqCh:
			requested, takeover = c.requestState()
			outcome.Requested = requested
			outcome.Takeover = takeover
			outcome.ShutdownErr = rt.Shutdown(c.shutdownCtx)
		case <-c.fatalCh:
			outcome.StartupErr = c.fatalErr
		}
	}

	stopSignals()
	outcome.CleanupErrs = c.cleanup()

	logger.Info("lifecycle finished",
		"requested", outcome.Requested, "takeover", outcome.Takeover, "exitCode", outcome.ExitCode())
	return outcome
}

// cleanup runs the fixed four final-cleanup phases and collects, rather than aborts on, errors.
func (c *Coordinator) cleanup() []error {
	c.resMu.Lock()
	c.cleanupStarted = true
	resources := c.resources
	last := c.last
	c.resMu.Unlock()

	var errs []error

	// Phase 1: cancel and join every task registered via Go.
	c.tasksCancel()
	c.taskWG.Wait()
	c.taskErrMu.Lock()
	for _, err := range c.taskErrs {
		if err != c.fatalErr { // the fatal one, if any, was already reported as StartupErr
			errs = append(errs, err)
		}
	}
	c.taskErrMu.Unlock()

	// Phase 2 (also covers phase 3, folded into Shutdown): disable daemon restarts, terminate
	// managed process groups and join the reaper, then let services go.
	c.pm.DisableRestarts()
	if err := c.pm.Shutdown(c.shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("process manager shutdown: %w", err))
	}
	c.servicesCancel()

	// Phase 4: release registered resources in reverse acquisition order, generation lock last.
	for _, r := range slices.Backward(resources) {
		if err := r.closer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close %s: %w", r.name, err))
		}
	}
	if last != nil {
		if err := last.closer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close %s: %w", last.name, err))
		}
	}

	return errs
}

// installSignals owns SIGTERM/SIGINT for the lifetime of Run and turns each into a shutdown
// request. The returned func stops and drains the notification.
func (c *Coordinator) installSignals() func() {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range ch {
			c.RequestShutdown(ReasonSignal)
		}
	}()
	return func() {
		signal.Stop(ch)
		close(ch)
		<-done
	}
}
