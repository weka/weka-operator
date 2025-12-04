package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/node_agent"
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

// SSDProxyVirtualDrive represents a virtual drive returned by ssd_proxy JSONRPC
type SSDProxyVirtualDrive struct {
	VirtualUUID string `json:"uuid"`
	ClusterGUID string `json:"clusterGuid"`
	SizeGB      int    `json:"sizeGB"`
}

// NodeAgentJSONRPCResponse represents the wrapper response from node agent
type NodeAgentJSONRPCResponse struct {
	Message string                   `json:"message"`
	Result  []SSDProxyVirtualDrive `json:"result"`
}

// getSignedVirtualDrives returns a map of virtual UUIDs that are signed on proxy devices
// by calling ssd_proxy JSONRPC API via node agent
func (r *containerReconcilerLoop) getSignedVirtualDrives(ctx context.Context, ssdproxyContainer *weka.WekaContainer, agentPod *v1.Pod, token string) (map[string]bool, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getSignedVirtualDrives")
	defer end()

	container := r.container

	// Collect unique physical drive UUIDs from allocations
	physicalUUIDs := make(map[string]bool)
	if container.Status.Allocations != nil {
		for _, vd := range container.Status.Allocations.VirtualDrives {
			physicalUUIDs[vd.PhysicalUUID] = true
		}
	}

	if len(physicalUUIDs) == 0 {
		logger.Debug("No physical drives to query for virtual drives")
		return make(map[string]bool), nil
	}

	// Query each physical drive via JSONRPC
	signedVirtualDrives := make(map[string]bool)
	for physicalUUID := range physicalUUIDs {
		l := logger.WithValues("physical_uuid", physicalUUID, "node", container.GetNodeAffinity())
		l.Info("Querying virtual drives for physical drive via JSONRPC")

		method := "ssd_proxy_list_virtual_drives"
		params := map[string]any{
			"physicalUuid": physicalUUID,
		}

		payload := node_agent.JSONRPCProxyPayload{
			ContainerId: string(ssdproxyContainer.GetUID()),
			Method:      method,
			Params:      params,
		}

		// Marshal payload to JSON
		jsonData, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}

		// Call node agent's /jsonrpc endpoint
		url := "http://" + agentPod.Status.PodIP + ":8090/jsonrpc"
		resp, err := util.SendJsonRequest(ctx, url, jsonData, util.RequestOptions{AuthHeader: "Token " + token})
		if err != nil {
			return nil, fmt.Errorf("failed to call node agent for physical drive %s on node %s: %w", physicalUUID, container.GetNodeAffinity(), err)
		}
		defer resp.Body.Close()

		// Read response body
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("failed to read response body on node %s: %w", container.GetNodeAffinity(), readErr)
		}

		// Log the JSONRPC response for debugging
		l.Info("JSONRPC response received", "status_code", resp.StatusCode, "response", string(respBody))

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("node agent on node %s returned status: %s, body: %s", container.GetNodeAffinity(), resp.Status, string(respBody))
		}

		// Parse the wrapped response from node agent
		var response NodeAgentJSONRPCResponse
		err = json.Unmarshal(respBody, &response)
		if err != nil {
			return nil, fmt.Errorf("failed to parse virtual drives response on node %s: %w, body: %s", container.GetNodeAffinity(), err, string(respBody))
		}

		// Add to signed drives map
		for _, vd := range response.Result {
			signedVirtualDrives[vd.VirtualUUID] = true
			l.Info("Found signed virtual drive",
				"virtual_uuid", vd.VirtualUUID,
				"cluster_uuid", vd.ClusterGUID,
				"size_gb", vd.SizeGB)
		}
	}

	logger.Info("Retrieved signed virtual drives via JSONRPC",
		"count", len(signedVirtualDrives),
		"physical_drives_queried", len(physicalUUIDs))
	return signedVirtualDrives, nil
}
