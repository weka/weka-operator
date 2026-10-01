package drivers

import "testing"

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
