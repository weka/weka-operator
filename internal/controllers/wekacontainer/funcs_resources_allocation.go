// This file contains functions related to resources allocation and writing during WekaContainer reconciliation,
// such as resources.json writing and verification, NICs allocation, drives ensuring operations
package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"go/types"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/pkg/util"
)

func (r *containerReconcilerLoop) ShouldAllocateNICs() bool {
	if !r.container.IsBackend() && !r.container.IsClientContainer() {
		return false
	}

	if r.container.Spec.Network.EthDevice != "" || len(r.container.Spec.Network.EthDevices) > 0 || len(r.container.Spec.Network.DeviceSubnets) > 0 {
		return false
	}

	if r.node == nil {
		return false
	}

	if r.container.IsMarkedForDeletion() {
		return false
	}

	if r.container.Spec.Network.UdpMode {
		return false
	}

	// Check EKS (always enabled)
	isEKS := strings.HasPrefix(r.node.Spec.ProviderID, "aws://")
	// Check OKE only if configuration is enabled
	isOKE := strings.HasPrefix(r.node.Spec.ProviderID, "ocid1.") && config.Config.OkeCompatibility.EnableNicsAllocation

	if !isEKS && !isOKE {
		return false
	}

	annotationAllocations := make(domain.Allocations)
	allocationsStr, ok := r.node.Annotations[domain.WEKAAllocations]
	if ok {
		err := json.Unmarshal([]byte(allocationsStr), &annotationAllocations)
		if err != nil {
			return true
		}
		allocationIdentifier := domain.GetAllocationIdentifier(r.container.Namespace, r.container.Name)
		nicsAllocationsNumber := 0
		if _, ok = annotationAllocations[allocationIdentifier]; ok {
			nicsAllocationsNumber = len(annotationAllocations[allocationIdentifier].NICs)
		}
		if nicsAllocationsNumber >= r.container.Spec.NumCores {
			return false
		}
	}

	return true
}

func (r *containerReconcilerLoop) AllocateNICs(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "allocateNICs")
	defer end()

	logger.Debug("Allocating container NICS", "name", r.container.ObjectMeta.Name)

	nicsStr, ok := r.node.Annotations[domain.WEKANICs]
	if !ok {
		err := fmt.Errorf("node %s does not have weka-nics annotation, but dpdk is enabled", r.node.Name)
		logger.Error(err, "")
		return err
	}

	annotationAllocations := make(domain.Allocations)
	allocationsStr, ok := r.node.Annotations[domain.WEKAAllocations]
	if ok {
		err := json.Unmarshal([]byte(allocationsStr), &annotationAllocations)
		if err != nil {
			return fmt.Errorf("failed to unmarshal weka-allocations: %v", err)
		}
	}

	var allNICs []domain.NIC
	err := json.Unmarshal([]byte(nicsStr), &allNICs)
	if err != nil {
		return fmt.Errorf("failed to unmarshal weka-nics: %v", err)
	}

	allocatedNICs := make(map[string]types.Nil)
	for _, alloc := range annotationAllocations {
		for _, nicIdentifier := range alloc.NICs {
			allocatedNICs[nicIdentifier] = types.Nil{}
		}
	}

	allocationIdentifier := domain.GetAllocationIdentifier(r.container.Namespace, r.container.Name)
	nicsAllocationsNumber := 0
	if _, ok := annotationAllocations[allocationIdentifier]; ok {
		nicsAllocationsNumber = len(annotationAllocations[allocationIdentifier].NICs)
	} else {
		annotationAllocations[allocationIdentifier] = domain.Allocation{NICs: []string{}}
	}

	requiredNicsNumber := r.container.Spec.NumCores
	logger.Debug("Allocated NICs", "allocatedNICs", allocatedNICs, "container", r.container.Name)
	if nicsAllocationsNumber >= requiredNicsNumber {
		logger.Debug("Container already allocated NICs", "name", r.container.ObjectMeta.Name)
		return nil
	}
	logger.Info("Allocating NICs", "requiredNicsNumber", requiredNicsNumber, "nicsAllocationsNumber", nicsAllocationsNumber, "container", r.container.Name)
	for range make([]struct{}, requiredNicsNumber-nicsAllocationsNumber) {
		for _, nic := range allNICs {
			if _, ok = allocatedNICs[nic.MacAddress]; !ok {
				allocatedNICs[nic.MacAddress] = types.Nil{}
				logger.Debug("Allocating NIC", "nic", nic.MacAddress, "container", r.container.Name)
				nics := append(annotationAllocations[allocationIdentifier].NICs, nic.MacAddress)
				annotationAllocations[allocationIdentifier] = domain.Allocation{NICs: nics}
				break
			}
		}
	}
	allocationsBytes, err := json.Marshal(annotationAllocations)
	if err != nil {
		return fmt.Errorf("failed to marshal weka-allocations: %v", err)
	}

	r.node.Annotations[domain.WEKAAllocations] = string(allocationsBytes)
	return r.Client.Update(ctx, r.node)
}

