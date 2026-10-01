// Package weka — ensure.go implements Weka backend/client container lifecycle management.
// Mirrors ensure_weka_container, create_container, handle_existing_container,
// should_recreate_client_container at weka_runtime.py:2312–3408.
package weka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/cpuaffinity"
	"github.com/weka/weka-operator/internal/runtime/network"
	"github.com/weka/weka-operator/internal/runtime/paths"
	"github.com/weka/weka-operator/internal/runtime/process"
	v1alpha1 "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// modeCoresFlag maps mode → weka local resources/setup cores flag.
var modeCoresFlag = map[string]string{
	"compute":       "--only-compute-cores",
	"drive":         "--only-drives-cores",
	"client":        "--only-frontend-cores",
	"s3":            "--only-frontend-cores",
	"nfs":           "--only-frontend-cores",
	"smbw":          "--only-frontend-cores",
	"data-services": "--only-dataserv-cores",
}

// ContainerInput carries the dependencies and configuration shared by every weka container
// mode (backend and client, via ClientInput).
type ContainerInput struct {
	Runner process.CommandRunner
	Roots  paths.Roots

	Name string
	Mode string
	Port int

	Cores       int
	CoreIDs     config.CoreSelection
	NonDatapath config.CoreSelection
	CPUPolicy   string

	MemoryBytes int64
	DPDKBaseMiB int

	JoinIPs       []string
	ManagementIPs []string

	NetDevice    string
	NetSelectors []v1alpha1.NetworkSelector
	NetSubnets   []string
	UDPMode      bool
	Gateway      string
	Netmask      int

	BindManagementAll bool
	NvidiaVFSingleIP  *bool
	AutoRemoveTimeout int
	FailureDomain     string

	Features domain.FeatureFlags
}

// ClientInput carries EnsureClientContainer's dependencies and configuration.
type ClientInput struct {
	ContainerInput
	ImageName string
}

// EnsureBackendContainer creates or reconciles a backend (compute/drive/s3/nfs/smbw/
// data-services) container. It never issues "weka local rm": a backend container missing its
// staging resources file is an error, not a recovery-by-recreation case.
func EnsureBackendContainer(ctx context.Context, in *ContainerInput) error {
	return ensureContainer(ctx, in, false, "")
}

// EnsureClientContainer creates or reconciles a client container, recreating it when its
// base_port or restricted_client no longer match the desired config.
// Mirrors Python ensure_weka_container() for MODE=client at weka_runtime.py:2312.
func EnsureClientContainer(ctx context.Context, in *ClientInput) error {
	return ensureContainer(ctx, &in.ContainerInput, true, in.ImageName)
}

