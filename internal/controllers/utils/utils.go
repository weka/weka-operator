package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/discovery"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func getAWSGatewayIP(cidr string) (string, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("invalid CIDR: %v", err)
	}

	ip := ipNet.IP.To4()
	if ip == nil {
		return "", fmt.Errorf("not a valid IPv4 CIDR")
	}

	ip[3] += 1
	return ip.String(), nil
}

func ShouldAllocateVfPerIoNode(node *v1.Node, container *weka.WekaContainer) bool {
	if container.Spec.Network.AllocateVfPerIoNode != nil {
		return *container.Spec.Network.AllocateVfPerIoNode
	}
	return discovery.IsSupportedCloudProvider(node.Spec.ProviderID)
}

func GetNetDevices(ctx context.Context, node *v1.Node, container *weka.WekaContainer) (netDevices []string, err error) {
	_, logger := instrumentation.CreateLogSpan(ctx, "getNetDevices")
	defer logger.End()

	// if subnet for devices auto-discovery or network selectors are set, we don't need to set the netDevice
	if len(container.Spec.Network.Selectors) > 0 {
		return
	}
	if len(container.Spec.Network.DeviceSubnets) > 0 {
		return
	}
	if container.Spec.Network.EthDevice != "" {
		logger.Info("Creating pod with network", "ethDevice", container.Spec.Network.EthDevice)
		netDevices = append(netDevices, container.Spec.Network.EthDevice)
		return
	}
	if len(container.Spec.Network.EthDevices) > 0 {
		netDevices = container.Spec.Network.EthDevices
		return
	}

	allocateVfPerIoNode := ShouldAllocateVfPerIoNode(node, container)

	if allocateVfPerIoNode && !container.Spec.Network.UdpMode && container.ShouldJoinCluster() {
		var allocations domain.Allocations
		var allNICs []domain.NIC

		nicsStr, ok := node.Annotations[domain.WEKANICs]
		if !ok {
			err = fmt.Errorf("node %s does not have weka-nics annotation", node.Name)
			return
		}

		err = json.Unmarshal([]byte(nicsStr), &allNICs)
		if err != nil {
			err = fmt.Errorf("failed to unmarshal weka-nics: %v", err)
			return
		}

		allocationsStr, ok := node.Annotations[domain.WEKAAllocations]
		if !ok {
			err = fmt.Errorf("node %s does not have allocations annotation", node.Name)
			return
		}
		err = json.Unmarshal([]byte(allocationsStr), &allocations)
		if err != nil {
			return
		}

		logger.Info("Fetching pod nics allocations", "allocations", allocations)
		allocationIdentifier := domain.GetAllocationIdentifier(container.Namespace, container.Name)
		for key, alloc := range allocations {
			if key == allocationIdentifier {
				logger.Info("Found allocations", "allocationIdentifier", allocationIdentifier, "alloc", alloc)
				for _, nicIdentifier := range alloc.NICs {
					logger.Info("Found NIC allocation", "nic", nicIdentifier)
					var nic domain.NIC

					for _, wekaNic := range allNICs {
						if nicIdentifier == wekaNic.MacAddress {
							nic = wekaNic
							break
						}
					}

					if nic.MacAddress == "" {
						err = fmt.Errorf("NIC %s not found in WEKA NICs on node: %s", nicIdentifier, node.Name)
						return
					}

					cidr := strings.Split(nic.SubnetCIDRBlock, "/")
					mask := cidr[1]
					gw, err2 := getAWSGatewayIP(nic.SubnetCIDRBlock)
					if err2 != nil {
						err = err2
						return
					}
					netDevices = append(
						netDevices,
						fmt.Sprintf("vf_%s/%s/%s/%s", nic.MacAddress, nic.PrimaryIP, mask, gw),
					)
				}
			}
		}

		if len(netDevices) == 0 {
			err = fmt.Errorf("no NICs found in allocations for container %s", allocationIdentifier)
			logger.Error(err, "Error getting NICs from allocations")
			return
		}
		return
	}

	netDevices = []string{"udp"}
	return
}

