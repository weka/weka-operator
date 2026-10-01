package syslog

import "testing"

// TestCommand verifies the go-syslog/syslog-ng selection and exact args per pkg.
func TestCommand(t *testing.T) {
	cases := []struct {
		pkg      string
		wantPath string
	}{
		{"go-syslog", "/usr/sbin/go-syslog"},
		{"syslog-ng", "/usr/sbin/syslog-ng"},
	}
	for _, tc := range cases {
		t.Run(tc.pkg, func(t *testing.T) {
			cmd, err := Command(tc.pkg)
			if err != nil {
				t.Fatalf("Command(%q) error = %v", tc.pkg, err)
			}
			if cmd.Path != tc.wantPath {
				t.Errorf("Command(%q).Path = %q, want %q", tc.pkg, cmd.Path, tc.wantPath)
			}
		})
	}

	cmd, err := Command("syslog-ng")
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	wantArgs := []string{"-F", "-f", "/etc/syslog-ng/syslog-ng.conf", "--pidfile", "/var/run/syslog-ng.pid"}
	if len(cmd.Args) != len(wantArgs) {
		t.Fatalf("Args = %v, want %v", cmd.Args, wantArgs)
	}
	for i := range wantArgs {
		if cmd.Args[i] != wantArgs[i] {
			t.Errorf("Args[%d] = %q, want %q", i, cmd.Args[i], wantArgs[i])
		}
	}
}
