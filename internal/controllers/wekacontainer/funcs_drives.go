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
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/pkg/util"
)

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
	if container.UsesDriveSharing() {
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

	timeout := time.Minute * 2
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &timeout)

	var errs []error

	// Handle drive sharing mode (virtual drives) vs regular mode (exclusive drives)
	if container.UsesDriveSharing() {
		// Drive sharing mode: add virtual drives using virtual uuids
		// Build map of added drives by device path
		drivesAddedByVids := make(map[string]bool)
		for _, d := range container.Status.AddedDrives {
			drivesAddedByVids[d.Uuid] = true
		}

		// Add each virtual drive to the cluster
		for _, vd := range container.Status.Allocations.VirtualDrives {
			l := logger.WithValues("virtual_uuid", vd.VirtualUUID, "serial", vd.Serial, "physical_uuid", vd.PhysicalUUID)

			// Check if drive is already added to weka
			if drivesAddedByVids[vd.VirtualUUID] {
				l.Info("Virtual drive already added to weka")
				continue
			}

			l.Info("Adding virtual drive to cluster")

			var pool *string

			if r.container.TlcToQlcRatioEnabled() {
				var p string
				switch vd.Type {
				case "TLC":
					p = "iu4k"
				case "QLC":
					p = "iubig"
				}

				if p != "" {
					pool = &p
				}
			}

			// Add drive using virtual UUID (virtual UUID was already signed on device via AddVirtualDrives)
			err = wekaService.AddDrive(ctx, *container.Status.ClusterContainerID, vd.VirtualUUID, pool)
			if err != nil {
				l.Error(err, "Error adding virtual drive to cluster")
				errs = append(errs, err)
				continue
			}

			l.Info("Virtual drive added to cluster")
			r.RecordEvent("", "VirtualDriveAdded", fmt.Sprintf("Virtual drive %s added to cluster", vd.VirtualUUID))
		}
	} else {
		drivesAddedBySerial := make(map[string]bool)
		for _, s := range container.Status.GetAddedDrivesSerials() {
			drivesAddedBySerial[s] = true
		}

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
			err = wekaService.AddDrive(ctx, *container.Status.ClusterContainerID, kDrives[drive].DevicePath, nil)
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

	logger.Info("Fetched added drives from weka", "count", len(drivesAdded), "drives", drivesAdded)

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

	blockedSerials, err := r.getNodeBlockedDriveSerials(ctx)
	if err != nil {
		return fmt.Errorf("failed to get blocked drives from node: %w", err)
	}

	if len(blockedSerials) == 0 {
		return nil
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
	for _, blockedDriveSerial := range blockedSerials {
		if d, ok := addedDrivesMap[blockedDriveSerial]; ok {
			toRemoveDrives[blockedDriveSerial] = d
		}
	}

	if len(toRemoveDrives) == 0 {
		logger.Info("No drives to remove from weka")
		return nil
	}

	err = r.deallocateDrivesBySerials(ctx, blockedSerials)
	if err != nil {
		return err
	}

	var errs []error

	timeout := time.Minute * 2
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &timeout)

	for _, drive := range toRemoveDrives {
		err := r.removeDriveFromWeka(ctx, &drive, wekaService, container.UsesDriveSharing())
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

func (r *containerReconcilerLoop) RemoveDrivesByPhysicalUuids(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	blockedPhysicalUuids, err := r.getNodeBlockedDriveUuids(ctx)
	if err != nil {
		return fmt.Errorf("failed to get blocked drive UUIDs from node: %w", err)
	}

	if len(blockedPhysicalUuids) == 0 {
		return nil
	}

	// get all virtual drives and create map of virtualUUID -> physicalUUID
	virtualToPhysicalUuidsMap := make(map[string]string)

	ssdproxyContainer, err := r.findSSDProxyOnNode(ctx)
	if err != nil {
		return fmt.Errorf("failed to find ssdproxy container: %w", err)
	}

	agentPod, err := r.GetNodeAgentPod(ctx, container.GetNodeAffinity())
	if err != nil {
		return fmt.Errorf("failed to get node agent pod: %w", err)
	}

	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		return fmt.Errorf("failed to get node agent token: %w", err)
	}

	virtualDrives, err := r.ssdProxyListVirtualDrives(ctx, string(ssdproxyContainer.GetUID()), agentPod, token)
	if err != nil {
		return fmt.Errorf("failed to list virtual drives: %w", err)
	}

	for _, vd := range virtualDrives {
		virtualToPhysicalUuidsMap[vd.VirtualUUID] = vd.PhysicalUUID
	}

	logger.Info("Built virtual to physical UUID map",
		"virtual_drives_count", len(virtualToPhysicalUuidsMap))

	addedDrivesByPhysicalUuidsMap := make(map[string]weka.Drive)
	for _, d := range container.Status.AddedDrives {
		physicalUuid, ok := virtualToPhysicalUuidsMap[d.Uuid]
		if !ok {
			logger.Warn("Added drive virtual UUID has no matching physical UUID", "virtual_uuid", d.Uuid)

			_ = r.RecordEventThrottled(v1.EventTypeWarning, "DriveRemovalSkipped", fmt.Sprintf("Added drive virtual UUID %s has no matching physical UUID", d.Uuid), time.Minute*1)
			continue
		}

		addedDrivesByPhysicalUuidsMap[physicalUuid] = d
	}

	toRemoveDrives := make(map[string]weka.Drive)

	for _, blockedDriveUuid := range blockedPhysicalUuids {
		if d, ok := addedDrivesByPhysicalUuidsMap[blockedDriveUuid]; ok {
			toRemoveDrives[blockedDriveUuid] = d
		}
	}

	if len(toRemoveDrives) == 0 {
		logger.Info("No drives to remove from weka")
		return nil
	}

	err = r.deallocateDrivesByPhysicalUuids(ctx, blockedPhysicalUuids)
	if err != nil {
		return err
	}

	var errs []error

	timeout := time.Minute * 2
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &timeout)
	for _, drive := range toRemoveDrives {
		err := r.removeDriveFromWeka(ctx, &drive, wekaService, container.UsesDriveSharing())
		if err != nil {
			errs = append(errs, err)
			continue
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during drive removal: %v", errs)
	}

	return nil
}

// TODO: make it work with physical UUIDs as well
func (r *containerReconcilerLoop) MarkDrivesForRemoval(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "MarkDrivesForRemoval")
	defer end()

	container := r.container

	if unhealthy, _, _ := utils.IsUnhealthy(ctx, container); unhealthy {
		return errors.New("container is uneligible for drive allocation (unhealthy)")
	}

	var toRemoveSerialIDs []string

	// check if any container has failed drives
	driveFailures := container.Status.GetStats().Drives.DriveFailures
	if len(driveFailures) > 0 {
		toRemoveSerialIDs = make([]string, 0, len(driveFailures))
		for _, driveFailure := range driveFailures {
			logger.Info("Drive marked as failed, marking for removal", "drive", driveFailure.SerialId)
			toRemoveSerialIDs = append(toRemoveSerialIDs, driveFailure.SerialId)
		}
	}

	if len(toRemoveSerialIDs) == 0 {
		logger.Info("No drives to mark for removal")
		return nil
	}

	// check if drives are already "blocked" on the node
	blockedDriveSerials, err := r.getNodeBlockedDriveSerials(ctx)
	if err != nil {
		return fmt.Errorf("failed to get blocked drives from node: %w", err)
	}
	allBlocked := true
	for _, drive := range toRemoveSerialIDs {
		if !slices.Contains(blockedDriveSerials, drive) {
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

func (r *containerReconcilerLoop) getNodeBlockedDriveUuids(ctx context.Context) (blockedPhysicalUuids []string, err error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getNodeBlockedDriveUuids")
	defer end()

	node := r.node
	if node == nil {
		return nil, errors.New("node is nil")
	}

	// drives blocked by physical UUIDs (for drive sharing / proxy mode)
	blockedPhysicalUuids = make([]string, 0)
	blockedUuidsStr, ok := node.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids]
	if ok && blockedUuidsStr != "" {
		if err := json.Unmarshal([]byte(blockedUuidsStr), &blockedPhysicalUuids); err != nil {
			return nil, fmt.Errorf("failed to unmarshal blocked shared drives: %w", err)
		}
	}

	logger.Debug("Fetched blocked drives from node annotation", "blocked_drives_uuids", blockedPhysicalUuids)

	return blockedPhysicalUuids, nil
}

func (r *containerReconcilerLoop) getNodeBlockedDriveSerials(ctx context.Context) (blockedSerials []string, err error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getNodeBlockedDriveSerials")
	defer end()

	node := r.node
	if node == nil {
		return nil, errors.New("node is nil")
	}

	// drives blocked by serial IDs
	blockedSerials = make([]string, 0)
	blockedDrivesStr, ok := node.Annotations[consts.AnnotationBlockedDrives]
	if ok && blockedDrivesStr != "" {
		err := json.Unmarshal([]byte(blockedDrivesStr), &blockedSerials)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal blocked drives: %v", err)
		}
	}

	logger.Debug("Fetched blocked drives from node annotation", "blocked_drives_serials", blockedSerials)

	return blockedSerials, nil
}

func (r *containerReconcilerLoop) removeDriveFromWeka(ctx context.Context, drive *weka.Drive, wekaService services.WekaService, useDriveSharing bool) error {
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

	if useDriveSharing {
		// remove virtual drive on ssdproxy
		err = r.removeVirtualDrive(ctx, drive.Uuid)
		if err != nil {
			return err
		}
	}

	return nil
}
