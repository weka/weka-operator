package v1alpha1_test

import (
	"testing"

	"github.com/weka/weka-k8s-api/api/v1alpha1"
)

func intPtr(i int) *int {
	return &i
}

func TestWekaCluster_GetNumaForRole(t *testing.T) {
	tests := []struct {
		name    string
		cluster v1alpha1.WekaCluster
		role    string
		want    *v1alpha1.WekaNuma
	}{
		{
			name:    "all nil returns nil",
			cluster: v1alpha1.WekaCluster{},
			role:    "compute",
			want:    nil,
		},
		{
			name: "global only, no role override",
			cluster: v1alpha1.WekaCluster{
				Spec: v1alpha1.WekaClusterSpec{
					Numa: &v1alpha1.WekaNuma{
						Single: true,
						Region: intPtr(0),
						Method: v1alpha1.WekaNumaMethodDevicePlugin,
					},
				},
			},
			role: "drive",
			want: &v1alpha1.WekaNuma{
				Single: true,
				Region: intPtr(0),
				Method: v1alpha1.WekaNumaMethodDevicePlugin,
			},
		},
		{
			name: "role override wins over global",
			cluster: v1alpha1.WekaCluster{
				Spec: v1alpha1.WekaClusterSpec{
					Numa: &v1alpha1.WekaNuma{Region: intPtr(0)},
					RoleNuma: v1alpha1.RoleNumaSelector{
						Compute: &v1alpha1.WekaNuma{Region: intPtr(1)},
					},
				},
			},
			role: "compute",
			want: &v1alpha1.WekaNuma{Region: intPtr(1)},
		},
		{
			name: "role without override falls back to global",
			cluster: v1alpha1.WekaCluster{
				Spec: v1alpha1.WekaClusterSpec{
					Numa: &v1alpha1.WekaNuma{Region: intPtr(2)},
					RoleNuma: v1alpha1.RoleNumaSelector{
						Compute: &v1alpha1.WekaNuma{Region: intPtr(1)},
					},
				},
			},
			role: "drive",
			want: &v1alpha1.WekaNuma{Region: intPtr(2)},
		},
		{
			name: "unknown role falls back to global",
			cluster: v1alpha1.WekaCluster{
				Spec: v1alpha1.WekaClusterSpec{
					Numa: &v1alpha1.WekaNuma{Region: intPtr(3)},
					RoleNuma: v1alpha1.RoleNumaSelector{
						Compute: &v1alpha1.WekaNuma{Region: intPtr(1)},
					},
				},
			},
			role: "unknown-role",
			want: &v1alpha1.WekaNuma{Region: intPtr(3)},
		},
		{
			name: "nfs role resolves correctly",
			cluster: v1alpha1.WekaCluster{
				Spec: v1alpha1.WekaClusterSpec{
					RoleNuma: v1alpha1.RoleNumaSelector{
						Nfs: &v1alpha1.WekaNuma{Region: intPtr(4)},
					},
				},
			},
			role: "nfs",
			want: &v1alpha1.WekaNuma{Region: intPtr(4)},
		},
		{
			name: "s3 role resolves correctly",
			cluster: v1alpha1.WekaCluster{
				Spec: v1alpha1.WekaClusterSpec{
					RoleNuma: v1alpha1.RoleNumaSelector{
						S3: &v1alpha1.WekaNuma{Region: intPtr(5)},
					},
				},
			},
			role: "s3",
			want: &v1alpha1.WekaNuma{Region: intPtr(5)},
		},
		{
			name: "smbw role resolves correctly",
			cluster: v1alpha1.WekaCluster{
				Spec: v1alpha1.WekaClusterSpec{
					RoleNuma: v1alpha1.RoleNumaSelector{
						Smbw: &v1alpha1.WekaNuma{Region: intPtr(6)},
					},
				},
			},
			role: "smbw",
			want: &v1alpha1.WekaNuma{Region: intPtr(6)},
		},
		{
			name: "data-services role resolves correctly",
			cluster: v1alpha1.WekaCluster{
				Spec: v1alpha1.WekaClusterSpec{
					RoleNuma: v1alpha1.RoleNumaSelector{
						DataServices: &v1alpha1.WekaNuma{Region: intPtr(7)},
					},
				},
			},
			role: "data-services",
			want: &v1alpha1.WekaNuma{Region: intPtr(7)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cluster.GetNumaForRole(tt.role)
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("GetNumaForRole() = %v, want %v", got, tt.want)
			}
			if got == nil {
				return
			}
			if got.Single != tt.want.Single {
				t.Errorf("GetNumaForRole().Single = %v, want %v", got.Single, tt.want.Single)
			}
			if got.Method != tt.want.Method {
				t.Errorf("GetNumaForRole().Method = %v, want %v", got.Method, tt.want.Method)
			}
			if (got.Region == nil) != (tt.want.Region == nil) {
				t.Fatalf("GetNumaForRole().Region = %v, want %v", got.Region, tt.want.Region)
			}
			if got.Region != nil && *got.Region != *tt.want.Region {
				t.Errorf("GetNumaForRole().Region = %v, want %v", *got.Region, *tt.want.Region)
			}
		})
	}
}
