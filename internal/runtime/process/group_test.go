package process

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCancellationTerminatesEntireProcessGroup verifies that cancelling Run's context signals
// the whole process group (Setpgid), not just the direct child: a background grandchild
// spawned by the shell must also receive SIGTERM.
func TestCancellationTerminatesEntireProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "childpid")
	m := NewManager(context.Background())
	ctx, cancel := context.WithCancel(context.Background())

	script := fmt.Sprintf("sleep 30 & echo $! > %s; wait", pidFile)
	done := make(chan error, 1)
	go func() {
		_, err := m.Run(ctx, Command{Path: "sh", Args: []string{"-c", script}})
		done <- err
	}()

	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil && len(data) > 0 {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				childPID = pid
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("background child did not report its pid in time")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if isProcessGone(childPID) {
			return // exited/reaped, or (Linux) a zombie past the point this test can affect
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("background child pid %d still alive after group SIGTERM", childPID)
}

// isProcessGone reports whether pid has terminated. sleep's real parent is sh, which this test
// also killed; if sh released it before it died, the kernel reparents it up to the pid
// namespace's PID 1, which in this test environment never reaps, so it lingers as a zombie
// (state Z) indefinitely. A zombie already ran its signal disposition and freed everything but
// its exit status, so for a liveness check it counts as gone. On non-Linux (no /proc) this falls
// back to the plain existence check.
func isProcessGone(pid int) bool {
	if data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		if i := strings.LastIndexByte(string(data), ')'); i >= 0 && i+2 < len(data) {
			if fields := strings.Fields(string(data[i+2:])); len(fields) > 0 {
				return fields[0] == "Z"
			}
		}
		return false
	}
	return syscall.Kill(pid, 0) != nil
}
