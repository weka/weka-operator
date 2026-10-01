package wekadrive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/blockdev"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

const ssdProxySocketPath = "/host-binds/ssdproxy-local-socket/container.sock"

// SignOptions controls weka-sign-drive signing flags.
type SignOptions struct {
	AllowEraseWekaPartitions    bool
	AllowEraseNonWekaPartitions bool
	AllowNonEmptyDevice         bool
	SkipTrimFormat              bool
}

// buildSignFlags returns the CLI flags corresponding to the given options.
func buildSignFlags(opts *SignOptions) []string {
	if opts == nil {
		return nil
	}
	var flags []string
	if opts.AllowEraseWekaPartitions {
		flags = append(flags, "--allow-erase-weka-partitions")
	}
	if opts.AllowEraseNonWekaPartitions {
		flags = append(flags, "--allow-erase-non-weka-partitions")
	}
	if opts.AllowNonEmptyDevice {
		flags = append(flags, "--allow-non-empty-device")
	}
	if opts.SkipTrimFormat {
		flags = append(flags, "--skip-trim-format")
	}
	return flags
}

// signDriveListOutput matches the JSON produced by `weka-sign-drive list -j`.
type signDriveListOutput struct {
	Devices []signDriveDevice `json:"devices"`
}

type signDriveDevice struct {
	Hardware     signDriveHardware  `json:"hardware"`
	WekaInfo     *signDriveWekaInfo `json:"weka_info"`
	Path         string             `json:"path"`
	Status       string             `json:"status"`
	PhysicalUUID string             `json:"physical_uuid"`
}

type signDriveHardware struct {
	SerialNumber string `json:"serial_number"`
	Path         string `json:"path"`
	Model        string `json:"model"`
	ModelNumber  string `json:"model_number"`
	IuSize       int    `json:"iu_size"`
	SizeBytes    int64  `json:"size_bytes"`
}

type signDriveWekaInfo struct {
	ClusterGUID string `json:"cluster_guid"`
	IsProxy     bool   `json:"is_proxy"`
}

// parseSignDriveListJSON is a thin helper that unmarshals raw JSON into a signDriveListOutput.
// It is used by both the production callers and tests.
func parseSignDriveListJSON(data []byte, out *signDriveListOutput) error {
	return json.Unmarshal(data, out)
}

// filterClusterGUIDDrives returns a serial→path map from a parsed list, keeping only devices
// that have a non-empty WekaInfo.ClusterGUID.  Devices with empty serial, empty path, or nil
// WekaInfo are skipped.  hardware.Path takes priority over the top-level path field.
func filterClusterGUIDDrives(parsed signDriveListOutput) map[string]string {
	result := make(map[string]string, len(parsed.Devices))
	for _, dev := range parsed.Devices {
		// M9 review: plan stated Python reads top-level device['serial'], but the actual
		// Python code at weka_runtime.py:513-515 reads hardware.get('serial_number') —
		// identical to dev.Hardware.SerialNumber here.  No change needed; already correct.
		serial := dev.Hardware.SerialNumber
		path := dev.Hardware.Path
		if path == "" {
			path = dev.Path
		}
		if serial == "" || path == "" {
			continue
		}
		if dev.WekaInfo == nil || dev.WekaInfo.ClusterGUID == "" {
			continue
		}
		result[serial] = path
	}
	return result
}

// extractProxyDrives returns SharedDriveInfo for every proxy-signed drive in a parsed list.
// A drive qualifies when:
//   - status == "weka_formatted"
//   - WekaInfo != nil AND isProxySigned(WekaInfo)
//   - PhysicalUUID is non-empty
//   - SizeBytes > 0
func extractProxyDrives(ctx context.Context, parsed signDriveListOutput) []domain.SharedDriveInfo {
	logger := instrumentation.CurrentSpanLogger(ctx)
	var drives []domain.SharedDriveInfo
	for _, dev := range parsed.Devices {
		if dev.Status != "weka_formatted" {
			continue
		}
		if dev.WekaInfo == nil {
			continue
		}
		if !isProxySigned(dev.WekaInfo) {
			continue
		}
		if dev.PhysicalUUID == "" {
			continue
		}
		if dev.Hardware.Model == "" {
			logger.Warn("proxy drive has empty model", "serial", dev.Hardware.SerialNumber, "path", dev.Path)
		}
		if dev.Hardware.SizeBytes <= 0 {
			logger.Error(fmt.Errorf("invalid size_bytes: %d", dev.Hardware.SizeBytes), "proxy drive has invalid size_bytes, skipping", "serial", dev.Hardware.SerialNumber, "path", dev.Path)
			continue
		}
		drives = append(drives, domain.SharedDriveInfo{
			PhysicalUUID: dev.PhysicalUUID,
			Serial:       dev.Hardware.SerialNumber,
			CapacityGiB:  int(dev.Hardware.SizeBytes / (1024 * 1024 * 1024)),
			Type:         iuSizeToDriveType(dev.Hardware.IuSize),
			Model:        blockdev.ResolveDriveModel(dev.Hardware.Model, dev.Hardware.ModelNumber, dev.Path),
		})
	}
	return drives
}

