package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/node_agent"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/pkg/util"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SetSSDProxyUID finds and records the ssdproxy container UID for drive-sharing containers
// This must be called after node affinity is set but before the pod is created
func (r *containerReconcilerLoop) SetSSDProxyUID(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "SetSSDProxyUID")
	defer end()

	container := r.container

	// Only for drive sharing mode
	if !container.Spec.UseDriveSharing {
		return nil
	}

	// Find the ssdproxy container on the same node
	ssdproxyContainer, err := r.findSSDProxyOnNode(ctx)
	if err != nil {
		return fmt.Errorf("failed to find ssdproxy container on node: %w", err)
	}

	// Use the WekaContainer CR UID because that's what determines the directory structure
	// on the host: /opt/k8s-weka/containers/<CR_UID>/
	proxyUID := string(ssdproxyContainer.GetUID())
	oldProxyUID := container.Status.SSDProxyUID

	// If already set to the correct value, nothing to do
	if oldProxyUID == proxyUID {
		return nil
	}

	// If pod exists and we're changing the proxy UID (or setting it for first time),
	// the pod needs to be recreated with the correct SSDPROXY_CONTAINER_UID env var
	if r.pod != nil && oldProxyUID != proxyUID {
		logger.Info("SSDProxy UID needs to be set/updated, pod needs to be recreated",
			"old_proxy_uid", oldProxyUID,
			"new_proxy_uid", proxyUID,
			"pod_name", r.pod.Name)
		// Delete the pod so it gets recreated with correct proxy UID env var
		if err := r.Manager.GetClient().Delete(ctx, r.pod); err != nil {
			return fmt.Errorf("failed to delete pod for ssdproxy UID update: %w", err)
		}
		logger.Info("Deleted pod, it will be recreated with correct SSDPROXY_CONTAINER_UID")
	}

	logger.Info("Setting ssdproxy UID for drive-sharing container",
		"proxy_uid", proxyUID,
		"proxy_name", ssdproxyContainer.Name,
		"old_proxy_uid", oldProxyUID)

	container.Status.SSDProxyUID = proxyUID
	if err := r.Status().Update(ctx, container); err != nil {
		return fmt.Errorf("failed to update container status with ssdproxy UID: %w", err)
	}

	return nil
}

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
	signedVirtualDrives, err := r.getSignedVirtualDrives(ctx, ssdproxyContainer, agentPod, token)
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

		err := r.removeVirtualDriveViaJSONRPC(ctx, ssdproxyContainer, agentPod, token, vd.VirtualUUID)
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
func (r *containerReconcilerLoop) removeVirtualDriveViaJSONRPC(ctx context.Context, ssdproxyContainer *weka.WekaContainer, agentPod *v1.Pod, token string, virtualUUID string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "removeVirtualDriveViaJSONRPC")
	defer end()

	logger.Info("Calling ssd_proxy_remove_virtual_drive via JSONRPC", "virtual_uuid", virtualUUID)

	method := "ssd_proxy_remove_virtual_drive"
	params := map[string]any{
		"virtualUuid": virtualUUID,
	}

	payload := node_agent.JSONRPCProxyPayload{
		ContainerId: string(ssdproxyContainer.GetUID()),
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

func (r *containerReconcilerLoop) EnsureDrives(ctx context.Context) error {
	container := r.container
	pod := r.pod
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureDrives", "cluster_guid", container.Status.ClusterID, "container_id", container.Status.ClusterID)
	defer end()

	if container.Status.ClusterContainerID == nil {
		err := errors.New("container cluster ID is not set, cannot ensure drives")
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*10)
	}

	// Determine expected drive count based on mode
	var expectedDriveCount int
	if container.Spec.UseDriveSharing {
		expectedDriveCount = len(container.Status.Allocations.VirtualDrives)
	} else {
		expectedDriveCount = len(container.Status.Allocations.Drives)
	}

	if len(container.Status.AddedDrives) == expectedDriveCount {
		return r.updateContainerStatusIfNotEquals(ctx, weka.Running)
	}

	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		return err
	}

	// get drives that were discovered
	// (these drives are requested in allocations and exist in kernel)
	var kDrives map[string]domain.DriveInfo
	// NOTE: used closure not to execute this function if we don't need to add any drives
	getKernelDrives := func() error {
		if kDrives == nil {
			kDrives, err = r.getKernelDrives(ctx, executor)
			if err != nil {
				return fmt.Errorf("error getting kernel drives: %v", err)
			} else {
				logger.Info("Kernel drives fetched", "drives", kDrives)
			}
		}
		return nil
	}

	drivesAddedBySerial := make(map[string]bool)
	for _, s := range container.Status.GetAddedDrivesSerials() {
		drivesAddedBySerial[s] = true
	}

	timeout := time.Minute * 2
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &timeout)

	var errs []error

	// Handle drive sharing mode (virtual drives) vs regular mode (exclusive drives)
	if container.Spec.UseDriveSharing {
		// Drive sharing mode: add virtual drives using device paths
		// Build map of added drives by device path
		drivesAddedByPath := make(map[string]bool)
		for _, d := range container.Status.AddedDrives {
			drivesAddedByPath[d.DevicePath] = true
		}

		// Add each virtual drive to the cluster
		for _, vd := range container.Status.Allocations.VirtualDrives {
			l := logger.WithValues("virtualUUID", vd.VirtualUUID, "devicePath", vd.DevicePath)

			// Check if drive is already added to weka
			if drivesAddedByPath[vd.DevicePath] {
				l.Info("Virtual drive already added to weka")
				continue
			}

			l.Info("Adding virtual drive to cluster")

			// Add drive using virtual UUID (virtual UUID was already signed on device via AddVirtualDrives)
			err = wekaService.AddDrive(ctx, *container.Status.ClusterContainerID, vd.VirtualUUID)
			if err != nil {
				l.Error(err, "Error adding virtual drive to cluster")
				errs = append(errs, err)
				continue
			}

			l.Info("Virtual drive added to cluster")
			r.RecordEvent("", "VirtualDriveAdded", fmt.Sprintf("Virtual drive %s added to cluster", vd.VirtualUUID))
		}
	} else {
		// Regular mode: add exclusive drives
		// Adding drives to weka one by one
		for _, drive := range container.Status.Allocations.Drives {
			l := logger.WithValues("drive_name", drive)

			// check if drive is already added to weka
			if _, ok := drivesAddedBySerial[drive]; ok {
				l.Info("drive is already added to weka")
				continue
			}

			l.Info("Attempting to configure drive")

			err := getKernelDrives()
			if err != nil {
				return err
			}
			if _, ok := kDrives[drive]; !ok {
				err := fmt.Errorf("drive %s not found in kernel", drive)
				l.Error(err, "Error configuring drive")
				errs = append(errs, err)
				continue
			}

			if kDrives[drive].Partition == "" {
				err := fmt.Errorf("drive %v is not partitioned", kDrives[drive])
				l.Error(err, "Error configuring drive")
				errs = append(errs, err)
				continue
			}

			l = l.WithValues("partition", kDrives[drive].Partition, "weka_guid", kDrives[drive].WekaGuid)

			if kDrives[drive].IsSigned {
				l.Info("Drive has Weka signature on it, forbidding usage")
				err := fmt.Errorf("drive %s has Weka signature on it, forbidding usage", drive)
				errs = append(errs, err)
				continue
			}

			l.Info("Adding drive into system")
			// TODO: We need to login here. Maybe handle it on wekaauthcli level?
			err = wekaService.AddDrive(ctx, *container.Status.ClusterContainerID, kDrives[drive].DevicePath)
			if err != nil {
				l.Error(err, "Error adding drive into system")
				errs = append(errs, err)
				continue
			} else {
				l.Info("Drive added into system")
				r.RecordEvent("", "DriveAdded", fmt.Sprintf("Drive %s added", drive))
			}
		}
	}

	if len(errs) > 0 {
		err := fmt.Errorf("errors while adding drives: %v", errs)
		return err
	}

	logger.InfoWithStatus(codes.Ok, "All drives added")

	return r.updateContainerStatusIfNotEquals(ctx, weka.Running)
}

