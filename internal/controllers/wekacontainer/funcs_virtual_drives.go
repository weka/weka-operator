package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/node_agent"
	"github.com/weka/weka-operator/pkg/util"
)

// AddVirtualDrives adds virtual drives on physical proxy devices using jrpc
// This is only called for drive containers in drive sharing mode
func (r *containerReconcilerLoop) AddVirtualDrives(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	// Only for drive sharing mode
	if !container.Spec.UseDriveSharing {
		logger.Debug("Container not using drive sharing, skipping virtual drive adding")
		return nil
	}

	// Check if we have virtual drives allocated
	if container.Status.Allocations == nil || len(container.Status.Allocations.VirtualDrives) == 0 {
		logger.Debug("No virtual drives allocated")
		return nil
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

	// Get list of virtual drives already added on proxy devices via JSONRPC
	addedVirtualDrives, err := r.getAddedVirtualDrives(ctx, string(ssdproxyContainer.GetUID()), agentPod, token)
	if err != nil {
		return fmt.Errorf("failed to get list of added virtual drives: %w", err)
	}

	// Check if all allocated virtual drives are already added
	allAdded := true
	for _, vd := range container.Status.Allocations.VirtualDrives {
		if !addedVirtualDrives[vd.VirtualUUID] {
			allAdded = false
			break
		}
	}

	if allAdded {
		logger.Info("All virtual drives already added and present on proxy devices")
		return nil
	}

	var errs []error

	// Add each virtual drive that hasn't been added yet
	for _, vd := range container.Status.Allocations.VirtualDrives {
		l := logger.WithValues("virtual_uuid", vd.VirtualUUID, "physical_uuid", vd.PhysicalUUID)

		// Check if already added
		if addedVirtualDrives[vd.VirtualUUID] {
			l.Info("Virtual drive already added to ssdproxy, skipping")
			continue
		}

		l.Info("Adding virtual drive via ssdproxy JSONRPC through node agent",
			"ssdproxy_name", ssdproxyContainer.Name,
			"ssdproxy_uid", ssdproxyContainer.UID,
			"node", container.GetNodeAffinity())

		// Add the virtual drive via JSONRPC
		err := r.addVirtualDriveViaJSONRPC(ctx, string(ssdproxyContainer.GetUID()), agentPod, token, vd)
		if err != nil {
			l.Error(err, "Failed to add virtual drive via JSONRPC")
			errs = append(errs, fmt.Errorf("failed to add virtual drive %s: %w", vd.VirtualUUID, err))
			continue
		}

		l.Info("Virtual drive added successfully via ssdproxy JSONRPC")
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors while adding virtual drives: %v", errs)
	}

	logger.InfoWithStatus(codes.Ok, "All virtual drives added successfully")

	return nil
}

// addVirtualDriveViaJSONRPC adds a virtual drive by calling ssd_proxy_add_virtual_drive via node agent
func (r *containerReconcilerLoop) addVirtualDriveViaJSONRPC(ctx context.Context, ssdproxyContainerUuid string, agentPod *v1.Pod, token string, vd weka.VirtualDrive) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "addVirtualDriveViaJSONRPC")
	defer end()

	method := "ssd_proxy_add_virtual_drive"

	params := map[string]any{
		"virtualUuid":  vd.VirtualUUID,
		"physicalUuid": vd.PhysicalUUID,
		"sizeGB":       vd.CapacityGiB,
	}

	payload := node_agent.JSONRPCProxyPayload{
		ContainerId: ssdproxyContainerUuid,
		Method:      method,
		Params:      params,
	}

	logger.Info("Calling ssdproxy JSONRPC via node agent",
		"method", method,
		"params", params,
		"ssdproxy_container_id", ssdproxyContainerUuid,
	)

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

	logger.Info("Virtual drive added successfully via JSONRPC", "virtual_uuid", vd.VirtualUUID)
	return nil
}

// RemoveVirtualDrives removes virtual drives from physical proxy devices using jrpc
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

func (r *containerReconcilerLoop) removeVirtualDrive(ctx context.Context, virtualDriveUuid string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "removeVirtualDrive")
	defer end()

	logger.Info("Removing virtual drive", "virtual_uuid", virtualDriveUuid)

	// Get node affinity for this container
	nodeAffinity := r.container.GetNodeAffinity()
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

	err = r.removeVirtualDriveViaJSONRPC(ctx, string(ssdproxyContainer.GetUID()), agentPod, token, virtualDriveUuid)
	if err != nil {
		return fmt.Errorf("failed to remove virtual drive %s: %w", virtualDriveUuid, err)
	}

	logger.Info("Virtual drive removed successfully", "virtual_uuid", virtualDriveUuid)

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
	VirtualUUID  string `json:"uuid"`
	PhysicalUUID string `json:"-"` // Not in JSON response, populated from request context
	ClusterGUID  string `json:"clusterGuid"`
	SizeGB       int    `json:"sizeGB"`
}

