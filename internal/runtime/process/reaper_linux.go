//go:build linux

package process

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
)

// reaper periodically claims orphaned children (grandchildren whose direct parent already
// exited) so they don't accumulate as zombies. It only ever targets PIDs not registered with
// Manager, so it never races a direct Wait.
type reaper struct {
	m    *Manager
	stop chan struct{}
	done chan struct{}
}

func newReaper(m *Manager) *reaper {
	return &reaper{m: m, stop: make(chan struct{}), done: make(chan struct{})}
}

func (r *reaper) run(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-ticker.C:
			r.sweep(ctx)
		}
	}
}

func (r *reaper) stopAndJoin() {
	close(r.stop)
	<-r.done
}

// sweep reaps every child of this process not tracked by Manager. A finished entry's pid is
// stale (the OS may have recycled it for a real orphan since), so it must not shadow that pid.
func (r *reaper) sweep(ctx context.Context) {
	_, logger := instrumentation.CreateLogSpan(ctx, "process.reaper.sweep")
	defer logger.End()

	self := os.Getpid()
	for _, pid := range scanChildren(self) {
		r.m.mu.Lock()
		tracked := false
		for _, e := range r.m.procs {
			if e.pid == pid && !e.finished {
				tracked = true
				break
			}
		}
		if !tracked {
			var ws syscall.WaitStatus
			// ECHILD/ESRCH mean the child is already gone by other means; expected races,
			// not actionable. Anything else is unexpected and worth logging.
			if _, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); err != nil &&
				err != syscall.ECHILD && err != syscall.ESRCH {
				logger.Error(err, "unexpected error reaping orphan", "pid", pid)
			}
		}
		r.m.mu.Unlock()
	}
}

// scanChildren returns PIDs under /proc whose ppid is self.
func scanChildren(self int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var children []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		ppid, ok := readPPID(pid)
		if ok && ppid == self {
			children = append(children, pid)
		}
	}
	return children
}

// readPPID parses field 4 of /proc/<pid>/stat. The comm field (2) may contain spaces or
// parens, so the split point is the last ')' rather than a fixed field index.
func readPPID(pid int) (int, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return 0, false
	}
	fields := strings.Fields(s[i+2:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}
