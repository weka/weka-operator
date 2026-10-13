package network

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/paths"
	"github.com/weka/weka-operator/internal/runtime/process"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// fakeRunner is a minimal process.CommandRunner test double: it records every call and
// delegates the result to resultFn, if set.
type fakeRunner struct {
	mu       sync.Mutex
	calls    []process.Command
	resultFn func(c process.Command) (process.Result, error)
}

func (f *fakeRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if f.resultFn != nil {
		return f.resultFn(c)
	}
	return process.Result{}, nil
}

func (f *fakeRunner) callsWithPrefix(prefix ...string) []process.Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []process.Command
	for _, c := range f.calls {
		if len(c.Args) < len(prefix) {
			continue
		}
		match := true
		for i, p := range prefix {
			if c.Args[i] != p {
				match = false
				break
			}
		}
		if match {
			out = append(out, c)
		}
	}
	return out
}

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

func TestPairDevicesBySubnet(t *testing.T) {
	tests := []struct {
		name             string
		subnets          []string
		perSubnetDevices [][]string
		want             []devSubnetPair
	}{
		{
			name:             "single subnet, single device",
			subnets:          []string{"10.0.0.0/24"},
			perSubnetDevices: [][]string{{"eth0"}},
			want:             []devSubnetPair{{device: "eth0", subnet: "10.0.0.0/24"}},
		},
		{
			name:             "distinct devices across subnets",
			subnets:          []string{"10.0.0.0/24", "10.0.1.0/24"},
			perSubnetDevices: [][]string{{"eth0"}, {"eth1"}},
			want: []devSubnetPair{
				{device: "eth0", subnet: "10.0.0.0/24"},
				{device: "eth1", subnet: "10.0.1.0/24"},
			},
		},
		{
			name:             "device seen in a later subnet keeps its first subnet",
			subnets:          []string{"10.0.0.0/24", "10.0.1.0/24"},
			perSubnetDevices: [][]string{{"eth0"}, {"eth0", "eth1"}},
			want: []devSubnetPair{
				{device: "eth0", subnet: "10.0.0.0/24"},
				{device: "eth1", subnet: "10.0.1.0/24"},
			},
		},
		{
			name:             "no devices in any subnet",
			subnets:          []string{"10.0.0.0/24"},
			perSubnetDevices: [][]string{nil},
			want:             nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pairDevicesBySubnet(tt.subnets, tt.perSubnetDevices)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("pairDevicesBySubnet() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestIsUDP(t *testing.T) {
	tests := []struct {
		name          string
		udpMode       bool
		networkDevice string
		want          bool
	}{
		{"UDPMode true", true, "", true},
		{"NetworkDevice=udp (lowercase)", false, "udp", true},
		{"NetworkDevice=UDP (uppercase)", false, "UDP", true},
		{"NetworkDevice=Udp (mixed case)", false, "Udp", true},
		{"both UDPMode and NetworkDevice=udp", true, "udp", true},
		{"neither UDPMode nor udp device", false, "eth0", false},
		{"empty", false, "", false},
		{"NetworkDevice contains udp but is not exactly udp", false, "eth0,udp", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsUDP(tt.udpMode, tt.networkDevice); got != tt.want {
				t.Errorf("IsUDP(%v, %q) = %v, want %v", tt.udpMode, tt.networkDevice, got, tt.want)
			}
		})
	}
}

// TestReconcileNetDevicesRdmaOnlySelectorSetsFlag verifies a selector with RdmaOnly produces
// the --rdma-only flag on the `weka local resources net add` command.
func TestReconcileNetDevicesRdmaOnlySelectorSetsFlag(t *testing.T) {
	runner := &fakeRunner{
		resultFn: func(c process.Command) (process.Result, error) {
			if c.Path == "ip" && len(c.Args) > 0 && c.Args[0] == "link" {
				return process.Result{}, nil // device exists
			}
			if c.Path == "weka" && len(c.Args) > 1 && c.Args[1] == "resources" && len(c.Args) == 5 {
				// `weka local resources -C <name> --json`
				return process.Result{Stdout: []byte(`{"net_devices":[],"rdma_devices":{"devices":[]}}`)}, nil
			}
			return process.Result{}, nil
		},
	}

	err := ReconcileNetDevices(context.Background(), runner, ReconcileInput{
		Name: "cont0",
		Selectors: []weka.NetworkSelector{
			{DeviceNames: []string{"ib0"}, RdmaOnly: true},
		},
	})
	if err != nil {
		t.Fatalf("ReconcileNetDevices() error = %v", err)
	}

	addCalls := runner.callsWithPrefix("local", "resources", "net", "-C", "cont0", "add", "ib0")
	if len(addCalls) != 1 {
		t.Fatalf("expected exactly one add call for ib0, got %d: %+v", len(addCalls), runner.calls)
	}
	found := false
	for _, a := range addCalls[0].Args {
		if a == "--rdma-only" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected --rdma-only flag in add command, got args %v", addCalls[0].Args)
	}
}

// TestWriteManagementIPsReturnsWrittenIPs verifies WriteManagementIPs returns the IPs it wrote,
// matching what lands in the management_ips file.
func TestWriteManagementIPsReturnsWrittenIPs(t *testing.T) {
	runner := &fakeRunner{
		resultFn: func(c process.Command) (process.Result, error) {
			return process.Result{Stdout: []byte("2: eth0    inet 10.0.0.5/24 brd 10.0.0.255 scope global eth0\\ valid_lft forever\n")}, nil
		},
	}
	dir := t.TempDir()
	roots := paths.Roots{K8sRuntime: dir}

	got, err := WriteManagementIPs(context.Background(), runner, ManagementInput{
		Mode:          "compute",
		NetworkDevice: "eth0",
	}, roots)
	if err != nil {
		t.Fatalf("WriteManagementIPs() error = %v", err)
	}
	want := []string{"10.0.0.5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WriteManagementIPs() = %v, want %v", got, want)
	}

	written, err := os.ReadFile(filepath.Join(dir, "management_ips"))
	if err != nil {
		t.Fatalf("reading management_ips: %v", err)
	}
	if string(written) != "10.0.0.5" {
		t.Fatalf("management_ips file = %q, want %q", written, "10.0.0.5")
	}
}

func TestGetDevicesBySelectorsDeviceNamesWithSubnet(t *testing.T) {
	// Only eth1 has an address in 10.0.1.0/24; every interface exists for `ip link show`.
	runner := &fakeRunner{
		resultFn: func(c process.Command) (process.Result, error) {
			if c.Path == "ip" && strings.Join(c.Args, " ") == "-o addr show dev eth1" {
				return process.Result{Stdout: []byte(
					"3: eth1    inet 10.0.2.9/24 scope global eth1\\ valid_lft forever\n" +
						"3: eth1    inet 10.0.1.7/24 scope global eth1\\ valid_lft forever\n")}, nil
			}
			return process.Result{}, nil
		},
	}

	tests := []struct {
		name    string
		sel     weka.NetworkSelector
		want    []deviceInfo
		wantErr bool
	}{
		{
			name: "keeps only devices with an address in the subnet",
			sel:  weka.NetworkSelector{DeviceNames: []string{"eth0", "eth1"}, Subnet: "10.0.1.0/24"},
			want: []deviceInfo{{device: "eth1", subnet: "10.0.1.0/24"}},
		},
		{
			name:    "no device in the subnet is an error",
			sel:     weka.NetworkSelector{DeviceNames: []string{"eth0"}, Subnet: "10.0.1.0/24"},
			wantErr: true,
		},
		{
			name: "rdma-only devices are not narrowed and carry no subnet",
			sel:  weka.NetworkSelector{DeviceNames: []string{"ib0"}, Subnet: "10.0.1.0/24", RdmaOnly: true},
			want: []deviceInfo{{device: "ib0", rdmaOnly: true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getDevicesBySelectors(context.Background(), runner, []weka.NetworkSelector{tt.sel})
			if (err != nil) != tt.wantErr {
				t.Fatalf("getDevicesBySelectors() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("getDevicesBySelectors() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func addrRunner(outputs map[string]string) *fakeRunner {
	return &fakeRunner{
		resultFn: func(c process.Command) (process.Result, error) {
			return process.Result{Stdout: []byte(outputs[c.Path+" "+strings.Join(c.Args, " ")])}, nil
		},
	}
}

func TestGetSingleDeviceIP(t *testing.T) {
	const (
		v4   = "2: eth0    inet 10.0.0.5/24 scope global eth0\\ valid_lft forever\n"
		v6   = "2: eth0    inet6 fd00::5/64 scope global \\ valid_lft forever\n"
		ll   = "2: eth0    inet6 fe80::1/64 scope link \\ valid_lft forever\n"
		lo   = "2: eth0    inet 127.0.0.1/8 scope host lo\\ valid_lft forever\n"
		link = "2: eth0    link/ether aa:bb:cc:dd:ee:ff brd ff:ff:ff:ff:ff:ff\n"
	)
	show := "ip -o addr show dev eth0"
	tests := []struct {
		name    string
		out     map[string]string
		device  string
		subnet  string
		isIPv6  bool
		want    string
		wantErr bool
	}{
		{name: "dual-stack picks v4", out: map[string]string{show: v6 + v4}, device: "eth0", want: "10.0.0.5"},
		{name: "dual-stack picks v6 when asked", out: map[string]string{show: v4 + v6}, device: "eth0", isIPv6: true, want: "fd00::5"},
		{name: "v6-only device without flag", out: map[string]string{show: v6}, device: "eth0", want: "fd00::5"},
		{name: "link-local and loopback skipped", out: map[string]string{show: ll + lo + v4}, device: "eth0", want: "10.0.0.5"},
		{name: "subnet containment", out: map[string]string{show: v4 + "2: eth0    inet 10.0.1.7/24 scope global eth0\\ x\n"}, device: "eth0", subnet: "10.0.1.0/24", want: "10.0.1.7"},
		{name: "default route with src", out: map[string]string{
			"ip -4 route show default": "default via 10.0.0.1 dev eth0 proto dhcp src 10.0.0.5 metric 100\n",
		}, device: "default", want: "10.0.0.5"},
		{name: "ipv6 default route falls back to dev address", out: map[string]string{
			"ip -6 route show default": "default via fd00::1 dev eth0 proto ra metric 1024\n",
			show:                       v4 + v6,
		}, device: "default", isIPv6: true, want: "fd00::5"},
		{name: "no address", out: map[string]string{show: link}, device: "eth0", wantErr: true},
		{name: "invalid subnet", out: map[string]string{show: v4}, device: "eth0", subnet: "nope", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getSingleDeviceIP(context.Background(), addrRunner(tt.out), tt.device, tt.subnet, tt.isIPv6)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
