// Package weka — ensure.go implements Weka container lifecycle management.
// Mirrors ensure_weka_container, create_container, handle_existing_container,
// should_recreate_client_container, write_feature_flags_json, write_telemetry_config_override
// at weka_runtime.py:2312–3408.
package weka

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/cpuaffinity"
	"github.com/weka/weka-operator/internal/runtime/network"
	"github.com/weka/weka-operator/internal/runtime/resources"
)

// modeCoresFlag maps mode → weka local resources cores flag.
var modeCoresFlag = map[string]string{
	"compute":       "--only-compute-cores",
	"drive":         "--only-drives-cores",
	"client":        "--only-frontend-cores",
	"s3":            "--only-frontend-cores",
	"nfs":           "--only-frontend-cores",
	"smbw":          "--only-frontend-cores",
	"data-services": "--only-dataserv-cores",
}

// EnsureWekaContainer ensures the named Weka container exists and is configured.
// Mirrors Python ensure_weka_container() at weka_runtime.py:2644.
func EnsureWekaContainer(ctx context.Context, cfg *config.Config, res *resources.NodeResources) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "weka.EnsureWekaContainer", "name", cfg.Name)
	defer logger.End()

	resourcesDir := fmt.Sprintf("/opt/weka/data/%s/container", cfg.Name)
	if err := os.MkdirAll(resourcesDir, 0o755); err != nil {
		return fmt.Errorf("EnsureWekaContainer mkdir: %w", err)
	}

	containers, err := getContainers(ctx)
	if err != nil {
		return err
	}

	if len(containers) == 0 {
		logger.Info("no pre-existing containers, creating")
		if createErr := createContainer(ctx, cfg); createErr != nil {
			return createErr
		}
	} else {
		var found map[string]interface{}
		for _, c := range containers {
			name, ok := c["name"].(string)
			if ok && name == cfg.Name {
				found = c
				break
			}
		}
		if found == nil {
			names := make([]string, 0, len(containers))
			for _, c := range containers {
				n, ok := c["name"].(string)
				if ok && n != "" {
					names = append(names, n)
				}
			}
			return fmt.Errorf("EnsureWekaContainer: container with name %q not found; existing: %v", cfg.Name, names)
		}
		if handleErr := handleExistingContainer(ctx, cfg, found, resourcesDir); handleErr != nil {
			return handleErr
		}
	}

	// Get number of cores for this container.
	numCores := numCoresForMode(cfg)
	var fullCores []string
	var localRes map[string]interface{}
	fullCores, err = cpuaffinity.FindFullCores(ctx, cfg, numCores)
	if err != nil {
		return fmt.Errorf("EnsureWekaContainer: find full cores: %w", err)
	}

	localRes, err = getWekaLocalResources(ctx, cfg.Name)
	if err != nil {
		if cfg.Mode == "client" && strings.Contains(err.Error(), "resources.json.staging: No such file or directory") {
			logger.Warn("client container corrupted state, recreating", "err", err)
			if stopErr := cmdutil.Run(ctx, "weka", "local", "stop", "--force"); stopErr != nil {
				logger.Warn("weka local stop --force failed during recreate (continuing)", "err", stopErr)
			}
			if rmErr := cmdutil.Run(ctx, "weka", "local", "rm", cfg.Name, "--force"); rmErr != nil {
				logger.Warn("weka local rm --force failed during recreate (continuing)", "err", rmErr)
			}
			if err2 := createContainer(ctx, cfg); err2 != nil {
				return err2
			}
			localRes, err = getWekaLocalResources(ctx, cfg.Name)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}

	if cfg.Mode == "client" && shouldRecreateClientContainer(cfg, localRes) {
		logger.Info("recreating client container")
		if stopErr := cmdutil.Run(ctx, "weka", "local", "stop", "--force"); stopErr != nil {
			logger.Warn("weka local stop --force failed during recreate (continuing)", "err", stopErr)
		}
		if rmErr := cmdutil.Run(ctx, "weka", "local", "rm", cfg.Name, "--force"); rmErr != nil {
			logger.Warn("weka local rm --force failed during recreate (continuing)", "err", rmErr)
		}
		if createErr := createContainer(ctx, cfg); createErr != nil {
			return createErr
		}
		localRes, err = getWekaLocalResources(ctx, cfg.Name)
		if err != nil {
			return err
		}
	}

	// Reconfigure core count if needed.
	if coresFlag, ok := modeCoresFlag[cfg.Mode]; ok {
		nodes, nodesOK := localRes["nodes"].(map[string]interface{})
		if !nodesOK || len(nodes) != numCores+1 {
			coreIDs := fullCores
			if len(coreIDs) > numCores {
				coreIDs = coreIDs[:numCores]
			}
			args := []string{"local", "resources", "cores", strconv.Itoa(numCores),
				"-C", cfg.Name, coresFlag, "--core-ids", strings.Join(coreIDs, ",")}
			if coresErr := cmdutil.Run(ctx, "weka", args...); coresErr != nil {
				return fmt.Errorf("EnsureWekaContainer: reconfigure cores: %w", coresErr)
			}
		}
	}

	// Re-fetch after potential core change.
	localRes, err = getWekaLocalResources(ctx, cfg.Name)
	if err != nil {
		return err
	}

	// Patch resource fields.
	if cfg.Mode == "s3" || cfg.Mode == "nfs" || cfg.Mode == "smbw" {
		localRes["allow_protocols"] = true
	}
	localRes["reserve_1g_hugepages"] = false
	localRes["excluded_drivers"] = []string{"igb_uio"}
	if cfg.Memory != "" {
		if memBytes, memErr := convertToBytes(cfg.Memory); memErr == nil {
			localRes["memory"] = memBytes
		} else {
			logger.Warn("failed to parse memory value, skipping", "memory", cfg.Memory, "err", memErr)
		}
	}
	localRes["auto_discovery_enabled"] = false
	localRes["ips"] = network.ManagementIPs

	dpdk := cfg.DPDKBaseMemMB
	if dpdk == 0 {
		dpdk = 64
	}
	localRes["dpdk_base_memory_mb"] = dpdk
	localRes["auto_remove_timeout"] = cfg.AutoRemoveTimeout

	// Join IPs / backend endpoints.
	if len(cfg.JoinIPs) > 0 {
		var endpoints []map[string]interface{}
		for _, joinIP := range cfg.JoinIPs {
			parts := strings.SplitN(joinIP, ":", 2)
			if len(parts) != 2 {
				continue
			}
			port, perr := strconv.Atoi(parts[1])
			if perr != nil {
				logger.Warn("invalid join IP port, using 0", "joinIP", joinIP, "err", perr)
			}
			endpoints = append(endpoints, map[string]interface{}{"ip": parts[0], "port": port})
		}
		localRes["backend_endpoints"] = endpoints
	}

	// Binding restriction.
	if cfg.Features.SupportsBindingToNotAllInterfaces {
		localRes["restrict_listen"] = !cfg.BindManagementAll
	}

	// NVIDIA VF single IP.
	localRes["nvidia_vf_single_ip"] = cfg.NvidiaVFSingleIP

	// Net gateway.
	if cfg.NetGateway != "" && !isUDP(cfg) {
		netDevs, ok := localRes["net_devices"].([]interface{})
		if ok && len(netDevs) == 1 {
			if devMap, ok := netDevs[0].(map[string]interface{}); ok {
				devMap["gateway"] = cfg.NetGateway
			}
		}
	}

	// Assign core IDs to nodes.
	nodes, nodesOK := localRes["nodes"].(map[string]interface{})
	coresCursor := 0
	if nodesOK {
		for _, nodeVal := range nodes {
			node, ok := nodeVal.(map[string]interface{})
			if !ok {
				continue
			}
			roles, rolesOK := node["roles"].([]interface{})
			isManagement := false
			if rolesOK {
				for _, r := range roles {
					s, ok := r.(string)
					if ok && s == "MANAGEMENT" {
						isManagement = true
						break
					}
				}
			}
			if isManagement {
				continue
			}
			if cfg.CPUPolicy == "shared" {
				node["dedicate_core"] = false
				node["dedicated_mode"] = "NONE"
			} else {
				node["dedicate_core"] = true
			}
			if coresCursor < len(fullCores) {
				coreID, coreIDErr := strconv.Atoi(fullCores[coresCursor])
				if coreIDErr != nil {
					logger.Warn("invalid core ID, skipping", "coreID", fullCores[coresCursor], "err", coreIDErr)
				} else {
					node["core_id"] = coreID
				}
				coresCursor++
			}
		}
	}

	// Atomic write of patched resources.
	resourceGen := fmt.Sprintf("%x", time.Now().UnixNano())
	fileName := fmt.Sprintf("weka-resources.%s.json", resourceGen)
	resourceFile := filepath.Join(resourcesDir, fileName)
	data, err := json.Marshal(localRes)
	if err != nil {
		return fmt.Errorf("EnsureWekaContainer: marshal resources: %w", err)
	}
	if err := os.WriteFile(resourceFile, data, 0o644); err != nil {
		return fmt.Errorf("EnsureWekaContainer: write resources: %w", err)
	}
	if err := linkResourcesFile(ctx, fileName, resourcesDir); err != nil {
		return err
	}

	// Reconcile net devices.
	desired := desiredNetDevices(cfg)
	if err := network.ReconcileNetDevices(ctx, cfg.Name, desired); err != nil {
		return fmt.Errorf("EnsureWekaContainer: reconcile net devices: %w", err)
	}

	return nil
}