// ensureContainer implements the shared backend/client container lifecycle.
// Mirrors Python ensure_weka_container() at weka_runtime.py:2312.
func ensureContainer(ctx context.Context, in *ContainerInput, isClient bool, imageName string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "weka.ensureContainer", "name", in.Name)
	defer logger.End()

	resourcesDir := filepath.Join(in.Roots.OptWeka, "data", in.Name, "container")
	if err := os.MkdirAll(resourcesDir, 0o755); err != nil {
		return fmt.Errorf("ensureContainer: mkdir %s: %w", resourcesDir, err)
	}

	containers, err := GetContainers(ctx, in.Runner)
	if err != nil {
		return fmt.Errorf("ensureContainer: list containers: %w", err)
	}

	if len(containers) == 0 {
		logger.Info("no pre-existing containers, creating")
		if cerr := createContainer(ctx, in, isClient, imageName); cerr != nil {
			return cerr
		}
	} else {
		found, ferr := findContainerByName(containers, in.Name)
		if ferr != nil {
			return fmt.Errorf("ensureContainer: %w", ferr)
		}
		if ierr := inspectContainers(ctx, in, isClient, imageName, found, resourcesDir); ierr != nil {
			return ierr
		}
	}

	fullCores, err := reconcileCores(ctx, in)
	if err != nil {
		return fmt.Errorf("ensureContainer: find cores: %w", err)
	}

	resBytes, err := GetWekaLocalResources(ctx, in.Runner, in.Name)
	if err != nil {
		if isClient {
			// A client whose staging resources file never landed recovers the same way an
			// existing-but-unhealthy container does.
			if recErr := checkResourcesJSON(ctx, in, isClient, imageName, resourcesDir); recErr != nil {
				return fmt.Errorf("ensureContainer: recover missing resources: %w", recErr)
			}
			resBytes, err = GetWekaLocalResources(ctx, in.Runner, in.Name)
		}
		if err != nil {
			return fmt.Errorf("ensureContainer: get resources: %w", err)
		}
	}

	if isClient && shouldRecreateClientContainer(in.Port, imageName, resBytes) {
		logger.Info("recreating client container: base_port or restricted_client mismatch")
		if _, rmerr := in.Runner.Run(ctx, process.Command{Path: "weka", Args: []string{"local", "rm", in.Name, "--force"}}); rmerr != nil {
			return fmt.Errorf("ensureContainer: remove stale client container: %w", rmerr)
		}
		if cerr := createContainer(ctx, in, isClient, imageName); cerr != nil {
			return cerr
		}
		fullCores, err = reconcileCores(ctx, in)
		if err != nil {
			return fmt.Errorf("ensureContainer: find cores after recreate: %w", err)
		}
		resBytes, err = GetWekaLocalResources(ctx, in.Runner, in.Name)
		if err != nil {
			return fmt.Errorf("ensureContainer: get resources after recreate: %w", err)
		}
	}

	doc, err := ParseResourceDoc(resBytes)
	if err != nil {
		return fmt.Errorf("ensureContainer: parse resources: %w", err)
	}

	if err := patchResourceDoc(in, isClient, doc, fullCores); err != nil {
		return fmt.Errorf("ensureContainer: patch resources: %w", err)
	}

	if err := WriteResourceDoc(ctx, resourcesDir, doc); err != nil {
		return fmt.Errorf("ensureContainer: write resources: %w", err)
	}

	return reconcileNetwork(ctx, in)
}

// findContainerByName returns the container matching name, or an error listing what was found.
func findContainerByName(containers []map[string]interface{}, name string) (map[string]interface{}, error) {
	for _, c := range containers {
		if n, ok := c["name"].(string); ok && n == name {
			return c, nil
		}
	}
	names := make([]string, 0, len(containers))
	for _, c := range containers {
		if n, ok := c["name"].(string); ok && n != "" {
			names = append(names, n)
		}
	}
	return nil, fmt.Errorf("container %q not found; existing: %v", name, names)
}

// inspectContainers mirrors Python handle_existing_container(): a running container, or one
// whose runStatus isn't "Unknown", needs no action.
func inspectContainers(ctx context.Context, in *ContainerInput, isClient bool, imageName string, container map[string]interface{}, resourcesDir string) error {
	if running, ok := container["isRunning"].(bool); ok && running {
		return nil
	}
	if status, ok := container["runStatus"].(string); !ok || status != "Unknown" {
		return nil
	}
	return checkResourcesJSON(ctx, in, isClient, imageName, resourcesDir)
}

// checkResourcesJSON mirrors Python check_resources_json(): an empty resources.json is
// recovered by relinking the newest non-empty weka-resources.*.json candidate. When no
// candidate exists, a client recreates from scratch; a backend errors, since it must never
// issue "weka local rm".
func checkResourcesJSON(ctx context.Context, in *ContainerInput, isClient bool, imageName, resourcesDir string) error {
	recovered, err := relinkLatestResourcesFile(ctx, resourcesDir)
	if err != nil {
		return fmt.Errorf("checkResourcesJSON: %w", err)
	}
	if recovered {
		return nil
	}

	if !isClient {
		return fmt.Errorf("checkResourcesJSON: no recoverable resources file in %s", resourcesDir)
	}
	_, logger := instrumentation.CreateLogSpan(ctx, "weka.checkResourcesJSON.recreate", "name", in.Name)
	defer logger.End()
	if _, err := in.Runner.Run(ctx, process.Command{Path: "weka", Args: []string{"local", "stop", in.Name}}); err != nil {
		logger.Warn("stop before recreate failed (continuing)", "err", err)
	}
	if _, err := in.Runner.Run(ctx, process.Command{Path: "weka", Args: []string{"local", "rm", "--all", "--force"}}); err != nil {
		logger.Warn("rm --all before recreate failed (continuing)", "err", err)
	}
	return createContainer(ctx, in, isClient, imageName)
}

