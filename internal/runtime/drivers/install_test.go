package drivers

import (
	"context"
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
