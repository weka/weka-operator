package wekadrive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/blockdev"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
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
//   - WekaInfo != nil AND (clusterGUID matches proxySignedGUID, or equals "proxy guid", or IsProxy)
//   - PhysicalUUID is non-empty
//   - SizeBytes > 0
func extractProxyDrives(parsed signDriveListOutput) []domain.SharedDriveInfo {
	var drives []domain.SharedDriveInfo
	for _, dev := range parsed.Devices {
		if dev.Status != "weka_formatted" {
			continue
		}
		if dev.WekaInfo == nil {
			continue
		}
		clusterGUID := strings.ToLower(dev.WekaInfo.ClusterGUID)
		if clusterGUID != proxySignedGUID && clusterGUID != "proxy guid" && !dev.WekaInfo.IsProxy {
			continue
		}
		if dev.PhysicalUUID == "" {
			continue
		}
		if dev.Hardware.SizeBytes <= 0 {
			continue
		}
		drives = append(drives, domain.SharedDriveInfo{
			PhysicalUUID: dev.PhysicalUUID,
			Serial:       dev.Hardware.SerialNumber,
			CapacityGiB:  int(dev.Hardware.SizeBytes / (1024 * 1024 * 1024)),
			Type:         iuSizeToDriveType(dev.Hardware.IuSize),
		})
	}
	return drives
}

// GetDrivesWithClusterGUID runs `weka-sign-drive list -j` and returns a map of serial → path
// for drives that have a cluster_guid (i.e. are claimed by a Weka cluster).
// If useProxySocket is true and the socket file exists, the proxy socket is used.
func GetDrivesWithClusterGUID(ctx context.Context, useProxySocket bool) (map[string]string, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "GetDrivesWithClusterGUID")
	defer logger.End()

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
		logger.Warn("list failed", "err", err)
		return map[string]string{}, nil
	}

	var parsed signDriveListOutput
	if jsonErr := parseSignDriveListJSON(out, &parsed); jsonErr != nil {
		return nil, fmt.Errorf("weka-sign-drive list: JSON parse: %w", jsonErr)
	}

	result := filterClusterGUIDDrives(parsed)
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

	args := []string{}
	if _, err := os.Stat(ssdProxySocketPath); err == nil {
		logger.Info("using proxy socket", "socket", ssdProxySocketPath)
		args = append(args, "--unix-socket", ssdProxySocketPath+":/api/v1")
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
	out = out[jsonStart:]

	var parsed signDriveListOutput
	if jsonErr := parseSignDriveListJSON(out, &parsed); jsonErr != nil {
		return nil, fmt.Errorf("weka-sign-drive list: JSON parse: %w", jsonErr)
	}

	drives := extractProxyDrives(parsed)
	logger.Info("done", "count", len(drives))
	return drives, nil
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

	return domain.SharedDriveInfo{
		PhysicalUUID: physicalUUID,
		Serial:       serial,
		CapacityGiB:  capacityGiB,
		Type:         iuSizeToDriveType(parsed.Hardware.IuSize),
	}, nil
}