func (r *containerReconcilerLoop) UpdateWekaAddedDrives(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	timeout := time.Minute * 2
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &timeout)

	// NOTE: this is a costly operation weka-side, so we should do it only once per container reconciliation
	drivesAdded, err := wekaService.ListContainerDrives(ctx, *container.Status.ClusterContainerID)
	if err != nil {
		return err
	}

	addedSerials := make([]string, 0, len(drivesAdded))
	for _, drive := range drivesAdded {
		addedSerials = append(addedSerials, drive.SerialNumber)
	}

	logger.Info("Fetched added drives from weka", "count", len(drivesAdded), "serials", addedSerials)

	container.Status.AddedDrives = drivesAdded
	err = r.Status().Update(ctx, container)
	if err != nil {
		err = fmt.Errorf("cannot update container status with added drives: %w", err)
		return err
	}

	logger.Info("Updated container status with added drives", "count", len(drivesAdded))

	return nil
}

func (r *containerReconcilerLoop) RemoveDrives(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	blockedDrives, err := r.getNodeBlockedDrives(ctx)
	if err != nil {
		return fmt.Errorf("failed to get blocked drives from node: %w", err)
	}

	addedDrivesMap := make(map[string]weka.Drive)
	for _, d := range container.Status.AddedDrives {
		if d.SerialNumber == "" {
			logger.Warn("Drive has no serial number", "drive", d)
			continue
		}
		addedDrivesMap[d.SerialNumber] = d
	}

	toRemoveDrives := make(map[string]weka.Drive)

	// check which drives from "blocked drives" list are still present in weka
	for _, blockedDriveSerial := range blockedDrives {
		if d, ok := addedDrivesMap[blockedDriveSerial]; ok {
			toRemoveDrives[blockedDriveSerial] = d
		}
	}

	if len(toRemoveDrives) == 0 {
		logger.Info("No drives to remove from weka")
		return nil
	}

	// trigger re-allocation if any of the drives in Allocations.RemoveDrives is still in driveAllocations
	triggerDeallocation := false
	for _, drive := range container.Status.Allocations.Drives {
		if slices.Contains(blockedDrives, drive) {
			triggerDeallocation = true
			break
		}
	}

	// Deallocate drives marked for removal
	if triggerDeallocation {
		err = r.deallocateRemovedDrives(ctx, blockedDrives)
		if err != nil {
			return err
		}
	}

	var errs []error

	timeout := time.Minute * 2
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &timeout)

	for _, drive := range toRemoveDrives {
		err := r.removeDriveFromWeka(ctx, &drive, wekaService)
		if err != nil {
			errs = append(errs, err)
			continue
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during drive replacement: %v", errs)
	}

	// adding of new drive is covered by EnsureDrives
	return nil
}

