// Package network handles management-IP discovery and network-device reconciliation.
// Mirrors write_management_ips, reconcile_net_devices, autodiscover_network_devices
// at weka_runtime.py:2217–3853.
package network

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
)

// ManagementIPs is updated by WriteManagementIPs and used when building the Weka container.
var ManagementIPs []string

// AutodiscoverNetDevices resolves cfg.NetworkDevice from selectors or subnets when it is empty.
// Mirrors the discovery at weka_runtime.py:2333–2339.
func AutodiscoverNetDevices(ctx context.Context, cfg *config.Config) error {
	if cfg.NetworkDevice != "" {
		return nil
	}
	if len(cfg.NetworkSelectors) > 0 {
		raw := mustJSONMarshal(cfg.NetworkSelectors)
		devInfos, err := getDevicesBySelectors(ctx, raw)
		if err != nil {
			return fmt.Errorf("network: selectors discovery: %w", err)
		}
		var names []string
		for _, d := range devInfos {
			names = append(names, d.device)
		}
		cfg.NetworkDevice = strings.Join(names, ",")
		return nil
	}
	if len(cfg.Subnets) > 0 {
		pairs, err := getDevicesBySubnets(ctx, cfg.Subnets)
		if err != nil {
			return fmt.Errorf("network: subnet discovery: %w", err)
		}
		names := make([]string, len(pairs))
		for i, p := range pairs {
			names[i] = p.device
		}
		cfg.NetworkDevice = strings.Join(names, ",")
	}
	return nil
}

// ReconcileNetDevices syncs the container's net devices to match the desired list.
// Mirrors Python reconcile_net_devices() at weka_runtime.py:2594.
func ReconcileNetDevices(ctx context.Context, containerName string, desired []string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "network.ReconcileNetDevices", "container", containerName)
	defer logger.End()

	current, err := getContainerNetDevices(ctx, containerName)
	if err != nil {
		return fmt.Errorf("network: get container net devices: %w", err)
	}

	desiredSet := make(map[string]struct{}, len(desired))
	for _, d := range desired {
		desiredSet[d] = struct{}{}
	}
	currentSet := make(map[string]struct{}, len(current))
	for _, c := range current {
		currentSet[c] = struct{}{}
	}

	for dev := range currentSet {
		if _, ok := desiredSet[dev]; !ok {
			if err := cmdutil.Run(ctx, "weka", "local", "resources", "net", "-C", containerName, "remove", dev); err != nil {
				return fmt.Errorf("network: remove %s: %w", dev, err)
			}
		}
	}
	for dev := range desiredSet {
		if _, ok := currentSet[dev]; !ok {
			if err := cmdutil.Run(ctx, "weka", "local", "resources", "net", "-C", containerName, "add", dev); err != nil {
				return fmt.Errorf("network: add %s: %w", dev, err)
			}
		}
	}
	return nil
}