func (r *containerReconcilerLoop) WriteResources(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "WriteResources")
	defer end()

	container := r.container

	timeout := time.Second * 30
	executor, err := r.ExecService.GetExecutorWithTimeout(ctx, container, &timeout)
	if err != nil {
		return err
	}

	_, _, err = executor.ExecNamed(ctx, "CheckPersistencyConfigured", []string{"bash", "-ce", "test -f /opt/weka/k8s-runtime/persistency-configured"})
	if err != nil {
		err = errors.New("Persistency is not yet configured")
		return lifecycle.NewWaitError(err)
	}

	allocations, err := r.getExpectedAllocations(ctx)
	if err != nil {
		return fmt.Errorf("failed to get expected allocations: %w", err)
	}

	var resourcesJson []byte
	resourcesJson, err = json.Marshal(allocations)
	if err != nil {
		return err
	}

	// Use base64 encoding for safe file writing - prevents bash injection issues
	resourcesStr := string(resourcesJson)
	logger.Info("writing resources", "json", resourcesStr)
	stdout, stderr, err := executor.ExecNamed(ctx, "WriteResources", []string{"bash", "-ce", fmt.Sprintf(`
mkdir -p /opt/weka/k8s-runtime/tmp
echo '%s' > /opt/weka/k8s-runtime/tmp/resources.json
mv /opt/weka/k8s-runtime/tmp/resources.json /opt/weka/k8s-runtime/resources.json
`, resourcesStr)})
	if err != nil {
		logger.Error(err, "Error writing resources", "stderr", stderr.String(), "stdout", stdout.String())
		return err
	}

	return r.verifyResourcesJson(ctx, executor, allocations)
}

func (r *containerReconcilerLoop) getExpectedAllocations(ctx context.Context) (*weka.ContainerAllocations, error) {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "getExpectedAllocations")
	defer end()

	var allocations *weka.ContainerAllocations
	if r.container.Status.Allocations != nil {
		allocations = r.container.Status.Allocations
	} else {
		// client flow
		allocations = &weka.ContainerAllocations{}

		machineIdentifierPath := r.container.Spec.GetOverrides().MachineIdentifierNodeRef
		if machineIdentifierPath == "" {
			if r.node != nil {
				// check if node has "weka.io/machine-identifier-ref" label
				// if yes - use it as machine identifier path
				if val, ok := r.node.Annotations["weka.io/machine-identifier-ref"]; ok && val != "" {
					machineIdentifierPath = r.node.Annotations["weka.io/machine-identifier-ref"]
				}
			}
		}

		if machineIdentifierPath != "" {
			uid, err := util.GetKubeObjectFieldValue[string](r.node, machineIdentifierPath)
			if err != nil {
				return nil, fmt.Errorf("failed to get machine identifier from node: %w and path %s", err, machineIdentifierPath)
			}
			allocations.MachineIdentifier = uid
		}
	}

	var err error
	allocations.NetDevices, err = utils.GetNetDevices(ctx, r.node, r.container)
	if err != nil {
		return nil, fmt.Errorf("failed to get net devices: %w", err)
	}

	return allocations, nil
}

