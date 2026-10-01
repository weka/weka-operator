package shutdown

import "testing"

func TestStopPolicy(t *testing.T) {
	cases := []struct {
		mode         string
		wantApproval bool
		wantForce    bool
	}{
		{"compute", true, false},
		{"drive", true, false},
		{"s3", true, false},
		{"nfs", true, false},
		{"smbw", true, false},
		{"client", true, true},
		{"data-services", false, true},
		{"envoy", false, true},
		{"telemetry", false, true},
		{"ssdproxy", false, true},
		{"drivers-dist", false, true},
		{"adhoc-op-with-container", false, true},
		{"discovery", false, false},
		{"drivers-loader", false, false},
		{"drivers-builder", false, false},
		{"adhoc-op", false, false},
	}
	if len(cases) != 16 {
		t.Fatalf("test covers %d modes, want all 16", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			approval, force := StopPolicy(tc.mode)
			if approval != tc.wantApproval || force != tc.wantForce {
				t.Fatalf("StopPolicy(%q) = (%v, %v), want (%v, %v)", tc.mode, approval, force, tc.wantApproval, tc.wantForce)
			}
		})
	}
}
