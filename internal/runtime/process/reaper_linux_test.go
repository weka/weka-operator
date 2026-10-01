//go:build linux

package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const subreaperHelperEnv = "WEKA_PROCESS_TEST_SUBREAPER"

// TestMain intercepts re-exec of this same test binary to run as an isolated child-subreaper
// helper. Production code never sets PR_SET_CHILD_SUBREAPER (see manager.go); it is only safe
// to do here because this helper process does nothing else and exits promptly.
func TestMain(m *testing.M) {
	if os.Getenv(subreaperHelperEnv) == "1" {
		runSubreaperHelper()
		return
	}
	os.Exit(m.Run())
}

func runSubreaperHelper() {
	const prSetChildSubreaper = 36
	if _, _, errno := syscall.Syscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); errno != 0 {
		fmt.Fprintln(os.Stderr, "prctl(PR_SET_CHILD_SUBREAPER) failed:", errno)
		os.Exit(1)
	}

	m := NewManager(context.Background())
	_ = m // starts the Linux reaper goroutine that this test exercises

	pidFile := os.Getenv("WEKA_PROCESS_TEST_PIDFILE")
	// The inner shell backgrounds a short-lived grandchild and exits immediately, orphaning
	// the grandchild to us (the subreaper) before it has finished running.
	script := "sleep 1 & echo $! > " + pidFile + "; exit 0"
	if err := exec.Command("sh", "-c", script).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "helper child failed:", err)
		os.Exit(1)
	}

	fmt.Println("ready")
	time.Sleep(5 * time.Second)
}

// TestReaperClaimsOrphanedGrandchild verifies that Manager's reaper reaps a grandchild orphaned
// onto it, instead of leaving it as a permanent zombie. It re-execs this test binary as an
// isolated subreaper process (see runSubreaperHelper) since PR_SET_CHILD_SUBREAPER is
// test-only.
func TestReaperClaimsOrphanedGrandchild(t *testing.T) {
	pidFile := t.TempDir() + "/childpid"
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), subreaperHelperEnv+"=1", "WEKA_PROCESS_TEST_PIDFILE="+pidFile)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	buf := make([]byte, 64)
	n, _ := stdout.Read(buf)
	if !strings.Contains(string(buf[:n]), "ready") {
		t.Fatalf("helper did not report ready, got %q", buf[:n])
	}

	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil && len(data) > 0 {
			if p, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				pid = p
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("grandchild did not report its pid in time")
	}

	// The grandchild (sleep 1) exits well within this window; without the reaper it would
	// remain a zombie forever under the subreaper helper, since nothing else waits on it.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat("/proc/" + strconv.Itoa(pid)); os.IsNotExist(err) {
			return // reaped
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("grandchild pid %d was never reaped (still present in /proc)", pid)
}