// listDevicesWithSignTool runs `weka-sign-drive list -j` (using the proxy socket if requested and
// present) and returns the parsed devices array. Mirrors Python _list_devices_with_sign_tool().
func listDevicesWithSignTool(ctx context.Context, useProxySocket bool) ([]signDriveDevice, error) {
	logger := instrumentation.CurrentSpanLogger(ctx)

	args := []string{}
	if useProxySocket {
		if _, err := os.Stat(ssdProxySocketPath); err == nil {
			logger.Info("using proxy socket", "socket", ssdProxySocketPath)
			args = append(args, "--unix-socket", ssdProxySocketPath+":/api/v1")
		}
	}
	args = append(args, "list", "-j")

	out, err := cmdutil.Output(ctx, "/weka-sign-drive", args...)
	if err != nil {
		return nil, fmt.Errorf("weka-sign-drive list: %w", err)
	}

	// Skip any non-JSON preamble (matches Python json_start = output_text.find('{'))
	jsonStart := bytes.IndexByte(out, '{')
	if jsonStart < 0 {
		return nil, fmt.Errorf("weka-sign-drive list: no JSON in output")
	}

	var parsed signDriveListOutput
	if jsonErr := parseSignDriveListJSON(out[jsonStart:], &parsed); jsonErr != nil {
		return nil, fmt.Errorf("weka-sign-drive list: JSON parse: %w", jsonErr)
	}
	return parsed.Devices, nil
}

// GetDrivesWithClusterGUID runs `weka-sign-drive list -j` and returns a map of serial → path
// for drives that have a cluster_guid (i.e. are claimed by a Weka cluster).
// If useProxySocket is true and the socket file exists, the proxy socket is used.
func GetDrivesWithClusterGUID(ctx context.Context, useProxySocket bool) (map[string]string, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "GetDrivesWithClusterGUID")
	defer logger.End()

	devices, err := listDevicesWithSignTool(ctx, useProxySocket)
	if err != nil {
		logger.Warn("list failed", "err", err)
		return map[string]string{}, nil
	}

	result := filterClusterGUIDDrives(signDriveListOutput{Devices: devices})
	logger.Info("done", "count", len(result))
	return result, nil
}

// proxySignedGUID is the sentinel cluster_guid that weka-sign-drive assigns to
// proxy-signed drives before they are added to a proxy cluster.
const proxySignedGUID = "026938d8-a8a2-4ad4-a316-2f23358a1e7a"

// ListAllProxyDrives runs `weka-sign-drive list -j` (using the proxy socket if available)
// and returns SharedDriveInfo for every proxy-signed drive currently visible on the node.
// Mirrors Python list_weka_proxy_drives_with_sign_tool().
func ListAllProxyDrives(ctx context.Context) ([]domain.SharedDriveInfo, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ListAllProxyDrives")
	defer logger.End()

	devices, err := listDevicesWithSignTool(ctx, true)
	if err != nil {
		return nil, err
	}

	drives := extractProxyDrives(ctx, signDriveListOutput{Devices: devices})
	logger.Info("done", "count", len(drives))
	return drives, nil
}

// isProxySigned reports whether the device is signed for ssdproxy, including the sentinel
// cluster_guid values weka-sign-drive reports before the drive is added to a proxy.
// Mirrors Python is_proxy_signed().
func isProxySigned(info *signDriveWekaInfo) bool {
	if info == nil {
		return false
	}
	clusterGUID := strings.ToLower(info.ClusterGUID)
	return clusterGUID == proxySignedGUID || clusterGUID == "proxy guid" || info.IsProxy
}

