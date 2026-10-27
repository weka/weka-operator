package daemon

import (
	"context"
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
		backoff = time.Second

		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		select {
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM) //nolint:errcheck // best-effort termination
			<-done
			return
		case err := <-done:
			logger.Warn("process exited unexpectedly", "err", err)
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
