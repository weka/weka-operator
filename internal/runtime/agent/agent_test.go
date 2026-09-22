package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAgentPortScript pins the [agent]-section-scoped port rewrite and its
// explicit verification (Go's cmdutil does not run under "set -e" like Python's
// run_command, so a silent no-op rewrite must fail loudly instead of leaving
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