// relinkLatestResourcesFile relinks resources.json (and its .stable/.staging siblings) to the
// newest non-empty weka-resources.*.json candidate in resourcesDir, when resources.json itself
// is empty. recovered is true when no relink was needed (resources.json already non-empty) or
// the relink succeeded; recovered is false, with a nil error, when resources.json is empty and
// no relinkable candidate exists, leaving the no-candidate handling to the caller.
func relinkLatestResourcesFile(ctx context.Context, resourcesDir string) (recovered bool, err error) {
	info, err := os.Stat(filepath.Join(resourcesDir, "resources.json"))
	if err != nil {
		return false, fmt.Errorf("stat resources.json: %w", err)
	}
	if info.Size() > 0 {
		return true, nil
	}

	latest, err := findLatestResourcesFile(resourcesDir)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", resourcesDir, err)
	}
	if latest == "" {
		return false, nil
	}
	if err := LinkResourcesFile(ctx, latest, resourcesDir); err != nil {
		return false, err
	}
	return true, nil
}

// findLatestResourcesFile returns the name of the most recently modified non-empty
// weka-resources.*.json file in dir, or "" if none is found.
func findLatestResourcesFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var latest string
	var latestMod time.Time
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "weka-resources.") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		fi, err := e.Info()
		if err != nil || fi.Size() == 0 {
			continue
		}
		if fi.ModTime().After(latestMod) {
			latestMod = fi.ModTime()
			latest = e.Name()
		}
	}
	return latest, nil
}

// shouldRecreateClientContainer returns true when the client container must be recreated.
// Mirrors Python should_recreate_client_container() at weka_runtime.py:2503.
//
// DELIBERATE DEVIATION from Python (weka_runtime.py:2503-2508):
// Python unconditionally checks `restricted_client is not True`, which always triggers
// recreation on 4.2.7.64 images (they never set restricted_client=True) causing an
// infinite recreate loop. Go instead computes the expected value from the image name
// (restricted_client should be True for all images except 4.2.7.64) and only recreates
// when the actual value differs from that expectation. Do not revert to Python's logic.
func shouldRecreateClientContainer(port int, imageName string, resBytes []byte) bool {
	var res struct {
		BasePort         *int `json:"base_port"`
		RestrictedClient bool `json:"restricted_client"`
	}
	if err := json.Unmarshal(resBytes, &res); err != nil || res.BasePort == nil || *res.BasePort != port {
		return true
	}
	expectedRestricted := !strings.Contains(imageName, "4.2.7.64")
	return res.RestrictedClient != expectedRestricted
}

// createContainer builds and runs the "weka local setup container" command.
// Mirrors Python create_container() at weka_runtime.py:2312.
func createContainer(ctx context.Context, in *ContainerInput, isClient bool, imageName string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "weka.createContainer", "name", in.Name)
	defer logger.End()

	fullCores, err := reconcileCores(ctx, in)
	if err != nil {
		return fmt.Errorf("createContainer: find cores: %w", err)
	}
	coreStr := strings.Join(fullCores, ",")
	modeFlag := modeCoresFlag[in.Mode]

	const joinSecretPath = "/var/run/secrets/weka-operator/operator-user/join-secret"
	joinSecretFlag := ""
	joinSecretCmd := ""
	if _, err := os.Stat(joinSecretPath); err == nil {
		joinSecretFlag = "--join-secret"
		if isClient {
			joinSecretFlag = "--join-token"
		}
		joinSecretCmd = fmt.Sprintf("$(cat %s)", joinSecretPath)
	}

	var netStr string
	switch {
	case network.ShouldAllocateVFPerIoNode(in.NetDevice):
		devices := make([]string, 0)
		for _, dev := range strings.Split(in.NetDevice, ",") {
			bare := strings.TrimPrefix(dev, "vf_")
			devices = append(devices, "--net "+bare)
		}
		netStr = strings.Join(devices, " ") + " --management-ips " + strings.Join(in.ManagementIPs, ",")
	default:
		// UDP mode and bare-metal both start with "--net udp"; bare-metal reconcile adds NICs
		// later via ReconcileNetDevices.
		netStr = "--net udp"
	}

	parts := []string{
		"weka", "local", "setup", "container",
		"--name", in.Name,
		"--no-start", "--disable",
		"--core-ids", coreStr,
		"--cores", strconv.Itoa(in.Cores),
	}
	if modeFlag != "" {
		parts = append(parts, modeFlag)
	}
	parts = append(parts, netStr, "--base-port", strconv.Itoa(in.Port))

	if joinSecretCmd != "" {
		parts = append(parts, joinSecretFlag, joinSecretCmd)
	}
	if len(in.JoinIPs) > 0 {
		parts = append(parts, "--join-ips", strings.Join(in.JoinIPs, ","))
	}
	if isClient {
		parts = append(parts, "--client")
		if !strings.Contains(imageName, "4.2.7.64") {
			parts = append(parts, "--restricted")
		}
	}
	if in.FailureDomain != "" {
		parts = append(parts, "--failure-domain", in.FailureDomain)
	}
	if in.Mode == "data-services" {
		parts = append(parts, "--allow-mix-setting")
	}

	cmdStr := strings.Join(parts, " ")
	logger.Info("creating container", "cmd", cmdStr)
	if _, err := in.Runner.Run(ctx, process.Shell(cmdStr)); err != nil {
		return fmt.Errorf("createContainer: %w", err)
	}

	// For bare-metal (non-VF, non-UDP): reconcile net devices after creation.
	if !network.ShouldAllocateVFPerIoNode(in.NetDevice) && !network.IsUDP(in.UDPMode, in.NetDevice) {
		if err := reconcileNetwork(ctx, in); err != nil {
			return fmt.Errorf("createContainer: reconcile net devices: %w", err)
		}
	}

	return nil
}

