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
	"path/filepath"
	"strings"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/paths"
	"github.com/weka/weka-operator/internal/runtime/process"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// ManagementInput carries the fields WriteManagementIPs consumes from config.Network /
// config.Runtime.
type ManagementInput struct {
	Mode                  string
	NetworkDevice         string
	ManagementIP          string
	ManagementIPSelectors []weka.NetworkSelector
	NetworkSelectors      []weka.NetworkSelector
	Subnets               []string
	IsIPv6                bool
	UDPMode               bool
}

// ReconcileInput carries what ReconcileNetDevices needs to sync a container's net devices.
type ReconcileInput struct {
	Name      string // container name, passed as `-C`
	Devices   []string
	Selectors []weka.NetworkSelector
	Subnets   []string
	UDPMode   bool
}

// ReconcileNetDevices syncs the container's net devices, including RDMA flags, to match the
// selectors/subnets/device list. A no-op under VF-per-IOnode or UDP topologies, which manage
// devices outside `weka local resources net`. Mirrors Python reconcile_net_devices() at
// weka_runtime.py:2878–2930.
func ReconcileNetDevices(ctx context.Context, runner process.CommandRunner, in ReconcileInput) error { //nolint:gocritic // value semantics preferred over pointer churn for this cold-path config struct
	networkDeviceStr := strings.Join(in.Devices, ",")
	if ShouldAllocateVFPerIoNode(networkDeviceStr) || IsUDP(in.UDPMode, networkDeviceStr) {
		return nil
	}

	_, logger := instrumentation.CreateLogSpan(ctx, "network.ReconcileNetDevices", "container", in.Name)
	defer logger.End()

	target := make(map[string]struct{}, len(in.Devices))
	for _, d := range in.Devices {
		target[d] = struct{}{}
	}
	flags := make(map[string]deviceInfo)

	switch {
	case len(in.Selectors) > 0:
		devInfos, err := getDevicesBySelectors(ctx, runner, in.Selectors)
		if err != nil {
			return fmt.Errorf("network: get devices by selectors: %w", err)
		}
		target = make(map[string]struct{}, len(devInfos))
		for _, d := range devInfos {
			target[d.device] = struct{}{}
			flags[d.device] = d
		}
	case len(in.Subnets) > 0:
		pairs, err := getDevicesBySubnets(ctx, runner, in.Subnets)
		if err != nil {
			return fmt.Errorf("network: get devices by subnets: %w", err)
		}
		target = make(map[string]struct{}, len(pairs))
		for _, p := range pairs {
			target[p.device] = struct{}{}
		}
	}

	netDevices, rdmaDevices, err := getContainerNetDevices(ctx, runner, in.Name)
	if err != nil {
		return fmt.Errorf("network: get container net devices: %w", err)
	}
	current := make(map[string]struct{}, len(netDevices)+len(rdmaDevices))
	for d := range netDevices {
		current[d] = struct{}{}
	}
	for d := range rdmaDevices {
		current[d] = struct{}{}
	}

	toRemove := make(map[string]struct{})
	for d := range current {
		if _, ok := target[d]; !ok {
			toRemove[d] = struct{}{}
		}
	}
	toAdd := make(map[string]struct{})
	for d := range target {
		if _, ok := current[d]; !ok {
			toAdd[d] = struct{}{}
		}
	}
	// A device already present but whose selector now says disable_rdma, while it is still
	// RDMA-registered, must be removed and re-added to pick up the flag change.
	toReadd := make(map[string]struct{})
	for d := range target {
		if _, ok := current[d]; !ok {
			continue
		}
		if info, ok := flags[d]; ok && info.disableRDMA {
			if _, isRdma := rdmaDevices[d]; isRdma {
				toReadd[d] = struct{}{}
			}
		}
	}

	for d := range toRemove {
		if err := removeNetDevice(ctx, runner, in.Name, d); err != nil {
			return err
		}
	}
	for d := range toReadd {
		if err := removeNetDevice(ctx, runner, in.Name, d); err != nil {
			return err
		}
	}
	for d := range toAdd {
		if err := addNetDevice(ctx, runner, in.Name, d, flags[d]); err != nil {
			return err
		}
	}
	for d := range toReadd {
		if err := addNetDevice(ctx, runner, in.Name, d, flags[d]); err != nil {
			return err
		}
	}
	return nil
}

