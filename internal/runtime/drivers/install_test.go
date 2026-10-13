package drivers

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestBuildWekaDriverArgs(t *testing.T) {
	cases := []struct {
		name                                        string
		subcmd, distService, version, kernelBuildID string
		want                                        string
	}{
		{
			name:   "download with dist service and kernel build id",
			subcmd: "download", distService: "http://dist", version: "4.4.0", kernelBuildID: "ubuntu24.04",
			want: "weka driver download --from 'http://dist' --without-agent --version 4.4.0 --kernel-build-id ubuntu24.04",
		},
		{
			name:   "install without dist service or kernel build id",
			subcmd: "install", distService: "", version: "4.4.0", kernelBuildID: "",
			want: "weka driver install --without-agent --version 4.4.0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildWekaDriverArgs(tc.subcmd, tc.distService, tc.version, tc.kernelBuildID)
			if got != tc.want {
				t.Fatalf("buildWekaDriverArgs() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLoadDriversLegacyUsesExactFileNames(t *testing.T) {
	runner := &fakeRunner{}
	in := &LoadInput{
		ImageName:   "quay.io/weka/weka-in-container:4.2.7.64-k8so-beta.10",
		DistService: "http://dist",
		OptWeka:     t.TempDir(),
	}
	if err := loadDriversLegacy(context.Background(), runner, in); err != nil {
		t.Fatal(err)
	}

	const kernel = "-$(uname -r).$(uname -m).ko"
	wekafs := ResolveVersionParams(in.ImageName).Wekafs
	for _, file := range []string{
		"weka_driver-wekafsgw-" + wekafs + kernel,
		"weka_driver-wekafsio-" + wekafs + kernel,
		"mpin_user-" + MpinUserDriverVersion + kernel,
	} {
		want := "curl -kfo " + in.OptWeka + "/dist/drivers/" + file + " http://dist/dist/v1/drivers/" + file
		found := false
		for _, c := range runner.commands {
			found = found || strings.HasSuffix(c.Args[len(c.Args)-1], want)
		}
		if !found {
			t.Errorf("no download command %q in %v", want, runner.commands)
		}
	}
	for _, c := range runner.commands {
		if script := c.Args[len(c.Args)-1]; strings.Contains(script, "*") {
			t.Errorf("wildcard in command %q", script)
		}
	}
}

func TestLoadDriversNewAlwaysRunsVersionGet(t *testing.T) {
	for name, tc := range map[string]struct{ target, want string }{
		"same image":      {"", "weka version get --without-agent --driver-only 4.4.0"},
		"different image": {"other:img", "weka version get --without-agent --driver-only --from file://shared-weka-version/opt-weka 4.4.0"},
	} {
		t.Run(name, func(t *testing.T) {
			optWeka := t.TempDir()
			if err := os.MkdirAll(optWeka+"/dist/release", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(optWeka+"/dist/release/4.4.0.spec", nil, 0o644); err != nil {
				t.Fatal(err)
			}
			runner := &fakeRunner{}
			in := &LoadInput{ImageName: "img", TargetImageName: tc.target, DistService: "http://dist", BuildID: "ubuntu24.04", OptWeka: optWeka}
			if err := loadDriversNew(context.Background(), runner, in); err != nil {
				t.Fatal(err)
			}
			first := runner.commands[0]
			if got := strings.TrimPrefix(first.Args[len(first.Args)-1], "set -e\n"); got != tc.want {
				t.Errorf("first command = %q, want %q", got, tc.want)
			}
		})
	}
}
