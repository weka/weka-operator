package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/node_agent"
	"github.com/weka/weka-operator/pkg/util"
)

// AddVirtualDrives signs virtual drives on physical proxy devices using weka-sign-drive virtual add
// This is only called for drive containers in drive sharing mode
func (r *containerReconcilerLoop) AddVirtualDrives(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	// Only for drive sharing mode
	if !container.Spec.UseDriveSharing {
		logger.Debug("Container not using drive sharing, skipping virtual drive signing")
		return nil
	}

	// Check if we have virtual drives allocated
	if container.Status.Allocations == nil || len(container.Status.Allocations.VirtualDrives) == 0 {
		logger.Debug("No virtual drives allocated")
		return nil
	}

	// Check if cluster ID is available (needed for --owner-cluster-guid)
	if container.Status.ClusterID == "" {
		err := errors.New("cluster ID is not set, cannot sign virtual drives")
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*10)
	}

	// Find the ssdproxy container on the same node
	// The JSONRPC call must be made to the ssdproxy, not the drive container
	ssdproxyContainer, err := r.findSSDProxyOnNode(ctx)
	if err != nil {
		return fmt.Errorf("failed to find ssdproxy container on node: %w", err)
	}

	// Get the node agent pod for making JSONRPC calls
	agentPod, err := r.GetNodeAgentPod(ctx, container.GetNodeAffinity())
	if err != nil {
		return err
	}

	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		return err
	}

	// Get list of virtual drives already signed on proxy devices via JSONRPC
	signedVirtualDrives, err := r.getSignedVirtualDrives(ctx, string(ssdproxyContainer.GetUID()), agentPod, token)
	if err != nil {
		return fmt.Errorf("failed to get signed virtual drives: %w", err)
	}

	// Check if all virtual drives are already signed on the proxy devices
	allSigned := true
	for _, vd := range container.Status.Allocations.VirtualDrives {
		if !signedVirtualDrives[vd.VirtualUUID] {
			allSigned = false
			break
		}
	}

	if allSigned {
		logger.Info("All virtual drives already signed and present on proxy devices")
		return nil
	}

	var errs []error

	// Sign each virtual drive that hasn't been signed yet
	for _, vd := range container.Status.Allocations.VirtualDrives {
		l := logger.WithValues("virtual_uuid", vd.VirtualUUID, "devicePath", vd.DevicePath)

		// Check if already signed
		if signedVirtualDrives[vd.VirtualUUID] {
			l.Info("Virtual drive already signed on device, skipping")
			continue
		}

		l.Info("Adding virtual drive via ssdproxy JSONRPC through node agent",
			"ssdproxy_name", ssdproxyContainer.Name,
			"ssdproxy_uid", ssdproxyContainer.UID,
			"node", container.GetNodeAffinity())

		// Call ssd_proxy JSONRPC API via node agent
		// The node agent will handle the Unix socket communication
		method := "ssd_proxy_add_virtual_drive"

		params := map[string]any{
			"virtualUuid":  vd.VirtualUUID,
			"physicalUuid": vd.PhysicalUUID,
			"clusterGuid":  container.Status.ClusterID,
			"sizeGB":       vd.CapacityGiB,
		}

		payload := node_agent.JSONRPCProxyPayload{
			ContainerId: string(ssdproxyContainer.GetUID()),
			Method:      method,
			Params:      params,
		}

		l.Info("Calling ssdproxy JSONRPC via node agent",
			"method", method,
			"params", params,
			"ssdproxy_container_id", ssdproxyContainer.UID,
			"node", container.GetNodeAffinity())

		// Marshal payload to JSON
		jsonData, err := json.Marshal(payload)
		if err != nil {
			l.Error(err, "Failed to marshal payload")
			errs = append(errs, fmt.Errorf("failed to marshal payload for virtual drive %s: %w", vd.VirtualUUID, err))
			continue
		}

		// Call node agent's /jsonrpc endpoint
		url := "http://" + agentPod.Status.PodIP + ":8090/jsonrpc"
		resp, err := util.SendJsonRequest(ctx, url, jsonData, util.RequestOptions{AuthHeader: "Token " + token})
		if err != nil {
			l.Error(err, "Failed to call node agent", "node", container.GetNodeAffinity())
			errs = append(errs, fmt.Errorf("failed to add virtual drive %s on node %s: %w", vd.VirtualUUID, container.GetNodeAffinity(), err))
			continue
		}
		defer resp.Body.Close()

		// Read response body for logging
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			l.Error(readErr, "Failed to read response body", "node", container.GetNodeAffinity())
			errs = append(errs, fmt.Errorf("failed to read response for virtual drive %s on node %s: %w", vd.VirtualUUID, container.GetNodeAffinity(), readErr))
			continue
		}

		if resp.StatusCode != http.StatusOK {
			err := fmt.Errorf("node agent returned status: %s, body: %s", resp.Status, string(respBody))
			l.Error(err, "Failed to add virtual drive via JSONRPC", "node", container.GetNodeAffinity())
			errs = append(errs, fmt.Errorf("failed to add virtual drive %s on node %s: %w", vd.VirtualUUID, container.GetNodeAffinity(), err))
			continue
		}

		l.Info("Virtual drive added successfully via ssdproxy JSONRPC", "response", string(respBody))
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors while signing virtual drives: %v", errs)
	}

	logger.InfoWithStatus(codes.Ok, "All virtual drives added successfully")
	return nil
}

