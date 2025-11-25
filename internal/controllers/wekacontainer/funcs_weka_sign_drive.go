package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/weka/go-weka-observability/instrumentation"

	"github.com/weka/weka-operator/pkg/util"
)

// WekaSignDriveListOutput represents the JSON output of weka-sign-drive list --json
type WekaSignDriveListOutput struct {
	Command   string                `json:"command"`
	Timestamp string                `json:"timestamp"`
	Devices   []WekaSignDriveDevice `json:"devices"`
}

// WekaSignDriveDevice represents a device in the weka-sign-drive list output
type WekaSignDriveDevice struct {
	Path     string                 `json:"path"`
	Status   string                 `json:"status"`
	WekaInfo *WekaSignDriveWekaInfo `json:"weka_info"`
	Usable   bool                   `json:"usable"`
}

// WekaSignDriveWekaInfo contains the Weka-specific information about a device
type WekaSignDriveWekaInfo struct {
	FormatStatus   string                      `json:"format_status"`
	ClusterGuid    string                      `json:"cluster_guid"`
	IsProxy        bool                        `json:"is_proxy"`
	VirtualDrives  []WekaSignDriveVirtualDrive `json:"virtual_drives"`
	ChecksumStatus string                      `json:"checksum_status"`
}

// WekaSignDriveVirtualDrive represents a virtual drive entry on a proxy device
type WekaSignDriveVirtualDrive struct {
	VirtualUUID string `json:"virtual_uuid"`
	ClusterUUID string `json:"cluster_uuid"`
	SizeGB      int    `json:"size_gb"`
}

// getSignedVirtualDrives returns a map of virtual UUIDs that are signed on proxy devices
// by parsing the output of weka-sign-drive list --json
func (r *containerReconcilerLoop) getSignedVirtualDrives(ctx context.Context, executor util.Exec) (map[string]bool, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getSignedVirtualDrives")
	defer end()

	// Execute weka-sign-drive list --json to get all devices and their virtual drives
	cmd := "weka-sign-drive list --json"
	stdout, stderr, err := executor.ExecNamed(ctx, "ListSignedVirtualDrives", []string{"bash", "-ce", cmd})
	if err != nil {
		return nil, fmt.Errorf("failed to list signed drives: %w, stderr: %s", err, stderr.String())
	}

	// The weka-sign-drive command may output warning/error messages before the JSON
	// (e.g., "Partition table check: could not open device...")
	// Find the first '{' character to locate the start of the JSON output
	output := stdout.String()
	jsonStart := 0
	for i, ch := range output {
		if ch == '{' {
			jsonStart = i
			break
		}
	}
	if jsonStart == 0 && len(output) > 0 && output[0] != '{' {
		return nil, fmt.Errorf("no JSON found in weka-sign-drive output")
	}

	// Parse the JSON output starting from the first '{'
	var listOutput WekaSignDriveListOutput
	err = json.Unmarshal([]byte(output[jsonStart:]), &listOutput)
	if err != nil {
		return nil, fmt.Errorf("failed to parse weka-sign-drive list output: %w", err)
	}

	// Build map of signed virtual drive UUIDs
	signedVirtualDrives := make(map[string]bool)
	for _, device := range listOutput.Devices {
		// Skip devices without weka_info or without virtual drives
		if device.WekaInfo == nil || len(device.WekaInfo.VirtualDrives) == 0 {
			continue
		}

		for _, vd := range device.WekaInfo.VirtualDrives {
			signedVirtualDrives[vd.VirtualUUID] = true
			logger.Info("Found signed virtual drive",
				"virtual_uuid", vd.VirtualUUID,
				"cluster_uuid", vd.ClusterUUID,
				"device", device.Path)
		}
	}

	logger.Info("Retrieved signed virtual drives from devices", "count", len(signedVirtualDrives))
	return signedVirtualDrives, nil
}