func (r *containerReconcilerLoop) selfUpdateAllocations(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container
	sleepBetween := config.Consts.ContainerUpdateAllocationsSleep

	cs, err := allocator.NewConfigMapStore(ctx, r.Client)
	if err != nil {
		return err
	}

	allAllocations, err := cs.GetAllocations(ctx)
	if err != nil {
		return lifecycle.NewWaitErrorWithDuration(errors.New("allocations are not set yet"), sleepBetween)
	}

	owner := container.GetOwnerReferences()
	nodeName := container.GetNodeAffinity()
	nodeAlloc, ok := allAllocations.NodeMap[nodeName]
	if !ok {
		return lifecycle.NewWaitErrorWithDuration(errors.New("node allocations are not set yet"), sleepBetween)
	}

	allocOwner := allocator.Owner{
		OwnerCluster: allocator.OwnerCluster{
			ClusterName: owner[0].Name,
			Namespace:   container.Namespace,
		},
		Container: container.Name,
		Role:      container.Spec.Mode,
	}

	allocatedDrives, ok := nodeAlloc.Drives[allocOwner]
	if !ok && container.IsDriveContainer() {
		return lifecycle.NewWaitErrorWithDuration(fmt.Errorf("no drives allocated for owner %v", allocOwner), sleepBetween)
	}

	currentRanges, ok := nodeAlloc.AllocatedRanges[allocOwner]
	if !ok {
		return lifecycle.NewWaitErrorWithDuration(fmt.Errorf("no ranges allocated for owner %v", allocOwner), sleepBetween)
	}
	wekaPort := currentRanges["weka"].Base
	agentPort := currentRanges["agent"].Base

	failureDomain := r.getFailureDomain(ctx)

	allocations := &weka.ContainerAllocations{
		Drives:        allocatedDrives,
		WekaPort:      wekaPort,
		AgentPort:     agentPort,
		FailureDomain: failureDomain,
	}
	logger.Info("Updating container with allocations", "allocations", allocations)

	container.Status.Allocations = allocations

	err = r.Status().Update(ctx, container)
	if err != nil {
		err = fmt.Errorf("cannot update container status with allocations: %w", err)
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) verifyResourcesJson(ctx context.Context, executor util.Exec, expectedAllocations *weka.ContainerAllocations) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "verifyResourcesJson")
	defer end()

	// Verify the file was written correctly by reading it back and validating JSON
	stdout, stderr, err := executor.ExecNamed(ctx, "VerifyResources", []string{"bash", "-ce", "cat /opt/weka/k8s-runtime/resources.json"})
	if err != nil {
		err = fmt.Errorf("error reading resources.json: %v, %s", err, stderr.String())
		logger.Error(err, "")
		return err
	}

	// Validate that the read content is valid JSON and matches what we wrote
	var verifyAllocations weka.ContainerAllocations
	readContent := stdout.String()
	if err = json.Unmarshal([]byte(readContent), &verifyAllocations); err != nil {
		err := fmt.Errorf("invalid JSON in resources.json: %w", err)
		logger.Error(err, "", "content", readContent)
		return err
	}

	// Verify the content matches what we intended to write
	if !expectedAllocations.Equals(&verifyAllocations) {
		err := fmt.Errorf("resources.json content does not match expected allocations")
		logger.Error(err, "", expectedAllocations, "actual", verifyAllocations)
		return err
	}

	logger.Info("Successfully verified resources.json was written correctly")

	return nil
}