// NodeAgentJSONRPCResponse represents the wrapper response from node agent
type NodeAgentJSONRPCResponse struct {
	Message string                 `json:"message"`
	Result  []SSDProxyVirtualDrive `json:"result"`
}

// SSDProxyPhysicalDrive represents a physical drive returned by ssd_proxy_list_physical_drives JSONRPC
type SSDProxyPhysicalDrive struct {
	NumVirtualDrives int    `json:"numVirtualDrives"`
	PhysicalUUID     string `json:"physicalUuid"`
	SizeGB           int    `json:"sizeGB"`
	Model            string `json:"model"`
	PCIAddress       string `json:"pciAddress"`
}

// SSDProxyPhysicalDrivesResponse represents the response from ssd_proxy_list_physical_drives
type SSDProxyPhysicalDrivesResponse struct {
	Result  []SSDProxyPhysicalDrive `json:"result"`
	ID      int                     `json:"id"`
	JSONRPC string                  `json:"jsonrpc"`
}

func (r *containerReconcilerLoop) ssdProxyListPhysicalDrives(ctx context.Context, ssdproxyContainerUuid string, agentPod *v1.Pod, token string) ([]SSDProxyPhysicalDrive, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ssdProxyListPhysicalDrives", "node", r.container.GetNodeAffinity())
	defer end()

	method := "ssd_proxy_list_physical_drives"
	params := map[string]any{}

	payload := node_agent.JSONRPCProxyPayload{
		ContainerId: ssdproxyContainerUuid,
		Method:      method,
		Params:      params,
	}

	logger.Info("Calling ssdproxy JSONRPC via node agent",
		"method", method,
		"ssdproxy_container_id", ssdproxyContainerUuid,
	)

	// Marshal payload to JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSONRPC payload: %w", err)
	}

	// Call node agent's /jsonrpc endpoint
	url := "http://" + agentPod.Status.PodIP + ":8090/jsonrpc"
	resp, err := util.SendJsonRequest(ctx, url, jsonData, util.RequestOptions{AuthHeader: "Token " + token})
	if err != nil {
		return nil, fmt.Errorf("failed to call node agent /jsonrpc endpoint: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("failed to read JSONRPC response body: %w", readErr)
	}

	// Log the JSONRPC response for debugging
	logger.Info("JSONRPC response received", "status_code", resp.StatusCode, "response", string(respBody))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("node agent returned non-OK status: %s, body: %s", resp.Status, string(respBody))
	}

	// Parse response
	var jsonrpcResp SSDProxyPhysicalDrivesResponse
	err = json.Unmarshal(respBody, &jsonrpcResp)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JSONRPC response: %w, body: %s", err, string(respBody))
	}

	logger.Info("Physical drives retrieved successfully via JSONRPC",
		"count", len(jsonrpcResp.Result))

	return jsonrpcResp.Result, nil
}

// ssdProxyListVirtualDrives lists all virtual drives across all physical drives
// by first querying physical drives and then querying virtual drives for each physical drive that has them
func (r *containerReconcilerLoop) ssdProxyListVirtualDrives(ctx context.Context, ssdproxyContainerUuid string, agentPod *v1.Pod, token string) ([]SSDProxyVirtualDrive, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ssdProxyListVirtualDrives", "node", r.container.GetNodeAffinity())
	defer end()

	// First, get all physical drives
	physicalDrives, err := r.ssdProxyListPhysicalDrives(ctx, ssdproxyContainerUuid, agentPod, token)
	if err != nil {
		return nil, fmt.Errorf("failed to list physical drives: %w", err)
	}

	logger.Info("Retrieved physical drives, querying for virtual drives",
		"physical_drives_count", len(physicalDrives))

	// Collect all virtual drives from physical drives that have them
	var allVirtualDrives []SSDProxyVirtualDrive

	for _, physicalDrive := range physicalDrives {
		// Skip physical drives with no virtual drives
		if physicalDrive.NumVirtualDrives == 0 {
			logger.Debug("Physical drive has no virtual drives, skipping",
				"physical_uuid", physicalDrive.PhysicalUUID,
				"model", physicalDrive.Model)
			continue
		}

		l := logger.WithValues(
			"physical_uuid", physicalDrive.PhysicalUUID,
			"num_virtual_drives", physicalDrive.NumVirtualDrives)
		l.Info("Querying virtual drives for physical drive")

		// Query virtual drives for this physical drive
		virtualDrives, err := r.ssdProxyListVirtualDrivesByPhysicalUuid(ctx, ssdproxyContainerUuid, physicalDrive.PhysicalUUID, agentPod, token)
		if err != nil {
			return nil, fmt.Errorf("failed to list virtual drives for physical drive %s: %w", physicalDrive.PhysicalUUID, err)
		}

		l.Info("Retrieved virtual drives for physical drive", "count", len(virtualDrives))
		allVirtualDrives = append(allVirtualDrives, virtualDrives...)
	}

	logger.Info("Retrieved all virtual drives across all physical drives",
		"total_virtual_drives", len(allVirtualDrives),
		"physical_drives_queried", len(physicalDrives))

	return allVirtualDrives, nil
}