// EnsureWekaVersion sets the active Weka version if not already set.
// Mirrors Python ensure_weka_version() at weka_runtime.py:2913.
func EnsureWekaVersion(ctx context.Context) error {
	return cmdutil.Run(ctx, "sh", "-c", "weka version | grep '*' || weka version set $(weka version)")
}

// ForceSetWekaVersion unconditionally pins the active Weka version.
// Used by ssdproxy mode (force_set=True) after creating the proxy container.
func ForceSetWekaVersion(ctx context.Context) error {
	return cmdutil.Run(ctx, "sh", "-c", "weka version set $(weka version)")
}

// WriteFeatureFlagsJSON atomically writes feature flags to /opt/weka/k8s-runtime/feature_flags.json.
// Mirrors Python write_feature_flags_json() at weka_runtime.py:1401.
func WriteFeatureFlagsJSON(ctx context.Context, cfg *config.Config) error {
	data, err := json.Marshal(cfg.Features)
	if err != nil {
		return err
	}
	const tmp = "/opt/weka/k8s-runtime/feature_flags.json.tmp"
	const dst = "/opt/weka/k8s-runtime/feature_flags.json"
	if err := os.MkdirAll("/opt/weka/k8s-runtime", 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// WriteTelemetryConfigOverride writes the telemetry audit-traces config override atomically.
// Mirrors Python write_telemetry_config_override() at weka_runtime.py:3344.
func WriteTelemetryConfigOverride(ctx context.Context) error {
	const auditDir = "/opt/weka/external-mounts/shared_boot_level/audit-traces"
	const configPath = auditDir + "/override.config.json"

	if _, err := os.Stat(auditDir); os.IsNotExist(err) {
		return nil
	}

	// Get filesystem stats to compute minimumFreeSpace.
	var stat syscall.Statfs_t
	minimumFreeSpace := int64(5368709120) // fallback ~5GiB
	if err := syscall.Statfs(auditDir, &stat); err == nil {
		// Python uses f_blocks * f_frsize (weka_runtime.py:3360); Bsize is f_bsize which may differ
		// on some NFS/btrfs mounts.  statfsFragmentSize returns Frsize on Linux (f_frsize).
		total := int64(stat.Blocks) * statfsFragmentSize(&stat)
		minimumFreeSpace = total * 20 / 100
	}

	const tracesRetentionSize = 10 * 1024 * 1024 * 1024 // 10 GiB

	configOverride := map[string]interface{}{
		"global": map[string]interface{}{
			"dumping": map[string]interface{}{
				"histogramRetentionSize": 134217728, // 128 MiB
				"maxHistograms":          30000,
				"minimumFreeSpace":       minimumFreeSpace,
				"tracesRetentionSize":    tracesRetentionSize,
			},
		},
	}

	newContent, err := json.Marshal(configOverride)
	if err != nil {
		return err
	}

	// Idempotent check.
	if existing, err := os.ReadFile(configPath); err == nil {
		if bytes.Equal(existing, newContent) {
			return nil
		}
	}

	tmpPath := fmt.Sprintf("%s/.config.json.tmp.%d", auditDir, os.Getpid())
	if err := os.WriteFile(tmpPath, newContent, 0o644); err != nil {
		return fmt.Errorf("WriteTelemetryConfigOverride write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath) //nolint:errcheck // best-effort cleanup of orphaned temp file
		return fmt.Errorf("WriteTelemetryConfigOverride rename: %w", err)
	}
	return nil
}

// StartContainer starts the named container.
// Mirrors Python start_weka_container() at weka_runtime.py:2827.
func StartContainer(ctx context.Context, name string) error {
	return cmdutil.Run(ctx, "weka", "local", "start", name)
}

// ---- unexported helpers ----------------------------------------------------------------

// getContainers runs "weka local ps --json" and returns the parsed array.
func getContainers(ctx context.Context) ([]map[string]interface{}, error) {
	out, err := cmdutil.Output(ctx, "weka", "local", "ps", "--json")
	if err != nil {
		return nil, fmt.Errorf("getContainers: %w", err)
	}
	var result []map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("getContainers: parse JSON: %w", err)
	}
	return result, nil
}

// getWekaLocalResources runs "weka local resources -C name --json" and returns the parsed map.
func getWekaLocalResources(ctx context.Context, name string) (map[string]interface{}, error) {
	out, err := cmdutil.Output(ctx, "weka", "local", "resources", "-C", name, "--json")
	if err != nil {
		return nil, fmt.Errorf("getWekaLocalResources(%s): %w", name, err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("getWekaLocalResources(%s): parse JSON: %w", name, err)
	}
	return result, nil
}

// shouldRecreateClientContainer returns true when the client container must be recreated.
// Mirrors Python should_recreate_client_container() at weka_runtime.py:2503.
//
// DELIBERATE DEVIATION from Python (weka_runtime.py:2503-2508):
// Python unconditionally checks `restricted_client is not True`, which always triggers
// recreation on 4.2.7.64 images (they never set restricted_client=True) causing an
// infinite recreate loop.  Go instead computes the expected value from the image name
// (restricted_client should be True for all images except 4.2.7.64) and only recreates
// when the actual value differs from that expectation.  Do not revert to Python's logic.
func shouldRecreateClientContainer(cfg *config.Config, res map[string]interface{}) bool {
	// base_port: zero on missing/wrong type → mismatch with any real port → recreate.
	basePort, basePortOK := res["base_port"].(float64)
	if !basePortOK || int(basePort) != cfg.Port {
		return true
	}
	expectedRestricted := !strings.Contains(cfg.ImageName, "4.2.7.64")
	// restricted_client: false on missing/wrong type is the safe default for the comparison.
	restricted, restrictedOK := res["restricted_client"].(bool)
	if !restrictedOK {
		restricted = false
	}
	return restricted != expectedRestricted
}

// handleExistingContainer handles a container that already exists.
// Mirrors Python handle_existing_container() at weka_runtime.py:2571.
func handleExistingContainer(ctx context.Context, cfg *config.Config, container map[string]interface{}, resourcesDir string) error {
	running, runningOK := container["isRunning"].(bool)
	if runningOK && running {
		return nil
	}
	status, statusOK := container["runStatus"].(string)
	if statusOK && status == "Unknown" {
		return checkResourcesJSON(ctx, cfg, resourcesDir)
	}
	return nil
}

// checkResourcesJSON handles empty resources file by restoring from a backup or recreating.
// Mirrors Python check_resources_json() at weka_runtime.py:2538.
func checkResourcesJSON(ctx context.Context, cfg *config.Config, resourcesDir string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "weka.checkResourcesJSON")
	defer logger.End()

	resourcesFile := filepath.Join(resourcesDir, "resources.json")
	info, err := os.Stat(resourcesFile)
	if err != nil {
		return fmt.Errorf("checkResourcesJSON: %w", err)
	}
	if info.Size() > 0 {
		return nil // not empty, nothing to do
	}

	// Find older non-empty weka-resources.*.json files.
	entries, err := os.ReadDir(resourcesDir)
	if err != nil {
		return err
	}
	var candidates []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "weka-resources.") && strings.HasSuffix(e.Name(), ".json") {
			full := filepath.Join(resourcesDir, e.Name())
			if fi, err := os.Stat(full); err == nil && fi.Size() > 0 {
				candidates = append(candidates, e.Name())
			}
		}
	}
	if len(candidates) == 0 {
		// Recreate container.
		if stopErr := cmdutil.Run(ctx, "weka", "local", "stop", "--force"); stopErr != nil {
			logger.Warn("weka local stop --force failed during recreate (continuing)", "err", stopErr)
		}
		if rmErr := cmdutil.Run(ctx, "weka", "local", "rm", "--all", "--force"); rmErr != nil {
			logger.Warn("weka local rm --all --force failed during recreate (continuing)", "err", rmErr)
		}
		return createContainer(ctx, cfg)
	}
	// Link the most-recently-modified candidate.
	var latest string
	var latestMod time.Time
	for _, name := range candidates {
		if fi, err := os.Stat(filepath.Join(resourcesDir, name)); err == nil {
			if fi.ModTime().After(latestMod) {
				latestMod = fi.ModTime()
				latest = name
			}
		}
	}
	return linkResourcesFile(ctx, latest, resourcesDir)
}

