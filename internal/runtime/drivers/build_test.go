package drivers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/weka/weka-operator/internal/pkg/osinfo"
)

// ---- KernelSignature tests ----

func TestKernelSignature(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(dir string) // creates files inside dir
		want     string
		wantErr  bool
		nonExist bool // pass a non-existent dir entirely
	}{
		{
			name: "valid weka-driver zip returns signature",
			setup: func(dir string) {
				name := "weka-driver-abc123def456-deadbeefcafe.zip"
				_ = os.WriteFile(filepath.Join(dir, name), []byte(""), 0644)
			},
			want: "deadbeefcafe",
		},
		{
			name: "dir with no matching zip returns error",
			setup: func(dir string) {
				_ = os.WriteFile(filepath.Join(dir, "some-other-file.txt"), []byte(""), 0644)
			},
			wantErr: true,
		},
		{
			name: "malformed zip name (no signature group) returns error",
			setup: func(dir string) {
				// File that starts with weka-driver but has only one hex segment (no sig).
				_ = os.WriteFile(filepath.Join(dir, "weka-driver-abc123.zip"), []byte(""), 0644)
			},
			wantErr: true,
		},
		{
			name:     "non-existent dir returns error",
			nonExist: true,
			wantErr:  true,
		},
		{
			name: "empty dir returns error",
			setup: func(dir string) {
				// nothing created
			},
			wantErr: true,
		},
		{
			name: "correct file alongside irrelevant files is found",
			setup: func(dir string) {
				_ = os.WriteFile(filepath.Join(dir, "unrelated.tar.gz"), []byte(""), 0644)
				_ = os.WriteFile(filepath.Join(dir, "weka-driver-ff00aa11bb22-cc33dd44.zip"), []byte(""), 0644)
			},
			want: "cc33dd44",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dir string
			if tt.nonExist {
				dir = filepath.Join(t.TempDir(), "nonexistent-subdir")
			} else {
				dir = t.TempDir()
				if tt.setup != nil {
					tt.setup(dir)
				}
			}

			got, err := KernelSignature(dir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("KernelSignature() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("KernelSignature() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---- isUbuntu24 tests ----

func TestIsUbuntu24(t *testing.T) {
	tests := []struct {
		name     string
		nodeInfo *osinfo.NodeInfo
		want     bool
	}{
		{
			name:     "Ubuntu OsBuildId 24.04 → true",
			nodeInfo: &osinfo.NodeInfo{Os: osinfo.OsNameUbuntu, OsBuildId: "24.04"},
			want:     true,
		},
		{
			name:     "Ubuntu OsBuildId 22.04 → false",
			nodeInfo: &osinfo.NodeInfo{Os: osinfo.OsNameUbuntu, OsBuildId: "22.04"},
			want:     false,
		},
		{
			name:     "Ubuntu OsBuildId 24 (no dot) → true",
			nodeInfo: &osinfo.NodeInfo{Os: osinfo.OsNameUbuntu, OsBuildId: "24"},
			want:     true,
		},
		{
			name:     "Ubuntu OsBuildId 20.04 → false",
			nodeInfo: &osinfo.NodeInfo{Os: osinfo.OsNameUbuntu, OsBuildId: "20.04"},
			want:     false,
		},
		{
			name:     "non-Ubuntu (cos) → false regardless of build id",
			nodeInfo: &osinfo.NodeInfo{Os: osinfo.OsNameCos, OsBuildId: "24.04"},
			want:     false,
		},
		{
			name:     "non-Ubuntu (rhcos) → false",
			nodeInfo: &osinfo.NodeInfo{Os: osinfo.OsNameRhCos, OsBuildId: "24.04"},
			want:     false,
		},
		{
			name:     "Ubuntu non-numeric major → false",
			nodeInfo: &osinfo.NodeInfo{Os: osinfo.OsNameUbuntu, OsBuildId: "focal.04"},
			want:     false,
		},
		{
			name:     "Ubuntu OsBuildId 26.04 → true",
			nodeInfo: &osinfo.NodeInfo{Os: osinfo.OsNameUbuntu, OsBuildId: "26.04"},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isUbuntu24(tt.nodeInfo)
			if got != tt.want {
				t.Errorf("isUbuntu24(%+v) = %v, want %v", tt.nodeInfo, got, tt.want)
			}
		})
	}
}

// ---- WekaDriversHandling tests ----

func TestWekaDriversHandling(t *testing.T) {
	tests := []struct {
		name      string
		imageName string
		want      bool
	}{
		{
			name:      "unknown tag → default params → true",
			imageName: "quay.io/weka/weka:99.99.99-unknown",
			want:      true,
		},
		{
			name:      "known tag 4.3.3 → explicit map entry → false",
			imageName: "quay.io/weka/weka:4.3.3",
			want:      false,
		},
		{
			name:      "known 4.2.10-k8so.0 entry → false",
			imageName: "quay.io/weka/weka:4.2.10-k8so.0",
			want:      false,
		},
		{
			name:      "s3multitenancy override → false (WekaDriversHandling absent in override dict)",
			imageName: "quay.io/weka/weka:4.2.7.64-s3multitenancy.3",
			want:      false,
		},
		{
			name:      "empty image name → unknown tag → default params → true",
			imageName: "",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WekaDriversHandling(tt.imageName)
			if got != tt.want {
				t.Errorf("WekaDriversHandling(%q) = %v, want %v", tt.imageName, got, tt.want)
			}
		})
	}
}