func (r *containerReconcilerLoop) MarkDrivesForRemoval(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "MarkDrivesForRemoval")
	defer end()

	container := r.container

	if unhealthy, _, _ := utils.IsUnhealthy(ctx, container); unhealthy {
		return errors.New("container is uneligible for drive allocation (unhealthy)")
	}

	var containerDriveFailures []string

	// check if any container has failed drives
	driveFailures := container.Status.GetStats().Drives.DriveFailures
	if len(driveFailures) > 0 {
		containerDriveFailures = make([]string, 0, len(driveFailures))
		for _, driveFailure := range driveFailures {
			logger.Info("Drive marked as failed, marking for removal", "drive", driveFailure.SerialId)
			containerDriveFailures = append(containerDriveFailures, driveFailure.SerialId)
		}
	}

	if len(containerDriveFailures) == 0 {
		logger.Info("No drives to mark for removal")
		return nil
	}

	toRemoveSerialIDs := make([]string, 0, len(containerDriveFailures))
	for _, drive := range containerDriveFailures {
		toRemoveSerialIDs = append(toRemoveSerialIDs, drive)
	}

	// check if drives are already "blocked" on the node
	blockedDrives, err := r.getNodeBlockedDrives(ctx)
	if err != nil {
		return fmt.Errorf("failed to get blocked drives from node: %w", err)
	}
	allBlocked := true
	for _, drive := range toRemoveSerialIDs {
		if !slices.Contains(blockedDrives, drive) {
			allBlocked = false
			break
		}
	}
	if allBlocked {
		logger.Info("Drives are already blocked on the node, no need to block again", "drives", toRemoveSerialIDs)
		return nil
	}

	ctx, logger, end = instrumentation.GetLogSpan(ctx, "BlockDrives", "drives", toRemoveSerialIDs)
	defer end()

	logger.Info("Blocking drives on the node")

	// call "block-drives" manual operation for the drives to be removed
	payload := &weka.BlockDrivesPayload{
		SerialIDs: toRemoveSerialIDs,
		Node:      string(container.GetNodeAffinity()),
	}
	op := operations.NewBlockDrivesOperation(r.Manager, payload, nil, nil, nil)
	err = operations.ExecuteOperation(ctx, op)
	if err != nil {
		return fmt.Errorf("failed to block drives %v: %w", toRemoveSerialIDs, err)
	}

	_ = r.RecordEvent(v1.EventTypeWarning, "DrivesMarkedForRemoval", fmt.Sprintf("Drives %v marked for removal from container", toRemoveSerialIDs))

	return nil
}