// RemoveVirtualDrives removes virtual drives from physical proxy devices using weka-sign-drive virtual remove
// This is called during container deletion for drive containers in drive sharing mode
func (r *containerReconcilerLoop) RemoveVirtualDrives(ctx context.Context) error {
	container := r.container
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "RemoveVirtualDrives")
	defer end()

	// Only for drive sharing mode
	if !container.Spec.UseDriveSharing {
		logger.Debug("Container not using drive sharing, skipping virtual drive removal")
		return nil
	}

	// Check if we have virtual drives allocated
	if container.Status.Allocations == nil || len(container.Status.Allocations.VirtualDrives) == 0 {
		logger.Debug("No virtual drives allocated to remove")
		return nil
	}

	// Get node affinity for this container
	nodeAffinity := container.GetNodeAffinity()
	if nodeAffinity == "" {
		logger.Warn("Node affinity is not set, cannot remove virtual drives")
		return nil
	}

	// Find SSDProxy container on this node
	ssdproxyContainer, err := r.findSSDProxyOnNode(ctx)
	if err != nil {
		logger.Warn("Failed to find SSDProxy container, cannot remove virtual drives via JSONRPC", "error", err)
		// Don't block deletion if we can't find SSDProxy
		return nil
	}

	// Get node agent pod
	agentPod, err := r.GetNodeAgentPod(ctx, nodeAffinity)
	if err != nil {
		logger.Warn("Failed to find node agent pod, cannot remove virtual drives via JSONRPC", "error", err)
		// Don't block deletion if we can't find agent
		return nil
	}

	// Get auth token
	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		logger.Warn("Failed to get node agent token, cannot remove virtual drives via JSONRPC", "error", err)
		// Don't block deletion if we can't get token
		return nil
	}

	var errs []error
	removedCount := 0

	// Remove each virtual drive via JSONRPC
	for _, vd := range container.Status.Allocations.VirtualDrives {
		l := logger.WithValues("virtual_uuid", vd.VirtualUUID, "physical_uuid", vd.PhysicalUUID)

		l.Info("Removing virtual drive via JSONRPC")

		err := r.removeVirtualDriveViaJSONRPC(ctx, string(ssdproxyContainer.GetUID()), agentPod, token, vd.VirtualUUID)
		if err != nil {
			l.Error(err, "Failed to remove virtual drive via JSONRPC")
			errs = append(errs, fmt.Errorf("failed to remove virtual drive %s: %w", vd.VirtualUUID, err))
			continue
		}

		l.Info("Virtual drive removed successfully via JSONRPC")
		removedCount++
	}

	if len(errs) > 0 {
		logger.Warn("Some virtual drives failed to remove", "errorCount", len(errs), "errors", errs)
		return fmt.Errorf("errors while removing virtual drives: %v", errs)
	}

	logger.InfoWithStatus(codes.Ok, "Virtual drive removal completed", "removedCount", removedCount)
	return nil
}