func (r *containerReconcilerLoop) ssdProxyListVirtualDrivesByPhysicalUuid(ctx context.Context, ssdproxyContainerUuid, physicalDriveUuid string, agentPod *v1.Pod, token string) ([]SSDProxyVirtualDrive, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ssdProxyListVirtualDrivesByPhysicalUuid", "node", r.container.GetNodeAffinity())
	defer end()

	method := "ssd_proxy_list_virtual_drives"
	params := map[string]any{
		"physicalUuid": physicalDriveUuid,
	}

	payload := node_agent.JSONRPCProxyPayload{
		ContainerId: ssdproxyContainerUuid,
		Method:      method,
		Params:      params,
	}

	logger.Info("Calling ssdproxy JSONRPC via node agent",
		"method", method,
		"physical_uuid", physicalDriveUuid,
		"ssdproxy_container_id", ssdproxyContainerUuid,
	)

	// Marshal payload to JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSONRPC payload: %w", err)
	}

	// Call node agent's /jsonrpc endpoint
	url := "http://" + agentPod.Status.PodIP + ":8090/jsonrpc"
	resp, err := util.SendJsonRequest(ctx, url, jsonData, util.RequestOptions{AuthHeader: "Token " + token})
	if err != nil {
		return nil, fmt.Errorf("failed to call node agent /jsonrpc endpoint for physical drive %s: %w", physicalDriveUuid, err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("failed to read JSONRPC response body: %w", readErr)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("node agent returned non-OK status: %s, body: %s", resp.Status, string(respBody))
	}

	// Parse the wrapped response from node agent
	var response NodeAgentJSONRPCResponse
	err = json.Unmarshal(respBody, &response)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JSONRPC response: %w, body: %s", err, string(respBody))
	}

	// Populate PhysicalUUID for each virtual drive (not in the JSONRPC response)
	for i := range response.Result {
		response.Result[i].PhysicalUUID = physicalDriveUuid
	}

	logger.Info("Virtual drives retrieved successfully via JSONRPC",
		"count", len(response.Result),
		"physical_uuid", physicalDriveUuid)

	return response.Result, nil
}

// getAddedVirtualDrives returns a map of virtual UUIDs that are added to proxy devices
// by calling ssd_proxy JSONRPC API via node agent
func (r *containerReconcilerLoop) getAddedVirtualDrives(ctx context.Context, ssdproxyContainerUuid string, agentPod *v1.Pod, token string) (map[string]bool, error) {
	container := r.container

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getAddedVirtualDrives", "node", container.GetNodeAffinity())
	defer end()

	// Collect unique physical drive UUIDs from allocations
	physicalUUIDs := container.Status.Allocations.GetVirtualDrivesPhysicalUuids()

	if len(physicalUUIDs) == 0 {
		logger.Debug("No physical drives to query for virtual drives")
		return make(map[string]bool), nil
	}

	// Query each physical drive via JSONRPC
	addedVirtualDrives := make(map[string]bool)
	for _, physicalUUID := range physicalUUIDs {
		l := logger.WithValues("physical_uuid", physicalUUID)
		l.Info("Querying virtual drives for physical drive via JSONRPC")

		virtualDrives, err := r.ssdProxyListVirtualDrivesByPhysicalUuid(ctx, ssdproxyContainerUuid, physicalUUID, agentPod, token)
		if err != nil {
			return nil, fmt.Errorf("failed to list virtual drives for physical drive %s on node %s: %w", physicalUUID, container.GetNodeAffinity(), err)
		}

		// Add to the map of added virtual drives
		for _, vd := range virtualDrives {
			addedVirtualDrives[vd.VirtualUUID] = true
			l.Info("Found added virtual drive",
				"virtual_uuid", vd.VirtualUUID,
				"physical_uuid", vd.PhysicalUUID,
				"cluster_uuid", vd.ClusterGUID,
				"size_gb", vd.SizeGB)
		}
	}

	logger.Info("Retrieved added virtual drives via JSONRPC",
		"count", len(addedVirtualDrives),
		"physical_drives_queried", len(physicalUUIDs),
	)

	return addedVirtualDrives, nil
}
