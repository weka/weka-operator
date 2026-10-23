package weka

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/process"
)

func lastCallScript(t *testing.T, r *stubRunner) string {
	t.Helper()
	if len(r.calls) == 0 {
		t.Fatal("no commands were run")
	}
	c := r.calls[len(r.calls)-1]
	if len(c.Args) < 2 {
		t.Fatalf("unexpected command shape: %+v", c)
	}
	return c.Args[1]
}

func TestConfigureTracesModeRouting(t *testing.T) {
	tests := []struct {
		name     string
		in       TracesInput
		wantDest string
	}{
		{
			name:     "override mode writes to old full location",
			in:       TracesInput{Mode: config.Traces{DumperConfigMode: "override", MaxCapacityGB: 10, EnsureFreeSpaceGB: 20}},
			wantDest: "/data/reserved_space/dumper_config.json.override",
		},
		{
			name:     "partial-override without feature flag writes to legacy location",
			in:       TracesInput{Mode: config.Traces{DumperConfigMode: "partial-override", MaxCapacityGB: 10, EnsureFreeSpaceGB: 20}},
			wantDest: "/data/reserved_space/dumper_config_overrides.json",
		},
		{
			name: "partial-override with feature flag writes under /traces",
			in: TracesInput{
				Mode:     config.Traces{DumperConfigMode: "partial-override", MaxCapacityGB: 10, EnsureFreeSpaceGB: 20},
				Features: domain.FeatureFlags{TracesOverrideInSlashTraces: true},
			},
			wantDest: "/traces/config_overrides.json",
		},
		{
			name:     "auto mode with partial support falls back to partial-override",
			in:       TracesInput{Mode: config.Traces{DumperConfigMode: "auto"}, Features: domain.FeatureFlags{TracesOverridePartialSupport: true}},
			wantDest: "/data/reserved_space/dumper_config_overrides.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &stubRunner{}
			if err := ConfigureTraces(context.Background(), r, tt.in, "envoy"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			script := lastCallScript(t, r)
			if !strings.Contains(script, tt.wantDest) {
				t.Errorf("script does not contain expected destination %q:\n%s", tt.wantDest, script)
			}
		})
	}
}

func TestConfigureTracesClusterModeRemovesOverrides(t *testing.T) {
	r := &stubRunner{}
	in := TracesInput{Mode: config.Traces{DumperConfigMode: "cluster"}}
	if err := ConfigureTraces(context.Background(), r, in, "envoy"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	script := lastCallScript(t, r)
	if !strings.Contains(script, "rm -f") {
		t.Errorf("expected cluster mode to remove override files, got: %s", script)
	}
}

func TestConfigureTracesSSDProxyWritesExtraConfig(t *testing.T) {
	r := &stubRunner{}
	in := TracesInput{Mode: config.Traces{DumperConfigMode: "cluster"}, SSDProxy: true}
	if err := ConfigureTraces(context.Background(), r, in, "ssdproxy"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("expected cluster-mode cleanup + ssdproxy config write, got %d calls", len(r.calls))
	}
	script := lastCallScript(t, r)
	if !strings.Contains(script, "/traces/config.json") {
		t.Errorf("expected ssdproxy config write to /traces/config.json, got: %s", script)
	}
}

func TestConfigureTracesInvalidModeErrors(t *testing.T) {
	r := &stubRunner{}
	in := TracesInput{Mode: config.Traces{DumperConfigMode: "bogus"}}
	if err := ConfigureTraces(context.Background(), r, in, "envoy"); err == nil {
		t.Error("expected an error for an invalid DUMPER_CONFIG_MODE")
	}
}

// TestCleanupTracesAndStopDumper_ContextCancelled asserts the poll loop gives up promptly
// on cancellation instead of hanging when supervisorctl never reports RUNNING.
func TestCleanupTracesAndStopDumper_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		CleanupTracesAndStopDumper(ctx, &stubRunner{}, clock.System, "dist", t.TempDir())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("CleanupTracesAndStopDumper: did not return after context cancellation")
	}
}

// TestCleanupTracesAndStopDumper_RunningDespiteNonZeroExit: supervisorctl status exits 3 when
// some programs are STOPPED; any RUNNING line must still end the wait and stop the dumper.
func TestCleanupTracesAndStopDumper_RunningDespiteNonZeroExit(t *testing.T) {
	r := &stubRunner{
		results: []process.Result{{Stdout: []byte("weka-io:weka-io-1  STOPPED\nweka-trace-dumper  RUNNING\n"), ExitCode: 3}, {}},
		errs:    []error{&process.ExecError{Kind: process.FailureExit, Err: errors.New("exit status 3")}, nil},
	}
	CleanupTracesAndStopDumper(context.Background(), r, clock.System, "dist", t.TempDir())
	if len(r.calls) != 2 || !strings.Contains(strings.Join(r.calls[1].Args, " "), "supervisorctl stop weka-trace-dumper") {
		t.Fatalf("calls = %+v, want status then stop", r.calls)
	}
}
