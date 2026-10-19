package adhoc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/blockdev"
	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/process"
	"github.com/weka/weka-operator/internal/runtime/results"
	"github.com/weka/weka-operator/internal/runtime/wekadrive"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

const (
	awsVendorID = "1d0f"
	awsDeviceID = "cd01"
	gcpVendorID = "0x1ae0"
	gcpDeviceID = "0x001f"
)

// RunSignDrives implements the sign-drives adhoc instruction.
// It reads a domain.SignedDrivesExtendedPayload from payloadJSON,
// enumerates target devices according to the payload type, optionally excludes
// already-claimed drives, and either signs for proxy mode or regular mode.
func RunSignDrives(ctx context.Context, runner process.CommandRunner, clk clock.Clock, payloadJSON, resultsPath string) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "RunSignDrives")
	defer logger.End()

	// 1. Parse payload
	var payload domain.SignedDrivesExtendedPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return fmt.Errorf("sign-drives: unmarshal payload: %w", err)
	}

	// 2. Build wekadrive.SignOptions from the v1alpha1 SignOptions embedded in the payload
	opts := &wekadrive.SignOptions{}
	if payload.SignOptions != nil {
		o := payload.SignOptions
		opts.AllowEraseWekaPartitions = o.AllowEraseWekaPartitions
		opts.AllowEraseNonWekaPartitions = o.AllowEraseNonWekaPartitions
		opts.AllowNonEmptyDevice = o.AllowNonEmptyDevice
		opts.SkipTrimFormat = o.SkipTrimFormat
	}

	// 3. Get drives with cluster GUID to know which excluded serials map to real paths
	guidMap, err := wekadrive.GetDrivesWithClusterGUID(ctx, runner, payload.Shared)
	if err != nil {
		logger.Warn("sign-drives: GetDrivesWithClusterGUID failed, proceeding without exclusions", "err", err)
		guidMap = map[string]string{}
	}

	// 4. Paths to skip: drives already signed into a cluster (annotation serials that still carry a
	// cluster GUID), and drives matching a driveExclusions rule.
	alreadySignedPaths := make(map[string]struct{})
	for _, serial := range payload.ExcludedSerialIds {
		if p, ok := guidMap[serial]; ok {
			alreadySignedPaths[wekadrive.RealPath(p)] = struct{}{}
			logger.Info("sign-drives: excluding drive", "serial", serial, "path", p)
		} else {
			logger.Info("sign-drives: serial has no cluster_guid, not excluding", "serial", serial)
		}
	}

	var ruleExcludedPaths map[string]struct{}
	if payload.DriveExclusions != nil && len(payload.DriveExclusions.Rules) > 0 {
		drives, dErr := wekadrive.GetDrivesWithSignTool(ctx, runner, payload.Shared)
		if dErr != nil {
			return fmt.Errorf("sign-drives: GetDrivesWithSignTool: %w", dErr)
		}
		ruleExcludedPaths = wekadrive.ExcludedPathsByRules(ctx, payload.DriveExclusions.Rules, drives)
	}

	// 5. Enumerate device paths by payload type
	paths, pathErr := enumerateDevicePaths(ctx, runner, &payload)
	if pathErr != nil {
		return fmt.Errorf("sign-drives: enumerate paths: %w", pathErr)
	}

	// 6. Filter out skipped paths, counting only this run's candidates; already-signed wins over a rule match.
	var filtered []string
	alreadySigned, excluded := 0, 0
	for _, p := range paths {
		rp := wekadrive.RealPath(p)
		if _, ok := alreadySignedPaths[rp]; ok {
			alreadySigned++
			logger.Info("sign-drives: skipping already-signed path", "path", p)
		} else if _, ok := ruleExcludedPaths[rp]; ok {
			excluded++
			logger.Info("sign-drives: skipping path matching driveExclusions", "path", p)
		} else {
			filtered = append(filtered, p)
		}
	}

	logger.Info("sign-drives: signing drives", "type", payload.Type, "shared", payload.Shared, "count", len(filtered))

	// 7. Sign
	var signed int
	if payload.Shared {
		signedPaths, fallbackAlreadySigned, signErr := wekadrive.SignBatchProxy(ctx, runner, filtered, opts)
		if signErr != nil {
			return fmt.Errorf("sign-drives: SignBatchProxy: %w", signErr)
		}
		signed = len(signedPaths)
		alreadySigned += fallbackAlreadySigned
	} else {
		signedPaths, signErr := wekadrive.SignBatch(ctx, runner, filtered, opts)
		if signErr != nil {
			return fmt.Errorf("sign-drives: SignBatch: %w", signErr)
		}
		signed = len(signedPaths)
	}
	// Mirror Python asyncio.sleep(3) hack — the kernel does not update device paths instantly. DO NOT remove.
	if sleepErr := clock.Sleep(ctx, clk, 3*time.Second); sleepErr != nil {
		return sleepErr
	}

	// 8. Discover and write results
	var res domain.DriveNodeResults
	if payload.Shared {
		proxyDrives, listErr := wekadrive.ListAllProxyDrives(ctx, runner)
		if listErr != nil {
			return fmt.Errorf("sign-drives: ListAllProxyDrives: %w", listErr)
		}
		// Mirror Python discover_ssdproxy_drives(): raw_drives and kernel_view_complete alongside proxy_drives.
		rawDrives, rawOK := collectRawDrives(ctx, runner)
		res = domain.DriveNodeResults{
			ProxyDrives:        proxyDrives,
			RawDrives:          rawDrives,
			KernelViewComplete: rawOK && blockdev.IsKernelViewComplete(ctx),
		}
	} else {
		res = discoverDriveResults(ctx, runner)
	}
	res.Signed, res.AlreadySigned, res.Excluded = signed, alreadySigned, excluded
	logger.Info("sign-drives: done", "signed", signed, "alreadySigned", alreadySigned, "excluded", excluded)
	return results.Write(ctx, resultsPath, res)
}

