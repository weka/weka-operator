package osinfo

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseOsRelease(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantKeys    map[string]string
		wantMissing []string
		wantErr     bool
	}{
		{
			name:    "ubuntu unquoted",
			content: "ID=ubuntu\nVERSION_ID=22.04\n",
			wantKeys: map[string]string{
				"ID":         "ubuntu",
				"VERSION_ID": "22.04",
			},
		},
		{
			name:    "ubuntu quoted values",
			content: `ID="ubuntu"` + "\n" + `VERSION_ID="22.04"` + "\n",
			wantKeys: map[string]string{
				"ID":         "ubuntu",
				"VERSION_ID": "22.04",
			},
		},
		{
			name:    "cos with BUILD_ID",
			content: "ID=cos\nBUILD_ID=12345\n",
			wantKeys: map[string]string{
				"ID":       "cos",
				"BUILD_ID": "12345",
			},
		},
		{
			name:    "rhcos with quoted VERSION",
			content: "ID=rhcos\nVERSION=\"413.92.202309\"\n",
			wantKeys: map[string]string{
				"ID":      "rhcos",
				"VERSION": "413.92.202309",
			},
		},
		{
			name:        "lines without = are skipped",
			content:     "# comment\nNAME=Ubuntu\nthis-has-no-equals\n",
			wantKeys:    map[string]string{"NAME": "Ubuntu"},
			wantMissing: []string{"this-has-no-equals"},
		},
		{
			name:        "empty value after = is not stored",
			content:     "ID=ubuntu\nBUILD_ID=\n",
			wantKeys:    map[string]string{"ID": "ubuntu"},
			wantMissing: []string{"BUILD_ID"},
		},
		{
			name:    "missing file returns error",
			content: "", // file won't be created
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var path string
			if tt.wantErr {
				path = filepath.Join(t.TempDir(), "nonexistent", "os-release")
			} else {
				dir := t.TempDir()
				path = filepath.Join(dir, "os-release")
				if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
					t.Fatalf("write test file: %v", err)
				}
			}

			got, err := parseOsRelease(path)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			for k, want := range tt.wantKeys {
				if got[k] != want {
					t.Errorf("key %q: got %q, want %q", k, got[k], want)
				}
			}
			for _, k := range tt.wantMissing {
				if v, ok := got[k]; ok {
					t.Errorf("key %q should be absent, got %q", k, v)
				}
			}
		})
	}
}

func TestParseThreadSiblingsList(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "single cpu no HT",
			input: "0",
			want:  []string{"0"},
		},
		{
			name:  "range format two siblings",
			input: "0-1",
			want:  []string{"0", "1"},
		},
		{
			name:  "comma-separated list",
			input: "0,1,2,3",
			want:  []string{"0", "1", "2", "3"},
		},
		{
			name:  "empty string returns nil",
			input: "",
			want:  nil,
		},
		{
			name:  "mixed range and comma",
			input: "0-3,8-11",
			want:  []string{"0", "3", "8", "11"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseThreadSiblingsList(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("length: got %v (%d), want %v (%d)", got, len(got), tt.want, len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("[%d]: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestNodeInfo_Methods(t *testing.T) {
	tests := []struct {
		os       string
		isRhCos  bool
		isCos    bool
		isUbuntu bool
	}{
		{os: OsNameRhCos, isRhCos: true},
		{os: OsNameCos, isCos: true},
		{os: OsNameUbuntu, isUbuntu: true},
		{os: "unknown"},
	}

	for _, tt := range tests {
		n := &NodeInfo{Os: tt.os}
		if n.IsRhCos() != tt.isRhCos {
			t.Errorf("os=%q IsRhCos: got %v, want %v", tt.os, n.IsRhCos(), tt.isRhCos)
		}
		if n.IsCos() != tt.isCos {
			t.Errorf("os=%q IsCos: got %v, want %v", tt.os, n.IsCos(), tt.isCos)
		}
		if n.IsUbuntu() != tt.isUbuntu {
			t.Errorf("os=%q IsUbuntu: got %v, want %v", tt.os, n.IsUbuntu(), tt.isUbuntu)
		}
	}
}

func TestParseGccMajor(t *testing.T) {
	tests := []struct {
		name, in, want string
		wantErr        bool
	}{
		{name: "gcc (GCC) form", in: "Linux version 6.18.52 (nixbld@localhost) (gcc (GCC) 15.2.0, GNU ld (GNU Binutils) 2.44) #1 SMP", want: "15"},
		{name: "gcc version form", in: "Linux version 6.1.0 (builder) (gcc version 14.2.0 (Debian 14.2.0-1)) #1 SMP", want: "14"},
		{name: "unparsable", in: "Linux version 6.1.0 (clang version 18.1.0)", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseGccMajor(tt.in)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("ParseGccMajor() = %q, %v; want %q, wantErr=%v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestNodeInfo_IsNixos(t *testing.T) {
	if !(&NodeInfo{Os: "nixos-gcc15"}).IsNixos() || !(&NodeInfo{Os: "nixos"}).IsNixos() {
		t.Error("nixos prefixes should be nixos")
	}
	if (&NodeInfo{Os: OsNameUbuntu}).IsNixos() {
		t.Error("ubuntu is not nixos")
	}
}

func TestLoadNixos(t *testing.T) {
	dir := t.TempDir()
	osRelease := filepath.Join(dir, "os-release")
	procVersion := filepath.Join(dir, "version")
	if err := os.WriteFile(osRelease, []byte("ID=nixos\nVERSION_ID=\"25.11\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(procVersion, []byte("Linux version 6.18.52 (nixbld@localhost) (gcc (GCC) 15.2.0) #1 SMP\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldOS, oldProc := osReleasePath, procVersionPath
	osReleasePath, procVersionPath = osRelease, procVersion
	t.Cleanup(func() { osReleasePath, procVersionPath = oldOS, oldProc })

	info, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if info.Os != "nixos-gcc15" || info.OsBuildId != "nixos-gcc15" || info.KubernetesDistro != KubeDistroK8s {
		t.Errorf("info = %+v", info)
	}

	if err := os.WriteFile(procVersion, []byte("no compiler here"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := load(); err == nil {
		t.Error("want error when gcc major is unparsable")
	}
}

func TestHostNsenterArgs(t *testing.T) {
	plain := []string{"--mount", "--pid", "--target", "1", "--", "lsblk", "-p"}
	for _, n := range []*NodeInfo{nil, {Os: OsNameUbuntu}} {
		if got := n.HostNsenterArgs("lsblk", "-p"); !reflect.DeepEqual(got, plain) {
			t.Errorf("non-nixos args = %v, want %v", got, plain)
		}
	}
	want := []string{"--mount", "--pid", "--target", "1", "--", "/usr/bin/env",
		"PATH=/run/current-system/sw/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "lsblk", "-p"}
	if got := (&NodeInfo{Os: "nixos-gcc15"}).HostNsenterArgs("lsblk", "-p"); !reflect.DeepEqual(got, want) {
		t.Errorf("nixos args = %v, want %v", got, want)
	}
}