// WriteManagementIPs discovers management IPs and writes them atomically.
// Mirrors Python write_management_ips() at weka_runtime.py:3797.
func WriteManagementIPs(ctx context.Context, cfg *config.Config) error {
	switch cfg.Mode {
	case "drive", "compute", "s3", "nfs", "smbw", "client", "data-services":
	default:
		return nil
	}

	_, logger := instrumentation.CreateLogSpan(ctx, "network.WriteManagementIPs")
	defer logger.End()

	var ipAddresses []string

	switch {
	case cfg.ManagementIP != "" && ShouldAllocateVFPerIoNode(cfg.NetworkDevice):
		ipAddresses = []string{cfg.ManagementIP}

	case len(cfg.ManagementIPSelectors) > 0:
		raw := mustJSONMarshal(cfg.ManagementIPSelectors)
		devInfos, err := getDevicesBySelectors(ctx, raw)
		if err != nil {
			return fmt.Errorf("network.WriteManagementIPs selectors: %w", err)
		}
		for _, d := range devInfos {
			ip, err := getSingleDeviceIP(ctx, d.device, d.subnet, cfg.IsIPv6)
			if err != nil {
				return err
			}
			ipAddresses = append(ipAddresses, ip)
		}

	case cfg.NetworkDevice == "" && len(cfg.NetworkSelectors) > 0:
		raw := mustJSONMarshal(cfg.NetworkSelectors)
		allDevInfos, err := getDevicesBySelectors(ctx, raw)
		if err != nil {
			return fmt.Errorf("network.WriteManagementIPs network selectors: %w", err)
		}
		for _, d := range allDevInfos {
			if d.rdmaOnly {
				continue
			}
			ip, err := getSingleDeviceIP(ctx, d.device, d.subnet, cfg.IsIPv6)
			if err != nil {
				return err
			}
			ipAddresses = append(ipAddresses, ip)
		}
		if len(ipAddresses) == 0 {
			return fmt.Errorf("network: no non-rdma-only devices available; configure managementIpsSelectors separately")
		}

	case cfg.NetworkDevice == "" && len(cfg.Subnets) > 0:
		pairs, err := getDevicesBySubnets(ctx, cfg.Subnets)
		if err != nil {
			return err
		}
		for _, p := range pairs {
			ip, err := getSingleDeviceIP(ctx, p.device, p.subnet, cfg.IsIPv6)
			if err != nil {
				return err
			}
			ipAddresses = append(ipAddresses, ip)
		}

	case isUDP(cfg):
		device := cfg.NetworkDevice
		if device == "udp" {
			device = "default"
		}
		ip, err := getSingleDeviceIP(ctx, device, "", cfg.IsIPv6)
		if err != nil {
			return err
		}
		ipAddresses = []string{ip}

	case !strings.Contains(cfg.NetworkDevice, ","):
		ip, err := getSingleDeviceIP(ctx, cfg.NetworkDevice, "", cfg.IsIPv6)
		if err != nil {
			return err
		}
		ipAddresses = []string{ip}

	default:
		// Multiple NICs.
		devices := strings.Split(cfg.NetworkDevice, ",")
		for _, dev := range devices {
			ip, err := getSingleDeviceIP(ctx, dev, "", cfg.IsIPv6)
			if err != nil {
				return err
			}
			ipAddresses = append(ipAddresses, ip)
		}
	}

	if len(ipAddresses) == 0 {
		return fmt.Errorf("network: failed to discover management IPs")
	}

	// Atomic write.
	tmpPath := "/opt/weka/k8s-runtime/management_ips.tmp"
	if err := os.MkdirAll("/opt/weka/k8s-runtime", 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmpPath, []byte(strings.Join(ipAddresses, "\n")), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, "/opt/weka/k8s-runtime/management_ips"); err != nil {
		return err
	}
	logger.Info("management IPs written", "ips", ipAddresses)
	ManagementIPs = ipAddresses
	return nil
}

// ---- helpers ----------------------------------------------------------------

type deviceInfo struct {
	device      string
	subnet      string // empty when the device came from a deviceNames selector
	rdmaOnly    bool
	disableRDMA bool
}

// devSubnetPair is a (device, subnet) result from subnet-based discovery.
// Mirrors the Python Tuple[str, str] return of get_devices_by_subnets().
type devSubnetPair struct {
	device string
	subnet string
}

// getDevicesBySelectors filters devices from a JSON-encoded selector list.
// Mirrors Python get_devices_by_selectors() at weka_runtime.py:3750.
func getDevicesBySelectors(ctx context.Context, selectorsJSON string) ([]deviceInfo, error) {
	var selectors []struct {
		Min         int      `json:"min"`
		Max         int      `json:"max"`
		DeviceNames []string `json:"deviceNames"`
		Subnet      string   `json:"subnet"`
		RdmaOnly    bool     `json:"rdmaOnly"`
		DisableRdma bool     `json:"disableRdma"`
	}
	if err := json.Unmarshal([]byte(selectorsJSON), &selectors); err != nil {
		return nil, fmt.Errorf("getDevicesBySelectors: parse JSON: %w", err)
	}

	var devices []deviceInfo
	seen := make(map[string]struct{})

	for _, sel := range selectors {
		minDev := sel.Min
		maxDev := sel.Max

		if len(sel.DeviceNames) > 0 {
			available := filterMissingDevices(ctx, sel.DeviceNames, sel.RdmaOnly)
			if len(available) < minDev {
				return nil, fmt.Errorf("not enough devices by deviceNames: want %d, got %d", minDev, len(available))
			}
			if maxDev > 0 && len(available) > maxDev {
				available = available[:maxDev]
			}
			for _, name := range available {
				if _, ok := seen[name]; !ok {
					seen[name] = struct{}{}
					devices = append(devices, deviceInfo{device: name, rdmaOnly: sel.RdmaOnly, disableRDMA: sel.DisableRdma})
				}
			}
			continue
		}
		if sel.Subnet == "" {
			return nil, fmt.Errorf("selector must have deviceNames or subnet")
		}
		subnetDevs, err := waitForSubnet(ctx, sel.Subnet)
		if err != nil {
			return nil, err
		}
		if len(subnetDevs) < minDev {
			return nil, fmt.Errorf("not enough devices in subnet %s: want %d, got %d", sel.Subnet, minDev, len(subnetDevs))
		}
		if maxDev > 0 && len(subnetDevs) > maxDev {
			subnetDevs = subnetDevs[:maxDev]
		}
		for _, name := range subnetDevs {
			if _, ok := seen[name]; !ok {
				seen[name] = struct{}{}
				devices = append(devices, deviceInfo{device: name, subnet: sel.Subnet, rdmaOnly: sel.RdmaOnly, disableRDMA: sel.DisableRdma})
			}
		}
	}
	return devices, nil
}

