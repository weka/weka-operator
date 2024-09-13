package resources

import (
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
)

func GetContainerNetwork(selector wekav1alpha1.NetworkSelector) (wekav1alpha1.Network, error) {
	network := wekav1alpha1.Network{}
	// These are on purpose different types
	// Network selector might be "Aws" or "auto" and that will prepare EthDevice for container-level, which will be simpler
	if selector.EthDevice != "" {
		network.EthDevice = selector.EthDevice
	}
	if selector.UdpMode {
		network.UdpMode = true
	}

	return network, nil
}
