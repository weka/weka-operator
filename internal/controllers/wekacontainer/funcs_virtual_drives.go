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
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/node_agent"
	"github.com/weka/weka-operator/internal/services/ssdproxy"
	"github.com/weka/weka-operator/pkg/util"
)

// AddVirtualDrives adds virtual drives on physical proxy devices using jrpc
// This is only called for drive containers in drive sharing mode
func (r *containerReconcilerLoop) AddVirtualDrives(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	container := r.container

	// Only for drive sharing mode
	if !container.UsesDriveSharing() {
		logger.Debug("Container not using drive sharing, skipping virtual drive adding")
		return nil
	}

	// Check if we have virtual drives allocated
	if container.Status.Allocations == nil || len(container.Status.Allocations.VirtualDrives) == 0 {
		logger.Debug("No virtual drives allocated")
		return nil
	}

	// Check if cluster ID is available (if cluster id for container is not set, it mean there is not cluster guid yet)
	if container.Status.ClusterID == "" {
		err := errors.New("cluster ID is not set, cannot sign virtual drives")
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*10)
	}

	cluster, err := r.getOwnerCluster(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get owner cluster")
	}

	clusterUUID := cluster.Status.ClusterID
	if clusterUUID == "" {
		missingErr := errors.New("owner cluster UUID is not set, cannot add virtual drives")
		return lifecycle.NewWaitErrorWithDuration(missingErr, time.Second*10)
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

	// A blocked VID is on its way out: re-signing it would undo the removal in progress.
	blockedVids := r.blockedVirtualUuidSet(ctx)

	// Check if all allocated virtual drives are already added
	allAdded := true
	for _, vd := range container.Status.Allocations.VirtualDrives {
		if !addedVirtualDrives[vd.VirtualUUID] && !blockedVids[vd.VirtualUUID] {
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
		vdCtx, l := logger.WithValues("virtual_uuid", vd.VirtualUUID, "physical_uuid", vd.PhysicalUUID)

		if blockedVids[vd.VirtualUUID] {
			l.Info("Virtual drive is blocked on the node, skipping until its removal completes")
			continue
		}

		// Check if already added
		if addedVirtualDrives[vd.VirtualUUID] {
			l.Info("Virtual drive already added to ssdproxy, skipping")
			continue
		}

		l.Info("Adding virtual drive via ssdproxy JSONRPC through node agent",
			"ssdproxy_name", ssdproxyContainer.Name,
			"ssdproxy_uid", ssdproxyContainer.UID,
			"cluster_uuid", clusterUUID,
			"node", container.GetNodeAffinity(),
		)

		// Add the virtual drive via JSONRPC
		err := r.addVirtualDriveViaJSONRPC(vdCtx, string(ssdproxyContainer.GetUID()), agentPod, token, clusterUUID, vd)
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
func (r *containerReconcilerLoop) addVirtualDriveViaJSONRPC(ctx context.Context, ssdproxyContainerUuid string, agentPod *v1.Pod, token, clusterUUID string, vd weka.VirtualDrive) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "addVirtualDriveViaJSONRPC")
	defer logger.End()

	method := "ssd_proxy_add_virtual_drive"

	params := map[string]any{
		"virtualUuid":  vd.VirtualUUID,
		"physicalUuid": vd.PhysicalUUID,
		"clusterGuid":  clusterUUID,
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
	defer resp.Body.Close() //nolint:errcheck // error return value intentionally not checked

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
// Uses a query-first approach: checks if VD exists, attempts removal, and tolerates transient failures
func (r *containerReconcilerLoop) RemoveVirtualDrives(ctx context.Context) error {
	container := r.container
	ctx, logger := instrumentation.CreateLogSpan(ctx, "RemoveVirtualDrives")
	defer logger.End()

	if container.Spec.GetOverrides().SkipVirtualDrivesRemoval {
		logger.Info("Skipping virtual drive removal as requested via SkipVirtualDrivesRemoval override")
		return nil
	}

	// Only for drive sharing mode
	if !container.UsesDriveSharing() {
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
		return fmt.Errorf("failed to find SSDProxy container for virtual drive removal: %w", err)
	}

	// Get node agent pod
	agentPod, err := r.GetNodeAgentPod(ctx, nodeAffinity)
	if err != nil {
		return fmt.Errorf("failed to get node agent pod for virtual drive removal: %w", err)
	}

	// Get auth token
	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		return fmt.Errorf("failed to get node agent token for virtual drive removal: %w", err)
	}

	// Query existing virtual drives first
	existingVDs, err := r.getAddedVirtualDrives(ctx, string(ssdproxyContainer.GetUID()), agentPod, token)
	if err != nil {
		return fmt.Errorf("failed to query existing virtual drives: %w", err)
	}

	var errs []error
	var attemptedVDUUIDs []string
	var removedVDUUIDs []string
	alreadyDeletedCount := 0

	// Remove each virtual drive via JSONRPC
	for _, vd := range container.Status.Allocations.VirtualDrives {
		vdCtx, l := logger.WithValues("virtual_uuid", vd.VirtualUUID, "physical_uuid", vd.PhysicalUUID)

		// Check if VD already doesn't exist (query-first approach)
		if !existingVDs[vd.VirtualUUID] {
			l.Info("Virtual drive does not exist on proxy, treating as already deleted")
			alreadyDeletedCount++
			continue
		}

		l.Info("Attempting to remove virtual drive via JSONRPC")

		err := r.removeVirtualDriveViaJSONRPC(vdCtx, string(ssdproxyContainer.GetUID()), agentPod, token, vd.VirtualUUID)
		if err != nil {
			l.Error(err, "Failed to remove virtual drive via JSONRPC")
			errs = append(errs, fmt.Errorf("failed to remove virtual drive %s: %w", vd.VirtualUUID, err))
			continue
		}

		attemptedVDUUIDs = append(attemptedVDUUIDs, vd.VirtualUUID)
	}

	// Single post-removal verification query for all attempted removals
	if len(attemptedVDUUIDs) > 0 {
		verifyVDs, verifyErr := r.getAddedVirtualDrives(ctx, string(ssdproxyContainer.GetUID()), agentPod, token)
		if verifyErr != nil {
			logger.Error(verifyErr, "Could not verify VD removals via query")
			errs = append(errs, fmt.Errorf("could not verify VD removals via query: %w", verifyErr))
		} else {
			// Check which removals succeeded via query
			for _, vdUUID := range attemptedVDUUIDs {
				if verifyVDs[vdUUID] {
					_, l := logger.WithValues("virtual_uuid", vdUUID)
					l.Info("Virtual drive still exists after deletion call, may be delayed. Retrying on next cycle")
					errs = append(errs, fmt.Errorf("virtual drive %s still exists after deletion", vdUUID))
				} else {
					removedVDUUIDs = append(removedVDUUIDs, vdUUID)
				}
			}
		}

		removedVDUUIDsJSON, errMarshal := json.Marshal(removedVDUUIDs)
		if errMarshal != nil {
			logger.Error(errMarshal, "Failed to marshal removed virtual drive UUIDs for logging")
		} else {
			logger.Info("Virtual drives removed successfully", "removedVDUUIDs", string(removedVDUUIDsJSON))
		}
	}

	if len(errs) > 0 {
		logger.Error(nil, "Some virtual drives failed to remove", "errorCount", len(errs), "errors", errs)
		return fmt.Errorf("errors while removing virtual drives: %v", errs)
	}

	logger.InfoWithStatus(codes.Ok, "Virtual drive removal completed",
		"removedCount", len(removedVDUUIDs),
		"alreadyDeletedCount", alreadyDeletedCount)
	return nil
}

func (r *containerReconcilerLoop) removeVirtualDrive(ctx context.Context, virtualDriveUuid string) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "removeVirtualDrive")
	defer logger.End()

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
		return fmt.Errorf("failed to find SSDProxy container for virtual drive cleanup: %w", err)
	}

	// Get node agent pod
	agentPod, err := r.GetNodeAgentPod(ctx, nodeAffinity)
	if err != nil {
		return fmt.Errorf("failed to get node agent pod for virtual drive cleanup: %w", err)
	}

	// Get auth token
	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		return fmt.Errorf("failed to get node agent token for virtual drive cleanup: %w", err)
	}

	return r.removeVirtualDriveViaProxy(ctx, virtualDriveUuid, string(ssdproxyContainer.GetUID()), agentPod, token)
}