// linkResourcesFile creates the standard symlinks for a resource file.
// Mirrors Python link_resources_file() at weka_runtime.py:2581.
func linkResourcesFile(_ context.Context, fileName, resourcesDir string) error {
	script := fmt.Sprintf(`
ln -sf %s %s/resources.json
ln -sf %s %s/resources.json.stable
ln -sf %s %s/resources.json.staging
`, fileName, resourcesDir,
		fileName, resourcesDir,
		fileName, resourcesDir)
	cmd := fmt.Sprintf("cd %s && %s", resourcesDir, script)
	return cmdutil.Run(context.Background(), "sh", "-c", cmd)
}

// createContainer builds and runs the "weka local setup container" command.
// Mirrors Python create_container() at weka_runtime.py:2312.
func createContainer(ctx context.Context, cfg *config.Config) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "weka.createContainer", "name", cfg.Name)
	defer logger.End()

	numCores := numCoresForMode(cfg)
	fullCores, err := cpuaffinity.FindFullCores(ctx, cfg, numCores)
	if err != nil {
		return fmt.Errorf("createContainer: find cores: %w", err)
	}
	coreStr := strings.Join(fullCores, ",")
	modeFlag := modeCoresFlag[cfg.Mode]

	// Join secret.
	joinSecretFlag := ""
	joinSecretCmd := ""
	if _, err := os.Stat("/var/run/secrets/weka-operator/operator-user/join-secret"); err == nil {
		joinSecretFlag = "--join-secret"
		if cfg.Mode == "client" {
			joinSecretFlag = "--join-token"
		}
		joinSecretCmd = "$(cat /var/run/secrets/weka-operator/operator-user/join-secret)"
	}

	// Network flags.
	var netStr string
	switch {
	case network.ShouldAllocateVFPerIoNode(cfg.NetworkDevice):
		devices := make([]string, 0)
		for _, dev := range strings.Split(cfg.NetworkDevice, ",") {
			bare := strings.TrimPrefix(dev, "vf_")
			devices = append(devices, "--net "+bare)
		}
		netStr = strings.Join(devices, " ") + " --management-ips " + strings.Join(network.ManagementIPs, ",")
	default:
		// UDP mode and bare-metal both start with "--net udp";
		// bare-metal reconcile adds NICs later via ReconcileNetDevices.
		netStr = "--net udp"
	}

	// Build command parts.
	parts := []string{
		"weka", "local", "setup", "container",
		"--name", cfg.Name,
		"--no-start", "--disable",
		"--core-ids", coreStr,
		"--cores", strconv.Itoa(numCores),
	}
	if modeFlag != "" {
		parts = append(parts, strings.Fields(modeFlag)...)
	}
	parts = append(parts, strings.Fields(netStr)...)
	parts = append(parts, "--base-port", strconv.Itoa(cfg.Port))

	if joinSecretCmd != "" {
		parts = append(parts, joinSecretFlag, joinSecretCmd)
	}
	if len(cfg.JoinIPs) > 0 {
		parts = append(parts, "--join-ips", strings.Join(cfg.JoinIPs, ","))
	}
	if cfg.Mode == "client" {
		parts = append(parts, "--client")
		if !strings.Contains(cfg.ImageName, "4.2.7.64") {
			parts = append(parts, "--restricted")
		}
	}
	if cfg.FailureDomain != "" {
		parts = append(parts, "--failure-domain", cfg.FailureDomain)
	}
	if cfg.Mode == "data-services" {
		parts = append(parts, "--allow-mix-setting")
	}

	// Run via shell to allow $(cat ...) expansion.
	cmdStr := strings.Join(parts, " ")
	logger.Info("creating container", "cmd", cmdStr)
	if err := cmdutil.Run(ctx, "sh", "-c", cmdStr); err != nil {
		return fmt.Errorf("createContainer: %w", err)
	}

	// For bare-metal (non-VF, non-UDP): reconcile net devices after creation.
	if !network.ShouldAllocateVFPerIoNode(cfg.NetworkDevice) && !isUDP(cfg) {
		desired := desiredNetDevices(cfg)
		if err := network.ReconcileNetDevices(ctx, cfg.Name, desired); err != nil {
			return fmt.Errorf("createContainer: reconcile net devices: %w", err)
		}
	}

	return nil
}