// reconcileCores selects the CPU core IDs for the container.
func reconcileCores(ctx context.Context, in *ContainerInput) ([]string, error) {
	cpu := config.CPU{
		Cores:            in.Cores,
		CoreIDs:          in.CoreIDs,
		NonDatapathCores: in.NonDatapath,
		Policy:           in.CPUPolicy,
	}
	return cpuaffinity.FindFullCores(ctx, cpu, in.Cores)
}

// reconcileNetwork syncs the container's net devices to the desired set.
func reconcileNetwork(ctx context.Context, in *ContainerInput) error {
	var desired []string
	if in.NetDevice != "" {
		desired = strings.Split(in.NetDevice, ",")
	}
	return network.ReconcileNetDevices(ctx, in.Runner, network.ReconcileInput{
		Name:      in.Name,
		Devices:   desired,
		Selectors: in.NetSelectors,
		Subnets:   in.NetSubnets,
		UDPMode:   in.UDPMode,
	})
}

// patchResourceDoc applies the container-specific field edits to a fetched resources document.
// Mirrors the resources-dict edits in Python ensure_weka_container() at weka_runtime.py:2400-2503.
func patchResourceDoc(in *ContainerInput, isClient bool, doc *ResourceDoc, fullCores []string) error {
	if in.Mode == "s3" || in.Mode == "nfs" || in.Mode == "smbw" {
		doc.SetAllowProtocols(true)
	}
	doc.SetReserve1GHugepages(false)
	doc.SetExcludedDrivers([]string{"igb_uio"})
	doc.SetMemory(in.MemoryBytes)

	doc.SetAutoDiscoveryEnabled(false)
	doc.SetIPs(in.ManagementIPs)

	doc.SetDPDKBaseMemoryMB(in.DPDKBaseMiB)

	if in.Features.WekaManagesNonIonodeAffinity {
		if !in.NonDatapath.Auto {
			doc.SetNonDatapathCores(in.NonDatapath.IDs)
		} else if len(fullCores) > 0 {
			ndp, err := cpuaffinity.DeriveNonDatapathCores("/proc/1/status", fullCores)
			if err == nil && len(ndp) > 0 {
				sort.Ints(ndp)
				doc.SetNonDatapathCores(ndp)
			}
		}
	}

	doc.SetAutoRemoveTimeout(in.AutoRemoveTimeout)

	if len(in.JoinIPs) > 0 {
		endpoints := make([]Endpoint, 0, len(in.JoinIPs))
		for _, joinIP := range in.JoinIPs {
			ipPart, portPart, ok := strings.Cut(joinIP, ":")
			if !ok {
				continue
			}
			port, aerr := strconv.Atoi(portPart)
			if aerr != nil {
				continue
			}
			endpoints = append(endpoints, Endpoint{IP: ipPart, Port: port})
		}
		doc.SetBackendEndpoints(endpoints)
	}

	if in.Features.SupportsBindingToNotAllInterfaces {
		doc.SetRestrictListen(!in.BindManagementAll)
	}

	if in.NvidiaVFSingleIP != nil {
		doc.SetNvidiaVFSingleIP(*in.NvidiaVFSingleIP)
	}

	if in.Gateway != "" || in.Netmask != 0 {
		if network.IsUDP(in.UDPMode, in.NetDevice) {
			// Not applicable in UDP mode; ignore.
		} else {
			count, err := doc.NetDeviceCount()
			if err != nil && !errors.Is(err, ErrFieldMissing) {
				return err
			}
			if count != 1 {
				return fmt.Errorf("gateway/netmask configuration is not supported with multiple or zero NICs")
			}
			if in.Gateway != "" {
				if err := doc.SetNetDeviceGateway(0, in.Gateway); err != nil {
					return err
				}
			}
			if in.Netmask != 0 {
				if err := doc.SetNetDeviceNetmask(0, in.Netmask); err != nil {
					return err
				}
			}
		}
	}

	nodeIDs, err := doc.NodeIDs()
	if err != nil {
		if errors.Is(err, ErrFieldMissing) {
			return nil
		}
		return err
	}

	needCores := 0
	for _, id := range nodeIDs {
		roles, rerr := doc.NodeRoles(id)
		if rerr != nil {
			roles = nil
		}
		if !slices.Contains(roles, "MANAGEMENT") {
			needCores++
		}
	}
	if needCores > len(fullCores) {
		return fmt.Errorf("not enough cores: %d nodes need a core, %d cores allocated", needCores, len(fullCores))
	}

	coresCursor := 0
	for _, id := range nodeIDs {
		roles, rerr := doc.NodeRoles(id)
		if rerr != nil {
			roles = nil
		}
		if slices.Contains(roles, "MANAGEMENT") {
			continue
		}
		dedicate := true
		dedicatedMode := ""
		if in.CPUPolicy == "shared" {
			dedicate = false
			dedicatedMode = "NONE"
		}
		coreID, convErr := strconv.Atoi(fullCores[coresCursor])
		if convErr == nil {
			if err := doc.SetNodeCore(id, coreID, dedicate, dedicatedMode); err != nil {
				return err
			}
		}
		coresCursor++
	}

	return nil
}