// getDevicesBySubnets finds interfaces whose IP is in any of the given subnets,
// paired with the subnet each was found in. Mirrors Python get_devices_by_subnets()
// / get_devices_waiting_for_all_subnets_to_have_device() at weka_runtime.py:3742 / 3680.
func getDevicesBySubnets(ctx context.Context, subnets []string) ([]devSubnetPair, error) {
	perSubnetDevices := make([][]string, len(subnets))
	for i, subnet := range subnets {
		devs, err := waitForSubnet(ctx, subnet)
		if err != nil {
			return nil, err
		}
		perSubnetDevices[i] = devs
	}
	return pairDevicesBySubnet(subnets, perSubnetDevices), nil
}

// pairDevicesBySubnet dedups per-subnet device lists into (device, subnet) pairs, keeping each
// device's first-seen subnet. Pure logic split out of getDevicesBySubnets for testability.
func pairDevicesBySubnet(subnets []string, perSubnetDevices [][]string) []devSubnetPair {
	var result []devSubnetPair
	seen := make(map[string]struct{})
	for i, subnet := range subnets {
		for _, d := range perSubnetDevices[i] {
			if _, ok := seen[d]; !ok {
				seen[d] = struct{}{}
				result = append(result, devSubnetPair{device: d, subnet: subnet})
			}
		}
	}
	return result
}

