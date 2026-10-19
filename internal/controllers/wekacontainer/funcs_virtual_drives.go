package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/services/ssdproxy"
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

	blockedPhysical, err := r.getNodeBlockedDriveUuids(ctx)
	if err != nil {
		return fmt.Errorf("failed to get blocked drive UUIDs from node: %w", err)
	}

	// Check if all allocated virtual drives are already added
	allAdded := true
	for _, vd := range container.Status.Allocations.VirtualDrives {
		if !addedVirtualDrives[vd.VirtualUUID] && !blockedVids[vd.VirtualUUID] && !slices.Contains(blockedPhysical, vd.PhysicalUUID) {
			allAdded = false
			break
		}
	}

	if allAdded {
		logger.Info("All virtual drives already added and present on proxy devices")
		return nil
	}

	var errs []error
	vidsChanged := false

	// Add each virtual drive that hasn't been added yet
	for i := range container.Status.Allocations.VirtualDrives {
		vd := &container.Status.Allocations.VirtualDrives[i]
		vdCtx, l := logger.WithValues("virtual_uuid", vd.VirtualUUID, "label", vd.Label, "physical_uuid", vd.PhysicalUUID)

		if blockedVids[vd.VirtualUUID] {
			l.Info("Virtual drive is blocked on the node, skipping until its removal completes")
			continue
		}

		if slices.Contains(blockedPhysical, vd.PhysicalUUID) {
			l.Info("Physical drive is blocked on the node, skipping virtual drive")
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

		if vidPending(vd) {
			vid, err := ssdproxy.NewClient(r.KubeService).AddVirtualDrive(vdCtx, agentPod, token,
				string(ssdproxyContainer.GetUID()), "label", vd.Label, vd.PhysicalUUID, clusterUUID, vd.CapacityGiB)
			if err != nil {
				l.Error(err, "Failed to add virtual drive via JSONRPC")
				errs = append(errs, fmt.Errorf("failed to add virtual drive with label %s: %w", vd.Label, err))
				continue
			}
			vd.VirtualUUID = vid
			l.Info("Proxy returned VID for label", "returned_vid", vid)
			vidsChanged = true
			continue
		}

		if vidLost(vd) {
			err := fmt.Errorf("VID %s of label %s is missing on the proxy; re-adding it by virtualUuid would not recreate the label-derived VID", vd.VirtualUUID, vd.Label)
			_ = r.RecordEvent(v1.EventTypeWarning, "VirtualDriveVidMismatch", consts.ActionManageDrives, err.Error()) //nolint:errcheck // error return value intentionally not checked
			errs = append(errs, err)
			continue
		}

		// Add the virtual drive via JSONRPC
		_, err := ssdproxy.NewClient(r.KubeService).AddVirtualDrive(vdCtx, agentPod, token,
			string(ssdproxyContainer.GetUID()), "virtualUuid", vd.VirtualUUID, vd.PhysicalUUID, clusterUUID, vd.CapacityGiB)
		if err != nil {
			l.Error(err, "Failed to add virtual drive via JSONRPC")
			errs = append(errs, fmt.Errorf("failed to add virtual drive %s: %w", vd.VirtualUUID, err))
			continue
		}

		l.Info("Virtual drive added successfully via ssdproxy JSONRPC")
	}

	// The cluster add uses the stored VID, so it must be persisted before anything else runs.
	if vidsChanged {
		if err := r.Status().Update(ctx, container); err != nil {
			errs = append(errs, fmt.Errorf("failed to persist VIDs returned by ssdproxy: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors while adding virtual drives: %v", errs)
	}

	logger.InfoWithStatus(codes.Ok, "All virtual drives added successfully")

	return nil
}

// vidPending reports whether vd has a label but no VID yet, so it is added and removed through the label API.
func vidPending(vd *weka.VirtualDrive) bool {
	return vd.Label != "" && vd.VirtualUUID == ""
}

// vidLost reports whether vd holds a VID the proxy resolved from its label (VID differs from label)
// and is missing on the proxy. The caller has already established that it is missing; the legacy add
// cannot recreate it.
func vidLost(vd *weka.VirtualDrive) bool {
	return vd.Label != "" && vd.VirtualUUID != "" && vd.VirtualUUID != vd.Label
}

// proxyPhysicalUuids returns the physical drives the proxy currently lists.
func proxyPhysicalUuids(ctx context.Context, c *ssdproxy.Client, agentPod *v1.Pod, token, ssdproxyUID string) (map[string]bool, error) {
	drives, err := c.ListPhysicalDrives(ctx, agentPod, token, ssdproxyUID)
	if err != nil {
		return nil, fmt.Errorf("failed to list physical drives: %w", err)
	}
	set := make(map[string]bool, len(drives))
	for _, d := range drives {
		set[d.PhysicalUUID] = true
	}
	return set, nil
}

// removeVidByLabel removes a pending allocation's VID by label. A physical drive the proxy no longer
// lists holds no VID, so there is nothing to remove.
func removeVidByLabel(ctx context.Context, c *ssdproxy.Client, agentPod *v1.Pod, token, ssdproxyUID string, onProxy map[string]bool, vd *weka.VirtualDrive) error {
	if !onProxy[vd.PhysicalUUID] {
		return nil
	}
	return c.RemoveVirtualDriveByLabel(ctx, agentPod, token, ssdproxyUID, vd.Label, vd.PhysicalUUID)
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

	proxyClient := ssdproxy.NewClient(r.KubeService)
	var onProxy map[string]bool
	var errs []error
	var attemptedVDUUIDs []string
	var removedVDUUIDs []string
	alreadyDeletedCount := 0

	// Remove each virtual drive via JSONRPC
	for _, vd := range container.Status.Allocations.VirtualDrives {
		vdCtx, l := logger.WithValues("virtual_uuid", vd.VirtualUUID, "label", vd.Label, "physical_uuid", vd.PhysicalUUID)

		// The proxy may have created a VID for this label that was never persisted; without this the
		// VID is orphaned until a manual stale-VID cleanup. Cannot be verified by listing: the VID is unknown.
		if vidPending(&vd) {
			l.Info("Virtual drive has no persisted VID, removing it by label")
			if onProxy == nil {
				onProxy, err = proxyPhysicalUuids(ctx, proxyClient, agentPod, token, string(ssdproxyContainer.GetUID()))
				if err != nil {
					return err
				}
			}
			err := removeVidByLabel(vdCtx, proxyClient, agentPod, token, string(ssdproxyContainer.GetUID()), onProxy, &vd)
			if err != nil {
				l.Error(err, "Failed to remove virtual drive by label via JSONRPC")
				errs = append(errs, fmt.Errorf("failed to remove virtual drive with label %s: %w", vd.Label, err))
			}
			continue
		}

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
