package v1alpha1_test

import (
	"testing"

	"github.com/weka/weka-k8s-api/api/v1alpha1"
)

func intPtr(i int) *int {
	return &i
}

func TestWekaClusterNuma_RegionForRole(t *testing.T) {
	tests := []struct {
		name string
		numa *v1alpha1.WekaClusterNuma
		role string
		want *int
	}{
		{
			name: "nil receiver returns nil",
			numa: nil,
			role: "compute",
			want: nil,
		},
		{
			name: "nil region returns nil",
			numa: &v1alpha1.WekaClusterNuma{},
			role: "compute",
			want: nil,
		},
		{
			name: "role-specific override wins over All",
			numa: &v1alpha1.WekaClusterNuma{
				Region: &v1alpha1.WekaClusterNumaRegion{
					All:     intPtr(0),
					Compute: intPtr(1),
				},
			},
			role: "compute",
			want: intPtr(1),
		},
		{
			name: "falls back to All when role-specific not set",
			numa: &v1alpha1.WekaClusterNuma{
				Region: &v1alpha1.WekaClusterNumaRegion{
					All: intPtr(2),
				},
			},
			role: "drive",
			want: intPtr(2),
		},
		{
			name: "no All and no role-specific returns nil",
			numa: &v1alpha1.WekaClusterNuma{
				Region: &v1alpha1.WekaClusterNumaRegion{
					Compute: intPtr(1),
				},
			},
			role: "drive",
			want: nil,
		},
		{
			name: "unknown role falls back to All",
			numa: &v1alpha1.WekaClusterNuma{
				Region: &v1alpha1.WekaClusterNumaRegion{
					All: intPtr(3),
				},
			},
			role: "unknown-role",
			want: intPtr(3),
		},
		{
			name: "nfs role resolves correctly",
			numa: &v1alpha1.WekaClusterNuma{
				Region: &v1alpha1.WekaClusterNumaRegion{
					All: intPtr(0),
					Nfs: intPtr(4),
				},
			},
			role: "nfs",
			want: intPtr(4),
		},
		{
			name: "s3 role resolves correctly",
			numa: &v1alpha1.WekaClusterNuma{
				Region: &v1alpha1.WekaClusterNumaRegion{
					All: intPtr(0),
					S3:  intPtr(5),
				},
			},
			role: "s3",
			want: intPtr(5),
		},
		{
			name: "smbw role resolves correctly",
			numa: &v1alpha1.WekaClusterNuma{
				Region: &v1alpha1.WekaClusterNumaRegion{
					All:  intPtr(0),
					Smbw: intPtr(6),
				},
			},
			role: "smbw",
			want: intPtr(6),
		},
		{
			name: "data-services role resolves correctly",
			numa: &v1alpha1.WekaClusterNuma{
				Region: &v1alpha1.WekaClusterNumaRegion{
					All:          intPtr(0),
					DataServices: intPtr(7),
				},
			},
			role: "data-services",
			want: intPtr(7),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.numa.RegionForRole(tt.role)
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("RegionForRole() = %v, want %v", got, tt.want)
			}
			if got != nil && *got != *tt.want {
				t.Fatalf("RegionForRole() = %v, want %v", *got, *tt.want)
			}
		})
	}
}

func TestWekaClusterNuma_NumaForRole(t *testing.T) {
	tests := []struct {
		name string
		numa *v1alpha1.WekaClusterNuma
		role string
		want *v1alpha1.WekaNuma
	}{
		{
			name: "nil receiver returns nil",
			numa: nil,
			role: "compute",
			want: nil,
		},
		{
			name: "no resolvable region returns nil",
			numa: &v1alpha1.WekaClusterNuma{},
			role: "compute",
			want: nil,
		},
		{
			name: "resolves role-specific region with single and method",
			numa: &v1alpha1.WekaClusterNuma{
				Single: true,
				Method: v1alpha1.WekaNumaMethodDevicePlugin,
				Region: &v1alpha1.WekaClusterNumaRegion{
					All:     intPtr(0),
					Compute: intPtr(1),
				},
			},
			role: "compute",
			want: &v1alpha1.WekaNuma{
				Single: true,
				Region: intPtr(1),
				Method: v1alpha1.WekaNumaMethodDevicePlugin,
			},
		},
		{
			name: "falls back to All region",
			numa: &v1alpha1.WekaClusterNuma{
				Single: false,
				Region: &v1alpha1.WekaClusterNumaRegion{
					All: intPtr(2),
				},
			},
			role: "drive",
			want: &v1alpha1.WekaNuma{
				Single: false,
				Region: intPtr(2),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.numa.NumaForRole(tt.role)
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("NumaForRole() = %v, want %v", got, tt.want)
			}
			if got == nil {
				return
			}
			if got.Single != tt.want.Single {
				t.Errorf("NumaForRole().Single = %v, want %v", got.Single, tt.want.Single)
			}
			if got.Method != tt.want.Method {
				t.Errorf("NumaForRole().Method = %v, want %v", got.Method, tt.want.Method)
			}
			if (got.Region == nil) != (tt.want.Region == nil) {
				t.Fatalf("NumaForRole().Region = %v, want %v", got.Region, tt.want.Region)
			}
			if got.Region != nil && *got.Region != *tt.want.Region {
				t.Errorf("NumaForRole().Region = %v, want %v", *got.Region, *tt.want.Region)
			}
		})
	}
}
