package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// TestAgentPortScript pins the [agent]-section-scoped port rewrite and its
// explicit verification (a silent no-op rewrite must fail loudly rather than
// leaving
// port=0 in place). agentPortScript targets the GNU sed shipped in the runtime
// container image, so this only runs on Linux.
func TestAgentPortScript(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("agentPortScript requires GNU sed, only available in the Linux runtime container")
	}
	tests := []struct {
		name    string
		initial string
		port    int
		wantErr bool
		wantOut string // expected port line under [agent] after the script runs
	}{
		{
			name:    "rewrites existing port under [agent] only",
			initial: "[os]\nport=9999\n[agent]\nport=14100\nother=x\n[net]\nport=7\n",
			port:    5000,
			wantOut: "port=5000",
		},
		{
			name:    "fails verification when [agent] section is missing",
			initial: "[os]\nport=9999\n[net]\nport=7\n",
			port:    5000,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			conf := filepath.Join(dir, "service.conf")
			if err := os.WriteFile(conf, []byte(tt.initial), 0o644); err != nil {
				t.Fatal(err)
			}

			scriptTemplate := strings.ReplaceAll(agentPortScript, "/etc/wekaio/service.conf", conf)
			script := fmt.Sprintf(scriptTemplate, tt.port, tt.port)
			err := exec.Command("sh", "-c", script).Run()

			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			got, err := os.ReadFile(conf)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), tt.wantOut) {
				t.Errorf("service.conf = %q, want to contain %q", got, tt.wantOut)
			}
		})
	}
}

// fakeRunner records issued commands and always reports success.
type fakeRunner struct {
	commands []process.Command
}

func (f *fakeRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.commands = append(f.commands, c)
	return process.Result{}, nil
}

// TestEnsureDrivers_ComputeModeChecksLsmod verifies the compute/drive path issues the
// legacy-mode probe followed by one lsmod check per required driver, all via the runner.
func TestEnsureDrivers_ComputeModeChecksLsmod(t *testing.T) {
	runner := &fakeRunner{}

	if err := EnsureDrivers(context.Background(), runner, clock.System, "compute", "", "/opt/weka"); err != nil {
		t.Fatalf("EnsureDrivers() error = %v", err)
	}

	if len(runner.commands) == 0 {
		t.Fatal("expected at least one command, got none")
	}
	first := runner.commands[0]
	if first.Path != "sh" || !strings.Contains(first.Args[1], "weka driver --help | grep pack") {
		t.Errorf("first command = %+v, want the legacy-mode probe", first)
	}
	for _, cmd := range runner.commands[1:] {
		if cmd.Path != "sh" || !strings.Contains(cmd.Args[1], "lsmod | grep -w") {
			t.Errorf("command = %+v, want an lsmod check", cmd)
		}
	}
}

// TestOverrideDependenciesFlag_NewDriverHandling verifies the WekaDriversHandling branch
// writes the skip marker via the runner instead of a per-kernel-version directory.
func TestOverrideDependenciesFlag_NewDriverHandling(t *testing.T) {
	runner := &fakeRunner{}

	if err := OverrideDependenciesFlag(context.Background(), runner, ""); err != nil {
		t.Fatalf("OverrideDependenciesFlag() error = %v", err)
	}

	if len(runner.commands) != 1 {
		t.Fatalf("got %d commands, want 1", len(runner.commands))
	}
	cmd := runner.commands[0]
	if cmd.Path != "sh" || !strings.Contains(cmd.Args[1], "touch /opt/weka/data/dependencies/skip") {
		t.Errorf("command = %+v, want the dependencies-skip marker script", cmd)
	}
}