// removeVirtualDriveViaProxy erases one virtual drive from its physical drive via the node's
// ssdproxy, given handles already resolved by the caller (ssdproxy UID, node-agent pod, auth
// token). Split out of removeVirtualDrive so a caller removing many VIDs in one pass can resolve
// the proxy, pod and token once instead of once per VID.
func (r *containerReconcilerLoop) removeVirtualDriveViaProxy(
	ctx context.Context, virtualDriveUuid, ssdproxyUID string, agentPod *v1.Pod, token string,
) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "removeVirtualDriveViaProxy", "virtual_uuid", virtualDriveUuid)
	defer logger.End()

	// Query first - check if VD exists
	existingVDs, err := r.getAddedVirtualDrives(ctx, ssdproxyUID, agentPod, token)
	if err != nil {
		return fmt.Errorf("failed to query existing virtual drives: %w", err)
	}

	// If VD doesn't exist, treat as already deleted
	if !existingVDs[virtualDriveUuid] {
		logger.Info("Virtual drive does not exist on proxy, treating as already deleted", "virtual_uuid", virtualDriveUuid)
		return nil
	}

	err = r.removeVirtualDriveViaJSONRPC(ctx, ssdproxyUID, agentPod, token, virtualDriveUuid)
	if err != nil {
		return fmt.Errorf("failed to remove virtual drive %s: %w", virtualDriveUuid, err)
	}

	// Defensively verify removal via query before returning success
	verifyVDs, verifyErr := r.getAddedVirtualDrives(ctx, ssdproxyUID, agentPod, token)
	if verifyErr != nil {
		logger.Error(verifyErr, "Could not verify removal via query, but deletion call succeeded", "virtual_uuid", virtualDriveUuid)
		return fmt.Errorf("could not verify removal via query, but deletion call succeeded %s: %w", virtualDriveUuid, verifyErr)
	}

	if verifyVDs[virtualDriveUuid] {
		return fmt.Errorf("virtual drive %s still exists after deletion call, may be transient", virtualDriveUuid)
	}

	logger.Info("Virtual drive removed successfully and verified via query", "virtual_uuid", virtualDriveUuid)

	return nil
}

