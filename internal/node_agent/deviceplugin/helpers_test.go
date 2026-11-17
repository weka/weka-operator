package deviceplugin

import (
	"os"
	"testing"
	"time"
)

// shortTempDir returns a freshly created temp directory with a short path, suitable for
// unix domain sockets. sockaddr_un.sun_path is limited to ~104 bytes on macOS (108 on
// Linux), which t.TempDir() routinely exceeds since it nests under the full (often long)
// test name.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "dp")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return dir
}

// waitUntil polls cond every 20ms and fails the test if it is not true within 5s.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !cond() {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