// SignToolDrive is a drive as reported by the sign tool. Type is empty when the tool reports no iu_size.
type SignToolDrive struct {
	Model       string
	CapacityGiB int
	Type        string
	Proxy       bool
}

// drivesFromDevices maps block device path (e.g. /dev/nvme0n1) to its sign tool view. Covers devices
// the tool could not open ("excluded" status) since their hardware info still carries iu_size.
func drivesFromDevices(ctx context.Context, devices []signDriveDevice) map[string]SignToolDrive {
	logger := instrumentation.CurrentSpanLogger(ctx)
	drives := make(map[string]SignToolDrive, len(devices))
	for i := range devices {
		dev := &devices[i]
		if dev.Path == "" {
			continue
		}
		var driveType string
		if dev.Hardware.IuSize == 0 {
			logger.Warn("no iu_size reported for device, drive type unknown", "path", dev.Path)
		} else {
			driveType = iuSizeToDriveType(dev.Hardware.IuSize)
		}
		drives[dev.Path] = SignToolDrive{
			Model:       blockdev.ResolveDriveModel(dev.Hardware.Model, dev.Hardware.ModelNumber, dev.Path),
			CapacityGiB: int(dev.Hardware.SizeBytes / (1024 * 1024 * 1024)),
			Type:        driveType,
			Proxy:       isProxySigned(dev.WekaInfo),
		}
	}
	return drives
}

// GetDrivesWithSignTool maps block device path to the sign tool's view of the drive.
// Mirrors Python get_drives_with_sign_tool().
func GetDrivesWithSignTool(ctx context.Context, useProxySocket bool) (map[string]SignToolDrive, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "GetDrivesWithSignTool")
	defer logger.End()

	devices, err := listDevicesWithSignTool(ctx, useProxySocket)
	if err != nil {
		return nil, err
	}

	drives := drivesFromDevices(ctx, devices)
	logger.Info("drives from sign tool", "count", len(drives))
	return drives, nil
}

// ProxySignedPaths returns the resolved device paths of drives signed for ssdproxy.
func ProxySignedPaths(ctx context.Context) (map[string]struct{}, error) {
	drives, err := GetDrivesWithSignTool(ctx, false)
	if err != nil {
		return nil, err
	}
	paths := make(map[string]struct{})
	for p, d := range drives {
		if d.Proxy {
			paths[RealPath(p)] = struct{}{}
		}
	}
	return paths, nil
}

// RealPath resolves symlinks like Python os.path.realpath: on failure it returns the cleaned path.
func RealPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// DriveRuleMatches reports whether every field set on the rule matches the drive.
// A rule with no field set matches nothing. Mirrors Python drive_rule_matches().
func DriveRuleMatches(rule weka.DriveExclusionRule, drive SignToolDrive) bool {
	model := strings.ToLower(strings.TrimSpace(rule.Model))
	if model == "" && rule.CapacityGiB == 0 && rule.Type == "" {
		return false
	}
	if model != "" && model != strings.ToLower(strings.TrimSpace(drive.Model)) {
		return false
	}
	if rule.CapacityGiB != 0 && rule.CapacityGiB != drive.CapacityGiB {
		return false
	}
	return rule.Type == "" || rule.Type == drive.Type
}

// ExcludedPathsByRules returns the resolved paths of drives matching any rule, logging each exclusion
// and every rule that matched no drive.
func ExcludedPathsByRules(ctx context.Context, rules []weka.DriveExclusionRule, drives map[string]SignToolDrive) map[string]struct{} {
	logger := instrumentation.CurrentSpanLogger(ctx)
	hasTypeRule := false
	for _, r := range rules {
		hasTypeRule = hasTypeRule || r.Type != ""
	}
	excluded := make(map[string]struct{})
	matchedRules := make(map[int]struct{})
	for path, drive := range drives {
		var hits []int
		for i, rule := range rules {
			if DriveRuleMatches(rule, drive) {
				hits = append(hits, i)
			}
		}
		if hasTypeRule && drive.Type == "" && len(hits) == 0 {
			logger.Warn("drive has no detectable type (no iu_size); driveExclusions type rules cannot match it", "path", path)
		}
		if len(hits) == 0 {
			continue
		}
		excluded[RealPath(path)] = struct{}{}
		logger.Info("excluding drive from signing: matches driveExclusions rules",
			"path", path, "model", drive.Model, "capacityGiB", drive.CapacityGiB, "type", drive.Type, "rules", hits)
		for _, i := range hits {
			matchedRules[i] = struct{}{}
		}
	}
	for i, rule := range rules {
		if _, ok := matchedRules[i]; !ok {
			logger.Warn("driveExclusions rule matched no drive", "rule", i, "model", rule.Model, "capacityGiB", rule.CapacityGiB, "type", rule.Type)
		}
	}
	return excluded
}

