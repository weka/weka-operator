package network

import (
	"net"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/config"
)

func TestShouldAllocateVFPerIoNode(t *testing.T) {
	tests := []struct {
		device string
		want   bool
	}{
		{"vf_eth0", true},
		{"eth0,vf_eth1", true},
		{"eth0", false},
		{"", false},
		{"udp", false},
	}
	for _, tt := range tests {
		if got := ShouldAllocateVFPerIoNode(tt.device); got != tt.want {
			t.Errorf("ShouldAllocateVFPerIoNode(%q) = %v, want %v", tt.device, got, tt.want)
		}
	}
}

// realIPAddrOutput is a verbatim fixture from a live node.
// Field layout: parts[0]=index, parts[1]=device, parts[2]=inet/inet6, parts[3]=addr/CIDR.
var realIPAddrOutput = []byte(
	"1: lo    inet 127.0.0.1/8 scope host lo\\ \n" +
		"       valid_lft forever preferred_lft forever\n" +
		"1: lo    inet6 ::1/128 scope host \\ \n" +
		"       valid_lft forever preferred_lft forever\n" +
		"2: enp80s0f0    inet 172.31.5.61/21 metric 100 brd 172.31.7.255 scope global dynamic enp80s0f0\\ \n" +
		"       valid_lft 9949sec preferred_lft 9949sec\n" +
		"2: enp80s0f0    inet6 fe80::1/64 scope link\\ \n" +
		"       valid_lft forever preferred_lft forever\n" +
		"4: enp99s0f0np0    inet 10.100.5.61/16 brd 10.100.255.255 scope global enp99s0f0np0\\ \n" +
		"       valid_lft forever preferred_lft forever\n" +
		"4: enp99s0f0np0    inet6 fe80::2/64 scope link\\ \n" +
		"       valid_lft forever preferred_lft forever\n" +
		"5: ib0    inet 10.2.5.61/16 brd 10.2.255.255 scope global ib0\\ \n" +
		"       valid_lft forever preferred_lft forever\n" +
		"5: ib0    inet6 fe80::3/64 scope link\\ \n" +
		"       valid_lft forever preferred_lft forever\n",
)

func mustParseCIDR(s string) *net.IPNet {
	_, ipNet, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return ipNet
}

func TestFilterDevicesInSubnet(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		subnet string
		want   []string
	}{
		{
			name:   "10.100.0.0/16 matches enp99s0f0np0",
			input:  realIPAddrOutput,
			subnet: "10.100.0.0/16",
			want:   []string{"enp99s0f0np0"},
		},
		{
			name:   "10.2.0.0/16 matches ib0",
			input:  realIPAddrOutput,
			subnet: "10.2.0.0/16",
			want:   []string{"ib0"},
		},
		{
			name:   "172.31.0.0/21 matches enp80s0f0",
			input:  realIPAddrOutput,
			subnet: "172.31.0.0/21",
			want:   []string{"enp80s0f0"},
		},
		{
			// IPv4 target: all inet6 lines must be excluded by family filter.
			name:   "IPv4 subnet excludes all inet6 lines",
			input:  realIPAddrOutput,
			subnet: "10.100.0.0/16",
			want:   []string{"enp99s0f0np0"}, // no inet6 entries even though enp99s0f0np0 has one
		},
		{
			// lo (127.0.0.1) must be excluded when target subnet doesn't contain it.
			name:   "lo excluded by CIDR mismatch",
			input:  realIPAddrOutput,
			subnet: "10.2.0.0/16",
			want:   []string{"ib0"}, // lo is not in 10.2.0.0/16
		},
		{
			// Subnet that no address belongs to returns nil.
			name:   "no match returns nil",
			input:  realIPAddrOutput,
			subnet: "192.168.0.0/24",
			want:   nil,
		},
		{
			// Short/garbage lines (< 4 fields) must be skipped silently.
			name:   "short lines skipped",
			input:  []byte("1: lo\n2: eth0    inet\n"),
			subnet: "10.0.0.0/8",
			want:   nil,
		},
		{
			// Empty input produces nil.
			name:   "empty input",
			input:  []byte(""),
			subnet: "10.0.0.0/8",
			want:   nil,
		},
		{
			// IPv6 case: target fe80::/64, expect enp80s0f0 (has fe80::1/64).
			name:   "IPv6 fe80::/64 matches link-local on enp80s0f0",
			input:  realIPAddrOutput,
			subnet: "fe80::/64",
			want:   []string{"enp80s0f0", "enp99s0f0np0", "ib0"},
		},
		{
			// Zone ID in address ("%eth0") must be stripped before parsing.
			name:   "zone ID stripped from IPv6 address",
			input:  []byte("3: eth1    inet6 fe80::1%eth1/64 scope link\\ \n"),
			subnet: "fe80::/64",
			want:   []string{"eth1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipNet := mustParseCIDR(tt.subnet)
			got := filterDevicesInSubnet(tt.input, ipNet)

			if len(got) != len(tt.want) {
				t.Fatalf("filterDevicesInSubnet(%q): got %v, want %v", tt.subnet, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("filterDevicesInSubnet(%q)[%d] = %q, want %q", tt.subnet, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIsUDP(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "UDPMode true",
			cfg:  &config.Config{UDPMode: true},
			want: true,
		},
		{
			name: "NetworkDevice=udp (lowercase)",
			cfg:  &config.Config{NetworkDevice: "udp"},
			want: true,
		},
		{
			name: "NetworkDevice=UDP (uppercase)",
			cfg:  &config.Config{NetworkDevice: "UDP"},
			want: true,
		},
		{
			name: "NetworkDevice=Udp (mixed case)",
			cfg:  &config.Config{NetworkDevice: "Udp"},
			want: true,
		},
		{
			name: "both UDPMode and NetworkDevice=udp",
			cfg:  &config.Config{UDPMode: true, NetworkDevice: "udp"},
			want: true,
		},
		{
			name: "neither UDPMode nor udp device",
			cfg:  &config.Config{NetworkDevice: "eth0"},
			want: false,
		},
		{
			name: "empty config",
			cfg:  &config.Config{},
			want: false,
		},
		{
			name: "NetworkDevice contains udp but is not exactly udp",
			cfg:  &config.Config{NetworkDevice: "eth0,udp"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isUDP(tt.cfg)
			if got != tt.want {
				t.Errorf("isUDP(%+v) = %v, want %v", tt.cfg, got, tt.want)
			}
		})
	}
}