func (r *containerReconcilerLoop) getKernelDrives(ctx context.Context, executor util.Exec) (map[string]domain.DriveInfo, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getKernelDrives")
	defer end()

	// Try to get drives from node-agent first
	drives, err := r.getKernelDrivesFromNodeAgent(ctx)
	if err != nil {
		logger.Info("Failed to get drives from node-agent, falling back to old implementation", "error", err)
		// Fallback to old implementation: read drives.json from pod
		drives, err = r.getKernelDrivesFromPod(ctx, executor)
		if err != nil {
			return nil, err
		}
	}

	return drives, nil
}

func (r *containerReconcilerLoop) getKernelDrivesFromNodeAgent(ctx context.Context) (map[string]domain.DriveInfo, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getKernelDrivesFromNodeAgent")
	defer end()

	// Find node-agent pod on the same node as this container
	agentPod, err := r.GetNodeAgentPod(ctx, r.container.GetNodeAffinity())
	if err != nil {
		return nil, fmt.Errorf("failed to get node-agent pod: %w", err)
	}

	// Get token for authentication
	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get node-agent token: %w", err)
	}

	// Call /findDrives endpoint
	url := fmt.Sprintf("http://%s:8090/findDrives", agentPod.Status.PodIP)

	timeout := time.Second * 30
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := util.SendJsonRequest(ctx, url, []byte("{}"), util.RequestOptions{AuthHeader: "Token " + token})
	if err != nil {
		return nil, fmt.Errorf("failed to call node-agent: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("findDrives failed: %s, status: %d", string(body), resp.StatusCode)
	}

	var response struct {
		Drives []domain.DriveInfo `json:"drives"`
		Error  string             `json:"error,omitempty"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if response.Error != "" {
		return nil, fmt.Errorf("node-agent returned error: %s", response.Error)
	}

	logger.Info("Successfully fetched drives from node-agent", "count", len(response.Drives))

	// Convert to map by serial ID
	serialIdMap := make(map[string]domain.DriveInfo)
	for _, drive := range response.Drives {
		serialIdMap[drive.SerialId] = drive
	}

	return serialIdMap, nil
}

func (r *containerReconcilerLoop) getKernelDrivesFromPod(ctx context.Context, executor util.Exec) (map[string]domain.DriveInfo, error) {
	stdout, _, err := executor.ExecNamed(ctx, "FetchKernelDrives",
		[]string{"bash", "-ce", "cat /opt/weka/k8s-runtime/drives.json"})
	if err != nil {
		return nil, err
	}
	var drives []domain.DriveInfo
	err = json.Unmarshal(stdout.Bytes(), &drives)
	if err != nil {
		return nil, err
	}
	serialIdMap := make(map[string]domain.DriveInfo)
	for _, drive := range drives {
		serialIdMap[drive.SerialId] = drive
	}

	return serialIdMap, nil
}

func (r *containerReconcilerLoop) getNodeBlockedDrives(ctx context.Context) ([]string, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getNodeBlockedDrives")
	defer end()

	node := r.node
	if node == nil {
		return nil, errors.New("node is nil")
	}

	annotationBlockedDrives := make([]string, 0)
	blockedDrivesStr, ok := node.Annotations[consts.AnnotationBlockedDrives]
	if ok && blockedDrivesStr != "" {
		err := json.Unmarshal([]byte(blockedDrivesStr), &annotationBlockedDrives)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal weka-allocations: %v", err)
		}
	}

	logger.Info("Fetched blocked drives from node annotation", "blocked_drives", annotationBlockedDrives)

	return annotationBlockedDrives, nil
}

func (r *containerReconcilerLoop) removeDriveFromWeka(ctx context.Context, drive *weka.Drive, wekaService services.WekaService) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "removeReplacedDriveFromWeka", "drive_uuid", drive.Uuid, "drive_serial", drive.SerialNumber)
	defer end()

	reFetchedDrive, err := wekaService.GetClusterDrive(ctx, drive.Uuid)
	var notFoundErr *services.DriveNotFound
	if errors.As(err, &notFoundErr) {
		logger.Info("Drive not found in weka, assuming already removed")
		return nil
	}
	if err != nil {
		err = fmt.Errorf("error fetching drive %s (%s) before removal: %w", drive.SerialNumber, drive.Uuid, err)
		return err
	}

	statusActive := "ACTIVE"
	statusInactive := "INACTIVE"

	switch reFetchedDrive.Status {
	case statusActive:
		logger.Info("Deactivating drive")
		err := wekaService.DeactivateDrive(ctx, drive.Uuid)
		if err != nil {
			err = fmt.Errorf("error deactivating drive %s: %w", drive.SerialNumber, err)
			return err
		}

		_ = r.RecordEvent("", "DriveDeactivated", fmt.Sprintf("Drive %s deactivated", drive.SerialNumber))
	case statusInactive:
		logger.Debug("Drive is inactive")
	default:
		err := fmt.Errorf("drive has status '%s', wait for it to become '%s'", drive.Status, statusInactive)
		return err
	}

	// remove failed (replaced) drive from weka
	logger.Info("Removing drive")

	err = wekaService.RemoveDrive(ctx, drive.Uuid)
	if err != nil {
		err = fmt.Errorf("error removing drive %s: %w", drive.SerialNumber, err)
		return err
	}

	_ = r.RecordEvent("", "DriveRemoved", fmt.Sprintf("Drive %s removed", drive.SerialNumber))

	logger.Info("Drive removed from weka")
	return nil
}

// findSSDProxyOnNode finds the ssdproxy container on the same node as the current drive container
func (r *containerReconcilerLoop) findSSDProxyOnNode(ctx context.Context) (*weka.WekaContainer, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "findSSDProxyOnNode")
	defer end()

	container := r.container
	nodeName := container.GetNodeAffinity()
	if nodeName == "" {
		return nil, errors.New("container has no node affinity")
	}

	// Get the operator namespace where ssdproxy containers are deployed
	operatorNamespace, err := util.GetPodNamespace()
	if err != nil {
		return nil, fmt.Errorf("failed to get operator namespace: %w", err)
	}

	// List all ssdproxy containers in the operator namespace
	// Note: We don't filter by cluster because ssdproxy containers are shared across clusters on the same node
	containerList := &weka.WekaContainerList{}
	listOpts := []client.ListOption{
		client.InNamespace(operatorNamespace),
		client.MatchingLabels{"weka.io/mode": weka.WekaContainerModeSSDProxy},
	}

	err = r.Manager.GetClient().List(ctx, containerList, listOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to list ssdproxy containers: %w", err)
	}

	// Find the ssdproxy on the same node
	for i := range containerList.Items {
		proxy := &containerList.Items[i]
		if proxy.GetNodeAffinity() == nodeName {
			logger.Info("Found ssdproxy container on node",
				"ssdproxy_name", proxy.Name,
				"ssdproxy_uid", proxy.UID,
				"node", nodeName)
			return proxy, nil
		}
	}

	return nil, fmt.Errorf("no ssdproxy container found on node %s", nodeName)
}