// CompareVersions compares two version strings in "major.minor.patch[.build]" format.
// Returns -1 if ver1 < ver2, 0 if equal, 1 if ver1 > ver2.
func CompareVersions(ver1, ver2 string) int {
	parts1 := strings.Split(ver1, ".")
	parts2 := strings.Split(ver2, ".")
	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}
	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(parts1) {
			n1, _ = strconv.Atoi(parts1[i]) //nolint:errcheck // error return value intentionally not checked
		}
		if i < len(parts2) {
			n2, _ = strconv.Atoi(parts2[i]) //nolint:errcheck // error return value intentionally not checked
		}
		if n1 < n2 {
			return -1
		}
		if n1 > n2 {
			return 1
		}
	}
	return 0
}

// DriverDistSource indicates how the driversDistService endpoint was resolved.
type DriverDistSource int

const (
	DriverDistExplicit  DriverDistSource = iota // explicitly set by user
	DriverDistPolicy                            // resolved from a single WekaPolicy
	DriverDistDefault                           // no policy found, using default
	DriverDistAmbiguous                         // multiple policies found, falling back to default
)

// ResolveDriversDistService determines the effective driversDistService endpoint.
// Priority: explicit value > WekaPolicy dist service > default global endpoint.
func ResolveDriversDistService(ctx context.Context, c client.Client, namespace, explicit string) (url string, source DriverDistSource) {
	if explicit != "" {
		return explicit, DriverDistExplicit
	}

	// Collect all matching policies with type "enable-local-drivers-distribution" that have valid dist service URL
	var matches []string
	policyList := &weka.WekaPolicyList{}
	if err := c.List(ctx, policyList, &client.ListOptions{Namespace: namespace}); err == nil {
		for i := range policyList.Items {
			policy := &policyList.Items[i]
			if policy.Spec.Type == weka.WekaPolicyTypeEnableLocalDriversDistribution &&
				policy.Status.TypedStatus != nil &&
				policy.Status.TypedStatus.DistService != nil &&
				policy.Status.TypedStatus.DistService.ServiceUrl != "" {
				matches = append(matches, policy.Status.TypedStatus.DistService.ServiceUrl)
			}
		}
	}

	switch len(matches) {
	case 0:
		return "https://drivers.weka.io", DriverDistDefault
	case 1:
		return matches[0], DriverDistPolicy
	default:
		return "https://drivers.weka.io", DriverDistAmbiguous
	}
}

// GetSoftwareVersion extracts the software version of weka
func GetSoftwareVersion(image string) string {
	idx := strings.LastIndex(image, ":")
	if idx == -1 {
		return ""
	}
	tag := image[idx+1:]
	parts := strings.Split(tag, "-")
	return parts[0]
}

// GetDpdkBaseMemoryMbByRole returns the DPDK base memory (in MiB) for a given role.
// Returns the override value if set, otherwise returns the default value of 64 MiB.
func GetDpdkBaseMemoryMbByRole(spec *weka.WekaClusterSpec, role string) int {
	switch role {
	case "compute":
		if spec.GetOverrides().DpdkBaseMemoryMb.Compute != 0 {
			return spec.GetOverrides().DpdkBaseMemoryMb.Compute
		}
	case "drive":
		if spec.GetOverrides().DpdkBaseMemoryMb.Drive != 0 {
			return spec.GetOverrides().DpdkBaseMemoryMb.Drive
		}
	case "s3":
		if spec.GetOverrides().DpdkBaseMemoryMb.S3 != 0 {
			return spec.GetOverrides().DpdkBaseMemoryMb.S3
		}
	case "nfs":
		if spec.GetOverrides().DpdkBaseMemoryMb.Nfs != 0 {
			return spec.GetOverrides().DpdkBaseMemoryMb.Nfs
		}
	case "smbw":
		if spec.GetOverrides().DpdkBaseMemoryMb.Smbw != 0 {
			return spec.GetOverrides().DpdkBaseMemoryMb.Smbw
		}
	case "data-services":
		if spec.GetOverrides().DpdkBaseMemoryMb.DataServices != 0 {
			return spec.GetOverrides().DpdkBaseMemoryMb.DataServices
		}
	}
	return 64 // Default value in MiB
}
