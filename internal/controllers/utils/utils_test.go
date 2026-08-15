package utils

import (
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

func TestHasExplicitNetDevices(t *testing.T) {
	cases := []struct {
		name string
		n    *weka.Network
		want bool
	}{
		// The per-role network fields in the API are *Network, so nil is reachable.
		{"nil network", nil, false},
		{"empty network", &weka.Network{}, false},
		{"selectors set", &weka.Network{Selectors: []weka.NetworkSelector{{}}}, true},
		{"device subnets set", &weka.Network{DeviceSubnets: []string{"10.0.0.0/24"}}, true},
		{"eth device set", &weka.Network{EthDevice: "eth0"}, true},
		{"eth devices set", &weka.Network{EthDevices: []string{"eth0", "eth1"}}, true},
		{"udp mode only, no explicit devices", &weka.Network{UdpMode: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := HasExplicitNetDevices(c.n)
			if got != c.want {
				t.Errorf("HasExplicitNetDevices(%+v) = %v, want %v", c.n, got, c.want)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		name   string
		v1, v2 string
		want   int
	}{
		{"equal 3-part", "4.4.2", "4.4.2", 0},
		{"greater patch", "4.4.3", "4.4.2", 1},
		{"lesser patch", "4.4.2", "4.4.3", -1},
		{"greater minor", "4.5.0", "4.4.9", 1},
		{"equal 4-part", "4.4.2.163", "4.4.2.163", 0},
		{"greater build", "4.4.2.163", "4.4.2.162", 1},
		{"lesser build", "4.4.2.162", "4.4.2.163", -1},
		{"3-part vs 4-part equal base", "4.4.2", "4.4.2.163", -1},
		{"dev build > k8s-qa build", "5.1.1.17119", "4.4.2.163", 1},
		{"k8s-qa build < dev build", "4.4.2.163", "5.1.1.17119", -1},
		{"same dev build", "5.1.1.17119", "5.1.1.17119", 0},
		{"dev build lower build num", "5.1.1.17118", "5.1.1.17119", -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CompareVersions(c.v1, c.v2)
			if got != c.want {
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.v1, c.v2, got, c.want)
			}
		})
	}
}

func TestGetNumericVersion(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		{
			image: "10.200.6.131:5000/weka-in-container:4.4.2.163-k8s-qa",
			want:  "4.4.2.163",
		},
		{
			image: "quay.io/weka.io/weka-in-container:4.4.2.163-k8s-qa",
			want:  "4.4.2.163",
		},
		{
			image: "quay.io/weka.io/weka-in-container:4.4.2",
			want:  "4.4.2",
		},
		{
			image: "image:4.4.2",
			want:  "4.4.2",
		},
		{
			image: "image:4.4.2.163",
			want:  "4.4.2.163",
		},
		{
			image: "image:4.4.2-rc1",
			want:  "4.4.2",
		},
		{
			image: "invalid-format",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			got := GetSoftwareVersion(tt.image)
			if got != tt.want {
				t.Errorf("GetSoftwareVersion(%q) = %q, want %q", tt.image, got, tt.want)
			}
		})
	}
}
