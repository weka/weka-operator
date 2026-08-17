package deviceplugin

import (
	"fmt"

	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// DevicesPerRegion is the fixed number of virtual devices advertised for each NUMA region.
// These are not physical devices: they exist purely so pods can request "a slot on NUMA
// region N" via a resource request, with the count acting as a concurrency cap per region.
const DevicesPerRegion = 32

// ResourceName returns the extended resource name advertised for the given NUMA region,
// e.g. "weka.io/numa-region-0".
func ResourceName(region int) string {
	return fmt.Sprintf("weka.io/numa-region-%d", region)
}

// SocketName returns the unix socket file name (relative to the kubelet device-plugins
// directory) used by the plugin instance serving the given NUMA region.
func SocketName(region int) string {
	return fmt.Sprintf("weka-numa-region-%d.sock", region)
}

// DeviceID returns the device ID advertised for the slot-th virtual device of a NUMA region.
func DeviceID(region, slot int) string {
	return fmt.Sprintf("numa-region-%d-slot-%d", region, slot)
}

// GenerateDevices returns the fixed list of DevicesPerRegion devices advertised for a NUMA
// region, all reported Healthy.
func GenerateDevices(region int) []*pluginapi.Device {
	devices := make([]*pluginapi.Device, 0, DevicesPerRegion)
	for slot := 0; slot < DevicesPerRegion; slot++ {
		devices = append(devices, &pluginapi.Device{
			ID:     DeviceID(region, slot),
			Health: pluginapi.Healthy,
			// NUMA affinity hint: lets the kubelet Topology Manager (when its policy is not
			// "none") align other aligned resources (e.g. static CPU Manager cpusets) with
			// the NUMA node this region resource represents.
			Topology: &pluginapi.TopologyInfo{
				Nodes: []*pluginapi.NUMANode{{ID: int64(region)}},
			},
		})
	}
	return devices
}