// runWithStderr runs a command and returns stdout, stderr, and any error.
// Used when callers need to inspect stderr independently of the error value.
func runWithStderr(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	var stderrBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // args are controlled by internal callers
	cmd.Stderr = &stderrBuf
	stdout, err = cmd.Output()
	return stdout, stderrBuf.Bytes(), err
}

// SignBatch signs paths in a single batch invocation of weka-sign-drive.
// Falls back to per-device signing if the batch fails.
// Returns the list of successfully signed paths.
func SignBatch(ctx context.Context, paths []string, opts *SignOptions) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	ctx, logger := instrumentation.CreateLogSpan(ctx, "SignBatch")
	defer logger.End()

	flags := buildSignFlags(opts)
	args := append([]string{"sign"}, flags...)
	args = append(args, "--")
	args = append(args, paths...)

	if _, err := cmdutil.Output(ctx, "/weka-sign-drive", args...); err == nil {
		return paths, nil
	} else {
		logger.Warn("batch sign failed, falling back to per-device", "err", err)
	}

	// Per-device fallback
	var signed []string
	for _, p := range paths {
		perArgs := append([]string{"sign"}, flags...)
		perArgs = append(perArgs, "--", p)
		if _, err := cmdutil.Output(ctx, "/weka-sign-drive", perArgs...); err != nil {
			logger.Error(err, "failed to sign device", "path", p)
			continue
		}
		signed = append(signed, p)
	}
	return signed, nil
}