// desiredNetDevices computes the list of net devices the container should have.
func desiredNetDevices(cfg *config.Config) []string {
	if cfg.NetworkDevice == "" {
		return nil
	}
	return strings.Split(cfg.NetworkDevice, ",")
}

// numCoresForMode returns the number of cores for the current config.
// Python: NUM_CORES = int(os.environ.get("CORES", 0)) and the list is per-role.
func numCoresForMode(cfg *config.Config) int {
	if len(cfg.Cores) > 0 {
		return cfg.Cores[0]
	}
	return 0
}

// isUDP mirrors Python is_udp().
func isUDP(cfg *config.Config) bool {
	return cfg.UDPMode || strings.EqualFold(cfg.NetworkDevice, "udp")
}

// convertToBytes parses a human-readable size string like "1GiB", "512MiB" into bytes.
// Mirrors Python convert_to_bytes() at weka_runtime.py:2511.
func convertToBytes(memory string) (int64, error) {
	upper := strings.ToUpper(strings.TrimSpace(memory))
	re := regexp.MustCompile(`^(\d+)([KMGTPE]I?B?)$`)
	matches := re.FindStringSubmatch(upper)
	if len(matches) != 3 {
		return 0, fmt.Errorf("invalid size: %q", memory)
	}
	size, parseErr := strconv.ParseInt(matches[1], 10, 64)
	if parseErr != nil {
		return 0, fmt.Errorf("convertToBytes: parse size %q: %w", matches[1], parseErr)
	}
	multipliers := map[string]int64{
		"B":  1,
		"KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15, "EB": 1e18,
		"KIB": 1 << 10, "MIB": 1 << 20, "GIB": 1 << 30,
		"TIB": 1 << 40, "PIB": 1 << 50,
	}
	unit := matches[2]
	mult, ok := multipliers[unit]
	if !ok {
		return 0, fmt.Errorf("unknown unit: %q", unit)
	}
	return size * mult, nil
}