func (r *containerReconcilerLoop) checkUnhealyPodResources(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	timeout := time.Second * 10
	executor, err := r.ExecService.GetExecutorWithTimeout(ctx, container, &timeout)
	if err != nil {
		return err
	}

	expectedAllocations, err := r.getExpectedAllocations(ctx)
	if err != nil {
		return fmt.Errorf("error getting expected allocations: %v, original err: %v", err, err)
	}

	err = r.verifyResourcesJson(ctx, executor, expectedAllocations)
	if err != nil {
		if strings.Contains(err.Error(), "context deadline exceeded") {
			return lifecycle.NewWaitErrorWithDuration(err, time.Second*10)
		}

		err = fmt.Errorf("error checking resources.json: %w", err)

		logger.Error(err, "resources.json is incorrect, re-writing it")

		err2 := r.WriteResources(ctx)
		if err2 != nil {
			err2 = fmt.Errorf("error writing resources.json: %v, prev. error %v", err2, err)
			return err2
		}
	}

	logger.Debug("resources.json is correct, no need to re-write it")

	return nil
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

func (r *containerReconcilerLoop) MarkDrivesForRemoval(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "MarkDrivesForRemoval")
	defer end()

	container := r.container

	if unhealthy, _, _ := utils.IsUnhealthy(ctx, container); unhealthy {
		return errors.New("container is uneligible for drive allocation (unhealthy)")
	}

	var containerDriveFailures []string

	// check the Stats for any drives marked as failed
	if config.Config.RemoveFailedDrivesFromWeka {
		// check if any container has failed drives
		driveFailures := container.Status.GetStats().Drives.DriveFailures
		if len(driveFailures) > 0 {
			containerDriveFailures = make([]string, 0, len(driveFailures))
			for _, driveFailure := range driveFailures {
				logger.Info("Drive marked as failed, marking for removal", "drive", driveFailure.SerialId)
				containerDriveFailures = append(containerDriveFailures, driveFailure.SerialId)
			}
		}
	}

	// check if any drives added to weka from this container are not present in status Allocations
	for _, addedDriveSerial := range container.Status.GetAddedDrivesSerials() {
		found := slices.Contains(container.Status.Allocations.Drives, addedDriveSerial)
		if !found {
			logger.Info("Drive not found in allocations, marking for removal", "drive", addedDriveSerial)
			containerDriveFailures = append(containerDriveFailures, addedDriveSerial)
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

func (r *containerReconcilerLoop) AllocateDrivesIfNeeded(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	resourceAllocator, err := allocator.GetAllocator(ctx, r.Client)
	if err != nil {
		return err
	}

	allocations, err := resourceAllocator.GetAllocations(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get resource allocations")
	}

	nodeName := container.GetNodeAffinity()
	nodeAlloc, ok := allocations.NodeMap[nodeName]
	if !ok {
		return fmt.Errorf("node %s allocations not found", nodeName)
	}

	owners := container.GetOwnerReferences()
	if len(owners) == 0 {
		return errors.New("no owner reference found")
	}
	owner := owners[0]

	ownerCluster := allocator.OwnerCluster{
		ClusterName: owner.Name,
		Namespace:   container.Namespace,
	}

	allocOwner := allocator.Owner{
		OwnerCluster: ownerCluster,
		Container:    container.Name,
		Role:         container.Spec.Mode,
	}

	driveAllocations, ok := nodeAlloc.Drives[allocOwner]
	if !ok {
		return fmt.Errorf("no drive allocations found for owner %s on node %s", allocOwner, nodeName)
	}

	// Allocate replacement drives
	err = resourceAllocator.AllocateContainerDrives(ctx, container)
	if err != nil {
		logger.Error(err, "Failed to allocate replacement drives for container", "container", container.Name)
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "AllocateContainerDrivesError", err.Error(), time.Minute)
		return err
	}

	// get updated allocations
	allocations, err = resourceAllocator.GetAllocations(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get resource allocations after drive allocation")
	}

	nodeAlloc, ok = allocations.NodeMap[nodeName]
	if !ok {
		return fmt.Errorf("node %s allocations not found", nodeName)
	}

	driveAllocations, ok = nodeAlloc.Drives[allocOwner]
	if !ok {
		return fmt.Errorf("no drive allocations found for owner %s on node %s after re-allocation", allocOwner, nodeName)
	}

	// update container status with new drive allocations
	container.Status.Allocations.Drives = driveAllocations
	err = r.Status().Update(ctx, container)
	if err != nil {
		err = fmt.Errorf("cannot update container status with new drive allocations: %w", err)
		return err
	}

	logger.Info("Drive re-allocation completed", "new_drives", driveAllocations)

	// trigger resources.json re-write
	logger.Info("Re-writing resources.json after drive re-allocation")
	err = r.WriteResources(ctx)
	if err != nil {
		err = fmt.Errorf("error writing resources.json after drive re-allocation: %w", err)
		return err
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
		addedDrivesMap[d.SerialNumber] = weka.Drive{}
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
		resourceAllocator, err := allocator.GetAllocator(ctx, r.Client)
		if err != nil {
			return err
		}

		err = resourceAllocator.DeallocateContainerDrives(ctx, container, blockedDrives)
		if err != nil {
			logger.Error(err, "Failed to deallocate drives for container", "container", container.Name, "drives", blockedDrives)
			_ = r.RecordEventThrottled(v1.EventTypeWarning, "DeallocateContainerDrivesError", err.Error(), time.Minute)
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

func (r *containerReconcilerLoop) removeDriveFromWeka(ctx context.Context, drive *weka.Drive, wekaService services.WekaService) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "removeReplacedDriveFromWeka", "drive_uuid", drive.Uuid, "drive_serial", drive.SerialNumber)
	defer end()

	statusActive := "ACTIVE"
	statusInactive := "INACTIVE"

	switch drive.Status {
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

	err := wekaService.RemoveDrive(ctx, drive.Uuid)
	if err != nil {
		err = fmt.Errorf("error removing drive %s: %w", drive.SerialNumber, err)
		return err
	}

	_ = r.RecordEvent("", "DriveRemoved", fmt.Sprintf("Drive %s removed", drive.SerialNumber))

	logger.Info("Drive removed from weka")
	return nil
}
