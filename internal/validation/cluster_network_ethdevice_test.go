package validation

import (
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// ethDeviceCluster builds a DPDK cluster whose data devices are pinned by name.
func ethDeviceCluster(driveCores, computeCores int, network weka.Network) *weka.WekaCluster {
	c := &weka.WekaCluster{}
	c.Spec.Dynamic = &weka.WekaClusterTemplate{
		DriveCores: driveCores, DriveContainers: 6,
		ComputeCores: computeCores, ComputeContainers: 6,
	}
	c.Spec.Network = network
	return c
}

// Devices pinned by name are not covered by weka.io/weka-nics, so the node allocatable
// cannot answer this; the count has to come from the spec itself.
func TestClusterNetworkEthdevice_NamedDevicesFewerThanCores(t *testing.T) {
	cluster := ethDeviceCluster(1, 2, weka.Network{EthDevice: "ens6"})

	errs := clusterNetworkEthdevice{}.Validate(context.Background(), fake.NewClientBuilder().Build(), cluster)

	if len(errs) != 1 {
		t.Fatalf("expected 1 error for computeCores=2 against 1 named device, got %d: %v", len(errs), errs)
	}
	if got := errs[0].Field; got != "spec.dynamicTemplate.computeCores" {
		t.Errorf("error on field %q, want spec.dynamicTemplate.computeCores", got)
	}
	if !strings.Contains(errs[0].Detail, "network.ethDevice(s)") {
		t.Errorf("detail does not mention the pinned devices: %s", errs[0].Detail)
	}
}

func TestClusterNetworkEthdevice_NamedDevicesCoverCores(t *testing.T) {
	cluster := ethDeviceCluster(1, 2, weka.Network{EthDevices: []string{"ens6", "ens7", "ens8"}})

	errs := clusterNetworkEthdevice{}.Validate(context.Background(), fake.NewClientBuilder().Build(), cluster)

	if len(errs) != 0 {
		t.Fatalf("expected no errors when 3 devices cover driveCores=1/computeCores=2, got: %v", errs)
	}
}

// udpMode roles need no data devices, so the device count is irrelevant.
func TestClusterNetworkEthdevice_UdpModeSkipped(t *testing.T) {
	cluster := ethDeviceCluster(1, 8, weka.Network{UdpMode: true, EthDevice: "ens6"})

	errs := clusterNetworkEthdevice{}.Validate(context.Background(), fake.NewClientBuilder().Build(), cluster)

	if len(errs) != 0 {
		t.Fatalf("expected udpMode to be skipped, got: %v", errs)
	}
}

// deviceSubnets resolve to a device count only on the node, so there is nothing to compare.
func TestClusterNetworkEthdevice_DeviceSubnetsSkipped(t *testing.T) {
	cluster := ethDeviceCluster(1, 8, weka.Network{DeviceSubnets: []string{"10.0.0.0/24"}})

	errs := clusterNetworkEthdevice{}.Validate(context.Background(), fake.NewClientBuilder().Build(), cluster)

	if len(errs) != 0 {
		t.Fatalf("expected deviceSubnets to be skipped, got: %v", errs)
	}
}

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