func removeNetDevice(ctx context.Context, runner process.CommandRunner, name, dev string) error {
	if _, err := runner.Run(ctx, process.Command{
		Path: "weka",
		Args: []string{"local", "resources", "net", "-C", name, "remove", dev},
		Log:  process.LogAll,
	}); err != nil {
		return fmt.Errorf("network: remove %s: %w", dev, err)
	}
	return nil
}

func addNetDevice(ctx context.Context, runner process.CommandRunner, name, dev string, info deviceInfo) error {
	args := []string{"local", "resources", "net", "-C", name, "add", dev}
	if info.rdmaOnly {
		args = append(args, "--rdma-only")
	}
	if info.disableRDMA {
		args = append(args, "--rdma-off")
	}
	if _, err := runner.Run(ctx, process.Command{
		Path: "weka",
		Args: args,
		Log:  process.LogAll,
	}); err != nil {
		return fmt.Errorf("network: add %s: %w", dev, err)
	}
	return nil
}

// WriteManagementIPs discovers management IPs and writes them atomically, returning the IPs
// written. Mirrors Python write_management_ips() at weka_runtime.py:3797.
func WriteManagementIPs(ctx context.Context, runner process.CommandRunner, in ManagementInput, roots paths.Roots) ([]string, error) { //nolint:gocritic // value semantics preferred over pointer churn for this cold-path config struct
	switch in.Mode {
	case "drive", "compute", "s3", "nfs", "smbw", "client", "data-services":
	default:
		return nil, nil
	}

	_, logger := instrumentation.CreateLogSpan(ctx, "network.WriteManagementIPs")
	defer logger.End()

	var ipAddresses []string

	switch {
	case in.ManagementIP != "" && ShouldAllocateVFPerIoNode(in.NetworkDevice):
		ipAddresses = []string{in.ManagementIP}

	case len(in.ManagementIPSelectors) > 0:
		devInfos, err := getDevicesBySelectors(ctx, runner, in.ManagementIPSelectors)
		if err != nil {
			return nil, fmt.Errorf("network.WriteManagementIPs selectors: %w", err)
		}
		for _, d := range devInfos {
			ip, err := getSingleDeviceIP(ctx, runner, d.device, d.subnet, in.IsIPv6)
			if err != nil {
				return nil, err
			}
			ipAddresses = append(ipAddresses, ip)
		}

	case in.NetworkDevice == "" && len(in.NetworkSelectors) > 0:
		allDevInfos, err := getDevicesBySelectors(ctx, runner, in.NetworkSelectors)
		if err != nil {
			return nil, fmt.Errorf("network.WriteManagementIPs network selectors: %w", err)
		}
		for _, d := range allDevInfos {
			if d.rdmaOnly {
				continue
			}
			ip, err := getSingleDeviceIP(ctx, runner, d.device, d.subnet, in.IsIPv6)
			if err != nil {
				return nil, err
			}
			ipAddresses = append(ipAddresses, ip)
		}
		if len(ipAddresses) == 0 {
			return nil, fmt.Errorf("network: no non-rdma-only devices available; configure managementIpsSelectors separately")
		}

	case in.NetworkDevice == "" && len(in.Subnets) > 0:
		pairs, err := getDevicesBySubnets(ctx, runner, in.Subnets)
		if err != nil {
			return nil, err
		}
		for _, p := range pairs {
			ip, err := getSingleDeviceIP(ctx, runner, p.device, p.subnet, in.IsIPv6)
			if err != nil {
				return nil, err
			}
			ipAddresses = append(ipAddresses, ip)
		}

	case IsUDP(in.UDPMode, in.NetworkDevice):
		device := in.NetworkDevice
		if device == "udp" {
			device = "default"
		}
		ip, err := getSingleDeviceIP(ctx, runner, device, "", in.IsIPv6)
		if err != nil {
			return nil, err
		}
		ipAddresses = []string{ip}

	case !strings.Contains(in.NetworkDevice, ","):
		ip, err := getSingleDeviceIP(ctx, runner, in.NetworkDevice, "", in.IsIPv6)
		if err != nil {
			return nil, err
		}
		ipAddresses = []string{ip}

	default:
		// Multiple NICs.
		devices := strings.Split(in.NetworkDevice, ",")
		for _, dev := range devices {
			ip, err := getSingleDeviceIP(ctx, runner, dev, "", in.IsIPv6)
			if err != nil {
				return nil, err
			}
			ipAddresses = append(ipAddresses, ip)
		}
	}

	if len(ipAddresses) == 0 {
		return nil, fmt.Errorf("network: failed to discover management IPs")
	}

	// Atomic write.
	finalPath := filepath.Join(roots.K8sRuntime, "management_ips")
	tmpPath := finalPath + ".tmp"
	if err := os.MkdirAll(roots.K8sRuntime, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(tmpPath, []byte(strings.Join(ipAddresses, "\n")), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return nil, err
	}
	logger.Info("management IPs written", "ips", ipAddresses)
	return ipAddresses, nil
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

// getDevicesBySelectors resolves each typed selector to its matching devices.
// Mirrors Python get_devices_by_selectors() at weka_runtime.py:3750.
func getDevicesBySelectors(ctx context.Context, runner process.CommandRunner, selectors []weka.NetworkSelector) ([]deviceInfo, error) {
	var devices []deviceInfo
	seen := make(map[string]struct{})

	for _, sel := range selectors {
		if len(sel.DeviceNames) > 0 {
			available := filterMissingDevices(ctx, runner, sel.DeviceNames, sel.RdmaOnly)
			if len(available) < sel.Min {
				return nil, fmt.Errorf("not enough devices by deviceNames: want %d, got %d", sel.Min, len(available))
			}
			if sel.Max > 0 && len(available) > sel.Max {
				available = available[:sel.Max]
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
		subnetDevs, err := waitForSubnet(ctx, runner, sel.Subnet)
		if err != nil {
			return nil, err
		}
		if len(subnetDevs) < sel.Min {
			return nil, fmt.Errorf("not enough devices in subnet %s: want %d, got %d", sel.Subnet, sel.Min, len(subnetDevs))
		}
		if sel.Max > 0 && len(subnetDevs) > sel.Max {
			subnetDevs = subnetDevs[:sel.Max]
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
func getDevicesBySubnets(ctx context.Context, runner process.CommandRunner, subnets []string) ([]devSubnetPair, error) {
	perSubnetDevices := make([][]string, len(subnets))
	for i, subnet := range subnets {
		devs, err := waitForSubnet(ctx, runner, subnet)
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
func waitForSubnet(ctx context.Context, runner process.CommandRunner, subnetStr string) ([]string, error) {
	_, logger := instrumentation.CreateLogSpan(ctx, "network.waitForSubnet")
	defer logger.End()

	_, ipNet, err := net.ParseCIDR(subnetStr)
	if err != nil {
		return nil, fmt.Errorf("network: invalid subnet %q: %w", subnetStr, err)
	}

	logger.Info("waiting for subnet to have a device", "subnet", subnetStr)

	deadline := time.Now().Add(300 * time.Second)
	for {
		devs, discoverErr := autodiscoverInSubnet(ctx, runner, ipNet)
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
func autodiscoverInSubnet(ctx context.Context, runner process.CommandRunner, ipNet *net.IPNet) ([]string, error) {
	res, err := runner.Run(ctx, process.Command{
		Path:   "ip",
		Args:   []string{"-o", "addr"},
		Output: process.Capture,
		Log:    process.LogExecution,
	})
	if err != nil {
		return nil, err
	}
	return filterDevicesInSubnet(res.Stdout, ipNet), nil
}

// getSingleDeviceIP gets the primary IP of a network interface. When subnet is
// non-empty, it picks the address in that subnet specifically, since a device
// can carry several addresses and position alone cannot pick the right one.
// Mirrors Python get_single_device_ip() at weka_runtime.py:3647.
func getSingleDeviceIP(ctx context.Context, runner process.CommandRunner, device, subnet string, isIPv6 bool) (string, error) {
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

	res, err := runner.Run(ctx, process.Shell(script))
	if err != nil {
		return "", fmt.Errorf("getSingleDeviceIP(%s): %w", device, err)
	}
	ip := strings.TrimSpace(string(res.Stdout))

	// Fallback for default IPv4 device.
	if ip == "" && (device == "" || device == "default") && !isIPv6 {
		fallback := "ip -4 addr show dev $(ip route show default | awk '{print $5}') | grep inet | awk '{print $2}' | cut -d/ -f1"
		res, err = runner.Run(ctx, process.Shell(fallback))
		if err == nil {
			ip = strings.TrimSpace(string(res.Stdout))
		}
	}
	if ip == "" {
		return "", fmt.Errorf("getSingleDeviceIP(%s): empty result", device)
	}
	return ip, nil
}

// filterMissingDevices removes devices that have no IP (or no interface for rdmaOnly).
// Mirrors Python filter_out_missing_devices() at weka_runtime.py:3720.
func filterMissingDevices(ctx context.Context, runner process.CommandRunner, names []string, rdmaOnly bool) []string {
	var available []string
	for _, name := range names {
		if rdmaOnly {
			// Just check if the interface exists.
			if _, err := runner.Run(ctx, process.Command{
				Path: "ip",
				Args: []string{"link", "show", "dev", name},
				Log:  process.LogExecution,
			}); err == nil {
				available = append(available, name)
			}
		} else {
			ip, err := getSingleDeviceIP(ctx, runner, name, "", false)
			if err == nil && ip != "" {
				available = append(available, name)
			}
		}
	}
	return available
}

// getContainerNetDevices reads the container's current net devices and RDMA-registered
// devices from `weka local resources`. Mirrors resources['net_devices'] /
// resources['rdma_devices']['devices'] read in Python reconcile_net_devices() at
// weka_runtime.py:2893-2896.
func getContainerNetDevices(ctx context.Context, runner process.CommandRunner, name string) (netDevices, rdmaDevices map[string]struct{}, err error) {
	res, err := runner.Run(ctx, process.Command{
		Path:   "weka",
		Args:   []string{"local", "resources", "-C", name, "--json"},
		Output: process.Capture,
		Log:    process.LogExecution,
	})
	if err != nil {
		return nil, nil, err
	}
	var parsed struct {
		NetDevices []struct {
			Device string `json:"device"`
		} `json:"net_devices"`
		RdmaDevices struct {
			Devices []struct {
				Name string `json:"name"`
			} `json:"devices"`
		} `json:"rdma_devices"`
	}
	if err := json.Unmarshal(res.Stdout, &parsed); err != nil {
		return nil, nil, err
	}
	netDevices = make(map[string]struct{}, len(parsed.NetDevices))
	for _, d := range parsed.NetDevices {
		netDevices[d.Device] = struct{}{}
	}
	rdmaDevices = make(map[string]struct{}, len(parsed.RdmaDevices.Devices))
	for _, d := range parsed.RdmaDevices.Devices {
		rdmaDevices[d.Name] = struct{}{}
	}
	return netDevices, rdmaDevices, nil
}

// ShouldAllocateVFPerIoNode reports whether the given network device string uses
// NVIDIA VF-per-IOnode topology. Mirrors Python should_allocate_vf_per_ionode()
// at weka_runtime.py:2305 ("vf_" in network_device).
func ShouldAllocateVFPerIoNode(networkDevice string) bool {
	return strings.Contains(networkDevice, "vf_")
}

// IsUDP mirrors Python is_udp().
func IsUDP(udpMode bool, networkDevice string) bool {
	return udpMode || strings.EqualFold(networkDevice, "udp")
}
