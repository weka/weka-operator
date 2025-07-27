package resources

import (
	"net"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
)

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
