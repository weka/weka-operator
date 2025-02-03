package resources

import (
	"net"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
)

func GetContainerNetwork(selector wekav1alpha1.NetworkSelector) (wekav1alpha1.Network, error) {
	network := wekav1alpha1.Network{}
	// These are on purpose different types
	// Network selector might be "Aws" or "auto" and that will prepare EthDevice for container-level, which will be simpler
	if selector.EthDevice != "" {
		network.EthDevice = selector.EthDevice
		// TODO: Convert single device to a reuse of a list of devices instead of double handling
	}
	if len(selector.EthDevices) > 0 {
		network.EthDevices = selector.EthDevices
	}
	if selector.UdpMode {
		network.UdpMode = true
	}
	if selector.Gateway != "" {
		network.Gateway = selector.Gateway
	}
	network.DeviceSubnets = selector.DeviceSubnets

	return network, nil
}

func ContainerHasDevicesInSubnets(container *wekav1alpha1.WekaContainer, subnets []string) bool {
	managementIps := container.Status.GetManagementIps()

	for _, subnet := range subnets {
		subnetCovered := false
		for _, ip := range managementIps {
			if ipIsInSubnet(ip, subnet) {
				subnetCovered = true
				break
			}
		}
		if !subnetCovered {
			return false
		}
	}
	return true
}

func ipIsInSubnet(ip string, subnet string) bool {
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return false
	}
	return ipNet.Contains(net.ParseIP(ip))
}
