package logrotate

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/process"
	"github.com/weka/weka-operator/internal/runtime/syslog"
)

func TestApplies(t *testing.T) {
	goSyslogPresent := syslog.UseGoSyslog("") // whichever this test host resolves "auto" to

	cases := []struct {
		mode, pkg string
		want      bool
	}{
		{"compute", "syslog-ng", true},
		{"client", "syslog-ng", true},
		{"adhoc-op", "syslog-ng", false},
		{"compute", "go-syslog", false},
		{"adhoc-op", "go-syslog", false},
		{"compute", "auto", !goSyslogPresent},
	}
	for _, tc := range cases {
		t.Run(tc.mode+"/"+tc.pkg, func(t *testing.T) {
			if got := Applies(tc.mode, tc.pkg); got != tc.want {
				t.Fatalf("Applies(%q, %q) = %v, want %v", tc.mode, tc.pkg, got, tc.want)
			}
		})
	}
}

type fakeRunner struct {
	mu    sync.Mutex
	calls []process.Command
}

func (f *fakeRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	return process.Result{}, nil
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestRotateWritesConfigAndRunsLogrotate(t *testing.T) {
	origPath := logrotateConfigPath
	logrotateConfigPath = filepath.Join(t.TempDir(), "logrotate.conf")
	defer func() { logrotateConfigPath = origPath }()

	runner := &fakeRunner{}
	if err := Rotate(context.Background(), runner); err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	if got := runner.callCount(); got != 1 {
		t.Fatalf("callCount = %d, want 1", got)
	}
	if _, err := os.Stat(logrotateConfigPath); err != nil {
		t.Fatalf("logrotate config not written: %v", err)
	}
}