// SignBatchProxy signs paths using `weka-sign-drive sign proxy` and returns SharedDriveInfo for each.
// Falls back to per-device signing if the batch fails.
func SignBatchProxy(ctx context.Context, paths []string, opts *SignOptions) ([]domain.SharedDriveInfo, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	ctx, logger := instrumentation.CreateLogSpan(ctx, "SignBatchProxy")
	defer logger.End()

	flags := buildSignFlags(opts)
	args := append([]string{"sign", "proxy"}, flags...)
	args = append(args, "--")
	args = append(args, paths...)

	if _, err := cmdutil.Output(ctx, "/weka-sign-drive", args...); err == nil {
		var infos []domain.SharedDriveInfo
		for _, p := range paths {
			info, infoErr := GetProxyDriveInfo(ctx, p)
			if infoErr != nil {
				logger.Warn("failed to get proxy drive info", "path", p, "err", infoErr)
				continue
			}
			infos = append(infos, info)
		}
		return infos, nil
	} else {
		logger.Warn("batch sign failed, falling back to per-device", "err", err)
	}

	// Per-device fallback
	var infos []domain.SharedDriveInfo
	for _, p := range paths {
		perArgs := append([]string{"sign", "proxy"}, flags...)
		perArgs = append(perArgs, "--", p)
		_, perStderr, perErr := runWithStderr(ctx, "/weka-sign-drive", perArgs...)
		if perErr != nil {
			// Python sign_device_path_for_proxy (weka_runtime.py:559-581): if stderr contains
			// "already a Weka partition" the drive is already proxy-signed — not an error.
			// Read existing drive metadata and include the drive in the result set.
			if strings.Contains(string(perStderr), "already a Weka partition") {
				logger.Info("device already proxy-signed, reading existing metadata", "path", p)
				info, infoErr := GetProxyDriveInfo(ctx, p)
				if infoErr != nil {
					logger.Warn("failed to get proxy drive info for already-signed device", "path", p, "err", infoErr)
					continue
				}
				infos = append(infos, info)
				continue
			}
			logger.Error(perErr, "failed to sign device for proxy", "path", p, "stderr", string(perStderr))
			continue
		}
		info, infoErr := GetProxyDriveInfo(ctx, p)
		if infoErr != nil {
			logger.Warn("failed to get proxy drive info after per-device sign", "path", p, "err", infoErr)
			continue
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// signDriveShowOutput matches the JSON produced by `weka-sign-drive show <path> --json`.
type signDriveShowOutput struct {
	Partitions []signDrivePartition `json:"partitions"`
	Hardware   signDriveShowHW      `json:"hardware"`
}

type signDrivePartition struct {
	Header signDrivePartHeader `json:"header"`
	Size   int64               `json:"size"`
}

type signDrivePartHeader struct {
	PhysicalUUID string `json:"physical_uuid"`
	IsProxy      bool   `json:"is_proxy"`
}

type signDriveShowHW struct {
	SerialNumber string `json:"serial_number"`
	Serial       string `json:"serial"`
	Model        string `json:"model"`
	ModelNumber  string `json:"model_number"`
	IuSize       int    `json:"iu_size"`
	SizeBytes    int64  `json:"size_bytes"`
}

// iuSizeToDriveType converts the IU size from weka-sign-drive into a human-readable type string.
// Mirrors Python iu_size_to_drive_type().
func iuSizeToDriveType(iuSize int) string {
	if iuSize >= 16384 {
		return "QLC"
	}
	return "TLC"
}

// GetProxyDriveInfo queries weka-sign-drive show for a single path and returns the SharedDriveInfo.
func GetProxyDriveInfo(ctx context.Context, path string) (domain.SharedDriveInfo, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "GetProxyDriveInfo", "path", path)
	defer logger.End()

	out, err := cmdutil.Output(ctx, "/weka-sign-drive", "show", path, "--json")
	if err != nil {
		return domain.SharedDriveInfo{}, fmt.Errorf("weka-sign-drive show %s: %w", path, err)
	}

	var parsed signDriveShowOutput
	if jsonErr := json.Unmarshal(out, &parsed); jsonErr != nil {
		return domain.SharedDriveInfo{}, fmt.Errorf("weka-sign-drive show %s: JSON parse: %w", path, jsonErr)
	}

	if len(parsed.Partitions) == 0 {
		return domain.SharedDriveInfo{}, fmt.Errorf("weka-sign-drive show %s: no partitions found", path)
	}

	partition := parsed.Partitions[0]
	if !partition.Header.IsProxy {
		return domain.SharedDriveInfo{}, fmt.Errorf("weka-sign-drive show %s: drive is not signed for proxy mode", path)
	}
	physicalUUID := partition.Header.PhysicalUUID
	if physicalUUID == "" {
		return domain.SharedDriveInfo{}, fmt.Errorf("weka-sign-drive show %s: no physical_uuid found", path)
	}

	// Serial: prefer hardware.serial_number, fallback to hardware.serial
	serial := parsed.Hardware.SerialNumber
	if serial == "" {
		serial = parsed.Hardware.Serial
	}
	// Last-resort: use blockdev serial resolution
	if serial == "" {
		serial, _ = blockdev.GetDeviceSerialID(ctx, path) //nolint:errcheck // best-effort serial resolution
	}
	if serial == "" {
		serial = "UNKNOWN"
	}

	// Capacity: prefer hardware.size_bytes, fallback to partition size, then blockdev
	sizeBytes := parsed.Hardware.SizeBytes
	if sizeBytes == 0 {
		sizeBytes = partition.Size
	}
	capacityGiB := int(sizeBytes / (1024 * 1024 * 1024))
	if capacityGiB == 0 {
		if devCap, capErr := blockdev.GetCapacityGiB(ctx, path); capErr == nil {
			capacityGiB = devCap
		} else {
			logger.Warn("failed to get capacity via blockdev", "err", capErr)
		}
	}

	// Model: prefer hardware info, falling back to sysfs resolution.
	model := blockdev.ResolveDriveModel(parsed.Hardware.Model, parsed.Hardware.ModelNumber, path)
	if model == "" {
		logger.Warn("could not determine model", "path", path)
	}

	return domain.SharedDriveInfo{
		PhysicalUUID: physicalUUID,
		Serial:       serial,
		CapacityGiB:  capacityGiB,
		Type:         iuSizeToDriveType(parsed.Hardware.IuSize),
		Model:        model,
	}, nil
}
