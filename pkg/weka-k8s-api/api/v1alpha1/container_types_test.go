package v1alpha1_test

import (
	"testing"

	"github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func containerWithIps(ipv6 bool, ips ...string) v1alpha1.WekaContainer {
	return v1alpha1.WekaContainer{
		Spec: v1alpha1.WekaContainerSpec{
			Ipv6: ipv6,
			Port: 15000,
		},
		Status: v1alpha1.WekaContainerStatus{
			ManagementIPs: ips,
		},
	}
}

func TestWekaContainer_GetHostIps(t *testing.T) {
	tests := []struct {
		name      string
		container v1alpha1.WekaContainer
		subnets   []string
		want      []string
	}{
		{
			name:      "ipv4 is never bracketed",
			container: containerWithIps(false, "10.100.6.241"),
			want:      []string{"10.100.6.241:15000"},
		},
		{
			name:      "ipv4 stays unbracketed even when the ipv6 flag is set",
			container: containerWithIps(true, "10.100.6.241"),
			want:      []string{"10.100.6.241:15000"},
		},
		{
			name:      "ipv6 is bracketed",
			container: containerWithIps(true, "2001:db8::1"),
			want:      []string{"[2001:db8::1]:15000"},
		},
		{
			name:      "ipv6 is bracketed even when the ipv6 flag is unset",
			container: containerWithIps(false, "2001:db8::1"),
			want:      []string{"[2001:db8::1]:15000"},
		},
		{
			name:      "mixed families are formatted per address",
			container: containerWithIps(true, "10.100.6.241", "2001:db8::1"),
			want:      []string{"10.100.6.241:15000", "[2001:db8::1]:15000"},
		},
		{
			name:      "subnet filter keeps only matching ips",
			container: containerWithIps(false, "10.100.6.241", "192.168.1.5"),
			subnets:   []string{"10.100.0.0/16"},
			want:      []string{"10.100.6.241:15000"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.container.GetHostIps(tt.subnets)
			if len(got) != len(tt.want) {
				t.Fatalf("GetHostIps() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("GetHostIps()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
