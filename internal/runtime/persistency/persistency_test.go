package persistency

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/paths"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// TestConfigureScript_OptWekaBlock verifies that the generated shell script for the
// /host-binds/opt-weka block matches the dist/bin/drivers persistence strategy and the
// shared-netns rbind (Python commits d4a16ac7, 35ad2240, 36e9faf9, ab7144b9, db335106).
func TestConfigureScript_OptWekaBlock(t *testing.T) {
	const persistenceDir = "/host-binds/opt-weka"
	script := buildMountScript(persistenceDir)

	checks := []struct {
		desc    string
		want    string
		present bool
	}{
		{"bind dist to staging dir", "mount -o bind /opt/weka/dist /opt/weka-dist-save", true},
		{"make-private on dist staging dir", "mount --make-private /opt/weka-dist-save", true},
		{"bind bin to staging dir", "mount -o bind /opt/weka/bin /opt/weka-bin-save", true},
		{"make-private on bin staging dir", "mount --make-private /opt/weka-bin-save", true},
		{"restore bin from staging", "mount -o bind /opt/weka-bin-save /opt/weka/bin", true},
		{"umount bin staging dir", "umount /opt/weka-bin-save", true},
		{"mkdir persistence dist/drivers", "mkdir -p " + persistenceDir + "/dist/drivers", true},
		{"stage image drivers into persistence dir", "cp -an /opt/weka/dist/drivers/. " + persistenceDir + "/dist/drivers/", true},
		{"bind persistence drivers to staging dir", "mount -o bind " + persistenceDir + "/dist/drivers /opt/weka-drivers-save", true},
		{"make-private on drivers staging dir", "mount --make-private /opt/weka-drivers-save", true},
		{"bind persistence dir to /opt/weka", "mount -o bind " + persistenceDir + " /opt/weka", true},
		{"restore dist from staging", "mount -o bind /opt/weka-dist-save /opt/weka/dist", true},
		{"umount dist staging dir", "umount /opt/weka-dist-save", true},
		{"restore drivers from staging", "mount -o bind /opt/weka-drivers-save /opt/weka/dist/drivers", true},
		{"umount drivers staging dir", "umount /opt/weka-drivers-save", true},
		{"shared-netns rbind", "mount --rbind /host-binds/shared-netns /opt/weka/external-mounts/shared-netns", true},
		{"shared-netns make-rshared", "mount --make-rshared /opt/weka/external-mounts/shared-netns", true},
		// Old strategy must be absent.
		{"no weka-preinstalled", "weka-preinstalled", false},
		{"no plain bind for shared-netns", "mount -o bind /host-binds/shared-netns", false},
	}

	for _, tc := range checks {
		t.Run(tc.desc, func(t *testing.T) {
			found := strings.Contains(script, tc.want)
			if found != tc.present {
				if tc.present {
					t.Errorf("script missing expected string:\n  %q\nScript:\n%s", tc.want, script)
				} else {
					t.Errorf("script contains unexpected string:\n  %q\nScript:\n%s", tc.want, script)
				}
			}
		})
	}
}

// TestConfigureScript_GlobalPersistence verifies the same properties when using
// global persistence mode (different persistenceDir path).
func TestConfigureScript_GlobalPersistence(t *testing.T) {
	const containerID = "abc123"
	persistenceDir := fmt.Sprintf("/opt/weka-global-persistence/containers/%s", containerID)
	script := buildMountScript(persistenceDir)

	if !strings.Contains(script, "mount --make-private /opt/weka-dist-save") {
		t.Error("global persistence script missing 'mount --make-private /opt/weka-dist-save'")
	}
	if !strings.Contains(script, "umount /opt/weka-dist-save") {
		t.Error("global persistence script missing 'umount /opt/weka-dist-save'")
	}
	if !strings.Contains(script, "cp -an /opt/weka/dist/drivers/. "+persistenceDir+"/dist/drivers/") {
		t.Error("global persistence script missing drivers staging cp")
	}
	if strings.Contains(script, "weka-preinstalled") {
		t.Error("global persistence script must not reference 'weka-preinstalled'")
	}
}

