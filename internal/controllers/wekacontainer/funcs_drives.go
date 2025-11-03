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

	if len(container.Status.AddedDrives) == len(container.Status.Allocations.Drives) {
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

	logger.Info("Fetched added drives from weka", "drives", addedSerials)

	currentAddedSerials := container.Status.GetAddedDrivesSerials()

	// sort drives for comparison
	slices.Sort(addedSerials)
	slices.Sort(currentAddedSerials)

	if !slices.Equal(addedSerials, currentAddedSerials) {
		container.Status.AddedDrives = drivesAdded
		err = r.Status().Update(ctx, container)
		if err != nil {
			err = fmt.Errorf("cannot update container status with added drives: %w", err)
			return err
		}
		logger.Info("Updated container status with added drives", "drives", drivesAdded)
	}

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
	agentPod, err := r.findAdjacentNodeAgent(ctx, r.pod)
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
	blockedDrivesStr, ok := node.Annotations["weka.io/blocked-drives"]
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