// LinkResourcesFile atomically points resources.json / resources.json.stable /
// resources.json.staging at fileName, replicating `ln -sf fileName dir/link` for each without
// shelling out. ctx is accepted only for external signature compatibility (see ssdproxy.go).
func LinkResourcesFile(_ context.Context, fileName, resourcesDir string) error {
	for _, link := range resourceLinkNames {
		if err := relink(resourcesDir, fileName, link); err != nil {
			return fmt.Errorf("LinkResourcesFile: %w", err)
		}
	}
	return nil
}

// StartContainer starts the named container.
// Mirrors Python start_weka_container() at weka_runtime.py:2827.
func StartContainer(ctx context.Context, runner process.CommandRunner, name string) error {
	_, err := runner.Run(ctx, process.Command{Path: "weka", Args: []string{"local", "start", name}})
	return err
}

// GetContainers runs "weka local ps --json" and returns the parsed array.
func GetContainers(ctx context.Context, runner process.CommandRunner) ([]map[string]interface{}, error) {
	res, err := runner.Run(ctx, process.Command{Path: "weka", Args: []string{"local", "ps", "--json"}})
	if err != nil {
		return nil, fmt.Errorf("GetContainers: %w", err)
	}
	var result []map[string]interface{}
	if err := json.Unmarshal(res.Stdout, &result); err != nil {
		return nil, fmt.Errorf("GetContainers: parse JSON: %w", err)
	}
	return result, nil
}

// GetWekaLocalResources runs "weka local resources -C name --json" and returns the raw stdout,
// letting callers parse it directly into a ResourceDoc without a lossy map round-trip.
func GetWekaLocalResources(ctx context.Context, runner process.CommandRunner, name string) ([]byte, error) {
	res, err := runner.Run(ctx, process.Command{
		Path: "weka",
		Args: []string{"local", "resources", "-C", name, "--json"},
		Log:  process.LogExecution,
	})
	if err != nil {
		return nil, fmt.Errorf("GetWekaLocalResources(%s): %w", name, err)
	}
	return res.Stdout, nil
}