// enumerateDevicePaths resolves which device paths should be signed based on the payload type.
func enumerateDevicePaths(ctx context.Context, runner process.CommandRunner, payload *domain.SignedDrivesExtendedPayload) ([]string, error) {
	switch payload.Type {
	case "device-paths":
		return payload.DevicePaths, nil

	case weka.SignDrivesTypeDeviceSerials:
		return resolveDevicePathsBySerials(ctx, runner, payload.DeviceSerials)

	case "all-not-root":
		disks, err := blockdev.FindDisks(ctx, runner)
		if err != nil {
			return nil, fmt.Errorf("all-not-root: FindDisks: %w", err)
		}
		var paths []string
		for _, d := range disks {
			if !d.IsMounted {
				paths = append(paths, d.Path)
			}
		}
		return paths, nil

	case "aws-all":
		return pciToDevicePaths(ctx, runner, awsVendorID, awsDeviceID)

	case "gcp-all":
		return gcpSysfsDevicePaths(ctx, gcpVendorID, gcpDeviceID)

	case "device-identifiers":
		if payload.PCIDevices == nil {
			return nil, fmt.Errorf("device-identifiers: pciDevices is required")
		}
		pci := payload.PCIDevices
		return pciToDevicePaths(ctx, runner, pci.VendorId, pci.DeviceId)

	default:
		return nil, fmt.Errorf("unknown sign-drives type: %q", payload.Type)
	}
}

// resolveDevicePathsBySerials fails if any serial does not resolve to a block device.
func resolveDevicePathsBySerials(ctx context.Context, runner process.CommandRunner, serials []string) ([]string, error) {
	paths := make([]string, 0, len(serials))
	for _, serial := range serials {
		p, err := blockdev.GetDevicePathBySerial(ctx, runner, serial)
		if err != nil {
			return nil, fmt.Errorf("could not resolve device path for serial %s: %w", serial, err)
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// pciToDevicePaths runs lspci and maps matching PCI addresses to /dev/disk/by-path/ paths.
func pciToDevicePaths(ctx context.Context, runner process.CommandRunner, vendorID, deviceID string) ([]string, error) {
	if vendorID == "" || deviceID == "" {
		return nil, fmt.Errorf("pciToDevicePaths: vendorId and deviceId are required")
	}
	// -D shows full PCI domain numbers (e.g. "0000:00:1f.0"); mirrors Python commit 587db35e.
	// The first whitespace-delimited field is still the BDF address, so downstream parsing is unaffected.
	res, err := runner.Run(ctx, process.Command{Path: "lspci", Args: []string{"-D", "-d", vendorID + ":" + deviceID}})
	if err != nil {
		instrumentation.CurrentSpanLogger(ctx).Warn("pciToDevicePaths: lspci found no devices", "err", err)
		return nil, nil
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// First field is the PCI address (e.g., "00:1f.0")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pciAddr := fields[0]
		paths = append(paths, fmt.Sprintf("/dev/disk/by-path/pci-%s-nvme-1", pciAddr))
	}
	return paths, nil
}

// gcpSysfsDevicePaths walks /sys/block/ and returns /dev/<name> for entries matching GCP vendor/device IDs.
func gcpSysfsDevicePaths(_ context.Context, vendorID, deviceID string) ([]string, error) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, fmt.Errorf("gcpSysfsDevicePaths: reading /sys/block: %w", err)
	}
	var paths []string
	for _, e := range entries {
		name := e.Name()
		vendorPath := "/sys/block/" + name + "/device/device/vendor"
		devicePath := "/sys/block/" + name + "/device/device/device"

		vendorData, vErr := os.ReadFile(vendorPath)
		if vErr != nil {
			continue
		}
		deviceData, dErr := os.ReadFile(devicePath)
		if dErr != nil {
			continue
		}

		if strings.TrimSpace(string(vendorData)) == vendorID &&
			strings.TrimSpace(string(deviceData)) == deviceID {
			paths = append(paths, "/dev/"+name)
		}
	}
	return paths, nil
}
