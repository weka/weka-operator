package validation

import (
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func TestClusterNetworkEthdevice(t *testing.T) {
	tests := []struct {
		name                     string
		driveCores, computeCores int
		network                  weka.Network
		wantField                string // empty means expect no errors
		wantSub                  string
	}{
		{
			// Devices pinned by name are not covered by weka.io/weka-nics, so the node
			// allocatable cannot answer this; the count has to come from the spec itself.
			name:       "fewer named devices than cores is reported",
			driveCores: 1, computeCores: 2,
			network:   weka.Network{EthDevice: "ens6"},
			wantField: "spec.dynamicTemplate.computeCores",
			wantSub:   "network.ethDevice(s)",
		},
		{
			name:       "named devices cover every role's cores",
			driveCores: 1, computeCores: 2,
			network: weka.Network{EthDevices: []string{"ens6", "ens7", "ens8"}},
		},
		{
			// udpMode roles need no data devices, so the device count is irrelevant.
			name:       "udpMode is skipped",
			driveCores: 1, computeCores: 8,
			network: weka.Network{UdpMode: true, EthDevice: "ens6"},
		},
		{
			// deviceSubnets resolve to a device count only on the node.
			name:       "deviceSubnets are skipped",
			driveCores: 1, computeCores: 8,
			network: weka.Network{DeviceSubnets: []string{"10.0.0.0/24"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &weka.WekaCluster{}
			c.Spec.Dynamic = &weka.WekaClusterTemplate{
				DriveCores: tc.driveCores, DriveContainers: 6,
				ComputeCores: tc.computeCores, ComputeContainers: 6,
			}
			c.Spec.Network = tc.network

			errs := clusterNetworkEthdevice{}.Validate(context.Background(), fakeClientWithNodes(t), c)

			if tc.wantField == "" {
				if len(errs) != 0 {
					t.Fatalf("expected no errors, got: %v", errs)
				}
				return
			}
			if len(errs) != 1 {
				t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
			}
			if errs[0].Field != tc.wantField {
				t.Errorf("error on field %q, want %q", errs[0].Field, tc.wantField)
			}
			if !strings.Contains(errs[0].Detail, tc.wantSub) {
				t.Errorf("detail missing %q: %s", tc.wantSub, errs[0].Detail)
			}
		})
	}
}

// Pins the EthDevices-wins-over-EthDevice precedence, which the table above does not cover.
func TestNamedNetDeviceCount(t *testing.T) {
	cases := []struct {
		name    string
		network *weka.Network
		want    int
	}{
		{"nil", nil, 0},
		{"empty", &weka.Network{}, 0},
		{"single", &weka.Network{EthDevice: "ens6"}, 1},
		{"list", &weka.Network{EthDevices: []string{"ens6", "ens7"}}, 2},
		{"list wins over single", &weka.Network{EthDevice: "ens6", EthDevices: []string{"ens7", "ens8"}}, 2},
		{"subnets are not named", &weka.Network{DeviceSubnets: []string{"10.0.0.0/24"}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := namedNetDeviceCount(tc.network); got != tc.want {
				t.Errorf("namedNetDeviceCount() = %d, want %d", got, tc.want)
			}
		})
	}
}
