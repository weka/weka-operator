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
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/results"
	"github.com/weka/weka-operator/internal/runtime/wekadrive"
)

const (
	awsVendorID = "1d0f"
	awsDeviceID = "cd01"
	gcpVendorID = "0x1ae0"
	gcpDeviceID = "0x001f"
)

// RunSignDrives implements the sign-drives adhoc instruction.
// It reads a domain.SignedDrivesExtendedPayload from cfg.Instructions.Payload,
// enumerates target devices according to the payload type, optionally excludes
// already-claimed drives, and either signs for proxy mode or regular mode.
func RunSignDrives(ctx context.Context, cfg *config.Config) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "RunSignDrives")
	defer logger.End()

	// 1. Parse payload
	var payload domain.SignedDrivesExtendedPayload
	if err := json.Unmarshal([]byte(cfg.Instructions.Payload), &payload); err != nil {
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
	guidMap, err := wekadrive.GetDrivesWithClusterGUID(ctx, payload.Shared)
	if err != nil {
		logger.Warn("sign-drives: GetDrivesWithClusterGUID failed, proceeding without exclusions", "err", err)
		guidMap = map[string]string{}
	}

	// 4. Build excluded paths set
	excludedPaths := make(map[string]struct{})
	for _, serial := range payload.ExcludedSerialIds {
		if p, ok := guidMap[serial]; ok {
			excludedPaths[p] = struct{}{}
			logger.Info("sign-drives: excluding drive", "serial", serial, "path", p)
		} else {
			logger.Info("sign-drives: serial has no cluster_guid, not excluding", "serial", serial)
		}
	}

	// 5. Enumerate device paths by payload type
	paths, pathErr := enumerateDevicePaths(ctx, &payload)
	if pathErr != nil {
		return fmt.Errorf("sign-drives: enumerate paths: %w", pathErr)
	}

	// 6. Filter out excluded paths
	var filtered []string
	for _, p := range paths {
		if _, excluded := excludedPaths[p]; excluded {
			logger.Info("sign-drives: skipping excluded path", "path", p)
			continue
		}
		filtered = append(filtered, p)
	}

	logger.Info("sign-drives: signing drives", "type", payload.Type, "shared", payload.Shared, "count", len(filtered))

	// 7. Sign and write results
	if payload.Shared {
		if _, signErr := wekadrive.SignBatchProxy(ctx, filtered, opts); signErr != nil {
			return fmt.Errorf("sign-drives: SignBatchProxy: %w", signErr)
		}
		time.Sleep(3 * time.Second)
		proxyDrives, listErr := wekadrive.ListAllProxyDrives(ctx)
		if listErr != nil {
			return fmt.Errorf("sign-drives: ListAllProxyDrives: %w", listErr)
		}
		return results.Write(domain.DriveNodeResults{ProxyDrives: proxyDrives})
	}

	// Regular signing — signed paths themselves are not needed in result; discover-drives populates it
	if _, signErr := wekadrive.SignBatch(ctx, filtered, opts); signErr != nil {
		return fmt.Errorf("sign-drives: SignBatch: %w", signErr)
	}
	time.Sleep(3 * time.Second)
	return RunDiscoverDrives(ctx, cfg)
}

// enumerateDevicePaths resolves which device paths should be signed based on the payload type.
func enumerateDevicePaths(ctx context.Context, payload *domain.SignedDrivesExtendedPayload) ([]string, error) {
	switch payload.Type {
	case "device-paths":
		return payload.DevicePaths, nil

	case "all-not-root":
		disks, err := blockdev.FindDisks(ctx)
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
		return pciToDevicePaths(ctx, awsVendorID, awsDeviceID)

	case "gcp-all":
		return gcpSysfsDevicePaths(ctx, gcpVendorID, gcpDeviceID)

	case "device-identifiers":
		if payload.PCIDevices == nil {
			return nil, fmt.Errorf("device-identifiers: pciDevices is required")
		}
		pci := payload.PCIDevices
		return pciToDevicePaths(ctx, pci.VendorId, pci.DeviceId)

	default:
		return nil, fmt.Errorf("unknown sign-drives type: %q", payload.Type)
	}
}

// pciToDevicePaths runs lspci and maps matching PCI addresses to /dev/disk/by-path/ paths.
func pciToDevicePaths(ctx context.Context, vendorID, deviceID string) ([]string, error) {
	if vendorID == "" || deviceID == "" {
		return nil, fmt.Errorf("pciToDevicePaths: vendorId and deviceId are required")
	}
	out, err := cmdutil.Output(ctx, "lspci", "-d", vendorID+":"+deviceID)
	if err != nil {
		instrumentation.CurrentSpanLogger(ctx).Warn("pciToDevicePaths: lspci found no devices", "err", err)
		return nil, nil
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
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