// removeVirtualDriveViaJSONRPC removes a virtual drive by calling ssd_proxy_remove_virtual_drive via node agent
func (r *containerReconcilerLoop) removeVirtualDriveViaJSONRPC(ctx context.Context, ssdproxyContainerUuid string, agentPod *v1.Pod, token, virtualUUID string) error {
	return ssdproxy.NewClient(r.KubeService).RemoveVirtualDrive(ctx, agentPod, token, ssdproxyContainerUuid, virtualUUID)
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

// ssdProxyListVirtualDrives lists all virtual drives across all physical drives.
func (r *containerReconcilerLoop) ssdProxyListVirtualDrives(ctx context.Context, ssdproxyContainerUuid string, agentPod *v1.Pod, token string) ([]ssdproxy.VirtualDrive, error) {
	return ssdproxy.NewClient(r.KubeService).ListVirtualDrives(ctx, agentPod, token, ssdproxyContainerUuid)
}

func (r *containerReconcilerLoop) ssdProxyListVirtualDrivesByPhysicalUuid(ctx context.Context, ssdproxyContainerUuid, physicalDriveUuid string, agentPod *v1.Pod, token string) ([]ssdproxy.VirtualDrive, error) {
	return ssdproxy.NewClient(r.KubeService).ListVirtualDrivesByPhysicalUUID(ctx, agentPod, token, ssdproxyContainerUuid, physicalDriveUuid)
}

// getAddedVirtualDrives returns a map of virtual UUIDs that are added to proxy devices
// by calling ssd_proxy JSONRPC API via node agent
func (r *containerReconcilerLoop) getAddedVirtualDrives(ctx context.Context, ssdproxyContainerUuid string, agentPod *v1.Pod, token string) (map[string]bool, error) {
	container := r.container

	_, logger := instrumentation.CreateLogSpan(ctx, "getAddedVirtualDrives", "node", container.GetNodeAffinity())
	defer logger.End()

	if container.Status.Allocations == nil {
		logger.Debug("No allocations to query")
		return make(map[string]bool), nil
	}

	// Collect unique physical drive UUIDs from allocations
	physicalUUIDs := container.Status.Allocations.GetVirtualDrivesPhysicalUuids()

	if len(physicalUUIDs) == 0 {
		logger.Debug("No physical drives to query for virtual drives")
		return make(map[string]bool), nil
	}

	// Query each physical drive via JSONRPC
	addedVirtualDrives := make(map[string]bool)
	for _, physicalUUID := range physicalUUIDs {
		physCtx, l := logger.WithValues("physical_uuid", physicalUUID)
		l.Info("Querying virtual drives for physical drive via JSONRPC")

		virtualDrives, err := r.ssdProxyListVirtualDrivesByPhysicalUuid(physCtx, ssdproxyContainerUuid, physicalUUID, agentPod, token)
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
