package deviceplugin

import (
	"os"
	"testing"
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
