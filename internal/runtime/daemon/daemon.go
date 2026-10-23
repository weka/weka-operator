package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
)

type CmdFactory func() *exec.Cmd

type managedProc struct {
	name    string
	factory CmdFactory
}

type Supervisor struct {
	procs []*managedProc
}

func NewSupervisor() *Supervisor {
	return &Supervisor{}
}

func (s *Supervisor) Add(name string, factory CmdFactory) {
	s.procs = append(s.procs, &managedProc{name: name, factory: factory})
}

func (s *Supervisor) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, p := range s.procs {
		wg.Add(1)
		go func(mp *managedProc) {
			defer wg.Done()
			s.supervise(ctx, mp)
		}(p)
	}
	wg.Wait()
	return nil
}

func (s *Supervisor) supervise(ctx context.Context, mp *managedProc) {
	_, logger := instrumentation.CreateLogSpan(ctx, "daemon.supervise", "process", mp.name)
	defer logger.End()

	backoff := time.Second
	for {
		cmd := mp.factory()
		// Mirror Python start_process(): send weka-agent/syslog output to pod logs.
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			logger.Error(err, "failed to start process")
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				backoff = minDuration(backoff*2, 30*time.Second)
				continue
			}
		}
		// Mirror Python: logging.info(f"Daemon started with PID {process.pid} for command {command}")
		logger.Info("process started", "pid", cmd.Process.Pid)
		backoff = time.Second

		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		select {
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM) //nolint:errcheck // best-effort termination
			<-done
			return
		case err := <-done:
			exitCode := -1
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			}
			logger.Warn("process exited unexpectedly", "err", err, "exit_code", exitCode, "backoff", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				backoff = minDuration(backoff*2, 30*time.Second)
			}
		}
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
