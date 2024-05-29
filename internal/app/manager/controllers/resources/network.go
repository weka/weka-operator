package resources

import (
	"github.com/weka/weka-operator/internal/app/manager/domain"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
)

func GetContainerNetwork(selector wekav1alpha1.NetworkSelector, resources *domain.OwnedResources) (wekav1alpha1.Network, error) {
	network := wekav1alpha1.Network{}
	// These are on purpose different types
	// Network selector might be "Aws" or "auto" and that will prepare EthDevice for container-level, which will be simpler
	if selector.EthDevice != "" {
		network.EthDevice = selector.EthDevice
	}
	if selector.UdpMode {
		network.UdpMode = true
	}

	if len(selector.EthSlots) > 0 {
		if resources != nil {
			network.EthDevices = resources.EthSlots
		}
	}

	return network, nil
}