// removeVirtualDriveViaJSONRPC removes a virtual drive by calling ssd_proxy_remove_virtual_drive via node agent
func (r *containerReconcilerLoop) removeVirtualDriveViaJSONRPC(ctx context.Context, ssdproxyContainerUuid string, agentPod *v1.Pod, token string, virtualUUID string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "removeVirtualDriveViaJSONRPC")
	defer end()

	logger.Info("Calling ssd_proxy_remove_virtual_drive via JSONRPC", "virtual_uuid", virtualUUID)

	method := "ssd_proxy_remove_virtual_drive"
	params := map[string]any{
		"virtualUuid": virtualUUID,
	}

	payload := node_agent.JSONRPCProxyPayload{
		ContainerId: ssdproxyContainerUuid,
		Method:      method,
		Params:      params,
	}

	// Marshal payload to JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal JSONRPC payload: %w", err)
	}

	// Call node agent's /jsonrpc endpoint
	url := "http://" + agentPod.Status.PodIP + ":8090/jsonrpc"
	resp, err := util.SendJsonRequest(ctx, url, jsonData, util.RequestOptions{AuthHeader: "Token " + token})
	if err != nil {
		return fmt.Errorf("failed to call node agent /jsonrpc endpoint: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return fmt.Errorf("failed to read JSONRPC response body: %w", readErr)
	}

	// Log the JSONRPC response for debugging
	logger.Info("JSONRPC response received", "status_code", resp.StatusCode, "response", string(respBody))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("node agent returned non-OK status: %s, body: %s", resp.Status, string(respBody))
	}

	// Parse response to check for JSONRPC errors
	var jsonrpcResp struct {
		Result interface{} `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	err = json.Unmarshal(respBody, &jsonrpcResp)
	if err != nil {
		return fmt.Errorf("failed to parse JSONRPC response: %w, body: %s", err, string(respBody))
	}

	if jsonrpcResp.Error != nil {
		return fmt.Errorf("JSONRPC error [%d]: %s", jsonrpcResp.Error.Code, jsonrpcResp.Error.Message)
	}

	logger.Info("Virtual drive removed successfully via JSONRPC", "virtual_uuid", virtualUUID, "result", jsonrpcResp.Result)
	return nil
}

// ssd_proxy_list_virtual_drives JRPC endpoint output
type VirtualDrivesListOutput struct {
	Command   string                    `json:"command"`
	Timestamp string                    `json:"timestamp"`
	Devices   []VirtualDrivesListDevice `json:"devices"`
}

type VirtualDrivesListDevice struct {
	Path     string                     `json:"path"`
	Status   string                     `json:"status"`
	WekaInfo *VirtualDrivesListWekaInfo `json:"weka_info"`
	Usable   bool                       `json:"usable"`
}

type VirtualDrivesListWekaInfo struct {
	FormatStatus   string             `json:"format_status"`
	ClusterGuid    string             `json:"cluster_guid"`
	IsProxy        bool               `json:"is_proxy"`
	VirtualDrives  []VirtualDriveInfo `json:"virtual_drives"`
	ChecksumStatus string             `json:"checksum_status"`
}

// VirtualDriveInfo represents a virtual drive entry on a proxy device
type VirtualDriveInfo struct {
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
	Message string                 `json:"message"`
	Result  []SSDProxyVirtualDrive `json:"result"`
}

// getSignedVirtualDrives returns a map of virtual UUIDs that are signed on proxy devices
// by calling ssd_proxy JSONRPC API via node agent
func (r *containerReconcilerLoop) getSignedVirtualDrives(ctx context.Context, ssdproxyContainerUuid string, agentPod *v1.Pod, token string) (map[string]bool, error) {
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
			ContainerId: ssdproxyContainerUuid,
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