// TestFilterCACertContent covers the per-file cert/key gate: a file is included whole only
// if it contains a certificate and no private key, matching Python's grep -q pair
// (weka_runtime.py:3249-3253).
func TestFilterCACertContent(t *testing.T) {
	const cert = "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"
	const key = "-----BEGIN PRIVATE KEY-----\nBBBB\n-----END PRIVATE KEY-----\n"

	cases := []struct {
		desc      string
		files     []caCertFile
		wantBytes string
	}{
		{"empty input", nil, ""},
		{"single cert file", []caCertFile{{"ca.crt", []byte(cert)}}, cert + "\n"},
		{"cert and key in same file skips whole file", []caCertFile{{"tls.crt", []byte(cert + key)}}, ""},
		{"key-only file skipped", []caCertFile{{"tls.key", []byte(key)}}, ""},
		{"non-cert file skipped", []caCertFile{{"readme.txt", []byte("hello")}}, ""},
		{
			"multiple files concatenated in order",
			[]caCertFile{{"a.crt", []byte(cert)}, {"b.crt", []byte(cert)}},
			cert + "\n" + cert + "\n",
		},
		{
			"mixed: only the clean cert file survives",
			[]caCertFile{{"tls.crt", []byte(cert + key)}, {"ca.crt", []byte(cert)}},
			cert + "\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := string(filterCACertContent(tc.files))
			if got != tc.wantBytes {
				t.Errorf("filterCACertContent() = %q, want %q", got, tc.wantBytes)
			}
		})
	}
}

// fakeRunner records issued commands and returns scripted results, keyed by Path.
type fakeRunner struct {
	commands []process.Command
}

func (f *fakeRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.commands = append(f.commands, c)
	return process.Result{}, nil
}

// TestConfigure_RunsMountAndFinalScripts verifies Configure issues the bind-mount script
// and the final (shared-configs + persistency-configured marker) script via the runner,
// using the given paths.Roots and config.Persistence instead of hardcoded defaults.
func TestConfigure_RunsMountAndFinalScripts(t *testing.T) {
	runner := &fakeRunner{}
	p := paths.Roots{HostBinds: "/host-binds"}
	persistence := config.Persistence{Mode: "local"}

	if err := Configure(context.Background(), runner, persistence, p); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}

	if len(runner.commands) != 2 {
		t.Fatalf("got %d commands, want 2", len(runner.commands))
	}
	for _, cmd := range runner.commands {
		if cmd.Path != "sh" || len(cmd.Args) != 2 || cmd.Args[0] != "-c" {
			t.Errorf("command = %+v, want sh -c <script>", cmd)
		}
	}
	if !strings.Contains(runner.commands[0].Args[1], "/host-binds/opt-weka") {
		t.Errorf("mount script = %q, want it to reference /host-binds/opt-weka", runner.commands[0].Args[1])
	}
	if !strings.Contains(runner.commands[1].Args[1], persistencyConfiguredPath) {
		t.Errorf("final script = %q, want it to touch %q", runner.commands[1].Args[1], persistencyConfiguredPath)
	}
}

// TestConfigure_GlobalPersistenceDir verifies global persistence mode derives the
// persistence directory from the container ID rather than HostBinds.
func TestConfigure_GlobalPersistenceDir(t *testing.T) {
	runner := &fakeRunner{}
	p := paths.Roots{HostBinds: "/host-binds"}
	persistence := config.Persistence{Mode: "global", ContainerID: "abc123"}

	if err := Configure(context.Background(), runner, persistence, p); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}

	want := "/opt/weka-global-persistence/containers/abc123"
	if !strings.Contains(runner.commands[0].Args[1], want) {
		t.Errorf("mount script = %q, want it to reference %q", runner.commands[0].Args[1], want)
	}
}