// waitForSubnet polls ip -o addr until at least one device is in the subnet (up to 300s).
// Mirrors Python get_devices_waiting_for_all_subnets_to_have_device() at weka_runtime.py:3680
// (5s poll interval, 300s timeout, error on timeout).
func waitForSubnet(ctx context.Context, subnetStr string) ([]string, error) {
	_, logger := instrumentation.CreateLogSpan(ctx, "network.waitForSubnet")
	defer logger.End()

	_, ipNet, err := net.ParseCIDR(subnetStr)
	if err != nil {
		return nil, fmt.Errorf("network: invalid subnet %q: %w", subnetStr, err)
	}

	logger.Info("waiting for subnet to have a device", "subnet", subnetStr)

	deadline := time.Now().Add(300 * time.Second)
	for {
		devs, discoverErr := autodiscoverInSubnet(ipNet)
		switch {
		case discoverErr != nil:
			logger.Warn("autodiscover in subnet failed, will retry", "subnet", subnetStr, "err", discoverErr)
		case len(devs) > 0:
			// Mirror Python: logging.info("All subnets have devices. Subnets: %s, Devices: %s", subnets, devices)
			logger.Info("All subnets have devices.", "subnet", subnetStr, "devices", devs)
			return devs, nil
		default:
			// Mirror Python: logging.info(f"No devices found for subnet {subnet}, waiting...")
			logger.Info("No devices found for subnet, waiting...", "subnet", subnetStr)
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("network: no device found for subnet %q after 300s", subnetStr)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// filterDevicesInSubnet parses raw `ip -o addr` output and returns the names of
// interfaces whose IP falls inside ipNet. Family (inet/inet6) is inferred from
// whether ipNet.IP is an IPv4 address. Zone IDs ("%zone") and CIDR suffixes are
// stripped before parsing.
func filterDevicesInSubnet(ipAddrOutput []byte, ipNet *net.IPNet) []string {
	wantFamily := "inet"
	if ipNet.IP.To4() == nil {
		wantFamily = "inet6"
	}
	var devices []string
	for _, line := range strings.Split(string(ipAddrOutput), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		devName := parts[1]
		family := parts[2]
		ipWithCIDR := parts[3]

		if family != wantFamily {
			continue
		}
		ipStr := strings.Split(strings.Split(ipWithCIDR, "/")[0], "%")[0]
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ipNet.Contains(ip) {
			devices = append(devices, devName)
		}
	}
	return devices
}

// autodiscoverInSubnet runs ip -o addr and returns interfaces with IPs in subnet.
// Mirrors Python autodiscover_network_devices() at weka_runtime.py:2217.
func autodiscoverInSubnet(ipNet *net.IPNet) ([]string, error) {
	out, err := exec.Command("ip", "-o", "addr").Output() //nolint:gosec // ip with fixed args, no user input
	if err != nil {
		return nil, err
	}
	return filterDevicesInSubnet(out, ipNet), nil
}

// getSingleDeviceIP gets the primary IP of a network interface. When subnet is
// non-empty, it picks the address in that subnet specifically, since a device
// can carry several addresses and position alone cannot pick the right one.
// Mirrors Python get_single_device_ip() at weka_runtime.py:3647.
func getSingleDeviceIP(ctx context.Context, device, subnet string, isIPv6 bool) (string, error) {
	var script string
	switch {
	case device == "" || device == "default":
		if isIPv6 {
			script = "ip -6 addr show $(ip -6 route show default | awk '{print $5}' | head -n1) | grep 'inet6 ' | grep global | awk '{print $2}' | cut -d/ -f1"
		} else {
			script = "ip route show default | grep src | awk '/default/ {print $9}' | head -n1"
		}
	case subnet != "":
		// Parse before interpolating: subnet is unvalidated spec input going into a shell.
		_, ipNet, err := net.ParseCIDR(subnet)
		if err != nil {
			return "", fmt.Errorf("getSingleDeviceIP: invalid subnet %q: %w", subnet, err)
		}
		script = fmt.Sprintf("ip -o addr show dev %s to %s | head -n1 | awk '{print $4}' | cut -d/ -f1", device, ipNet.String())
	default:
		if isIPv6 {
			script = fmt.Sprintf("ip -6 addr show dev %s | grep -E 'inet6 (fd|2)' | head -n1 | awk '{print $2}' | cut -d/ -f1", device)
		} else {
			script = fmt.Sprintf("ip addr show dev %s | grep 'inet ' | head -n1 | awk '{print $2}' | cut -d/ -f1", device)
		}
	}

	out, err := cmdutil.Output(ctx, "sh", "-c", script)
	if err != nil {
		return "", fmt.Errorf("getSingleDeviceIP(%s): %w", device, err)
	}
	ip := strings.TrimSpace(string(out))

	// Fallback for default IPv4 device.
	if ip == "" && (device == "" || device == "default") && !isIPv6 {
		fallback := "ip -4 addr show dev $(ip route show default | awk '{print $5}') | grep inet | awk '{print $2}' | cut -d/ -f1"
		out, err = cmdutil.Output(ctx, "sh", "-c", fallback)
		if err == nil {
			ip = strings.TrimSpace(string(out))
		}
	}
	if ip == "" {
		return "", fmt.Errorf("getSingleDeviceIP(%s): empty result", device)
	}
	return ip, nil
}

// filterMissingDevices removes devices that have no IP (or no interface for rdmaOnly).
// Mirrors Python filter_out_missing_devices() at weka_runtime.py:3720.
func filterMissingDevices(ctx context.Context, names []string, rdmaOnly bool) []string {
	var available []string
	for _, name := range names {
		if rdmaOnly {
			// Just check if the interface exists.
			if err := cmdutil.Run(ctx, "ip", "link", "show", "dev", name); err == nil {
				available = append(available, name)
			}
		} else {
			ip, err := getSingleDeviceIP(ctx, name, "", false)
			if err == nil && ip != "" {
				available = append(available, name)
			}
		}
	}
	return available
}

// getContainerNetDevices reads the current net_devices from weka local resources.
func getContainerNetDevices(ctx context.Context, name string) ([]string, error) {
	out, err := cmdutil.Output(ctx, "weka", "local", "resources", "-C", name, "--json")
	if err != nil {
		return nil, err
	}
	var res struct {
		NetDevices []struct {
			Device string `json:"device"`
		} `json:"net_devices"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, err
	}
	result := make([]string, len(res.NetDevices))
	for i, d := range res.NetDevices {
		result[i] = d.Device
	}
	return result, nil
}

// ShouldAllocateVFPerIoNode reports whether the given network device string uses
// NVIDIA VF-per-IOnode topology. Mirrors Python should_allocate_vf_per_ionode()
// at weka_runtime.py:2305 ("vf_" in network_device).
func ShouldAllocateVFPerIoNode(networkDevice string) bool {
	return strings.Contains(networkDevice, "vf_")
}

// isUDP mirrors Python is_udp().
func isUDP(cfg *config.Config) bool {
	return cfg.UDPMode || strings.EqualFold(cfg.NetworkDevice, "udp")
}

// mustJSONMarshal marshals v or panics. Only used with compile-time-known string slices.
func mustJSONMarshal(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
