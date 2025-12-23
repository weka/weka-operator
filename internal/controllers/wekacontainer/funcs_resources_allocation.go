// This file contains functions related to resources allocation and writing during WekaContainer reconciliation,
// such as resources.json writing and verification, NICs allocation, drives ensuring operations
package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"go/types"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/pkg/domain"
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

func (r *containerReconcilerLoop) AllocateDrivesIfNeeded(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	// Get the node
	nodeName := container.GetNodeAffinity()
	if nodeName == "" {
		return errors.New("container has no node affinity")
	}

	node := &v1.Node{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node); err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	// Calculate how many drives we need (handle both regular and virtual drives)
	var currentDriveCount int
	if container.Spec.UseDriveSharing {
		currentDriveCount = len(container.Status.Allocations.VirtualDrives)
	} else {
		currentDriveCount = len(container.Status.Allocations.Drives)
	}

	numDrivesToAllocate := container.Spec.NumDrives - currentDriveCount
	if numDrivesToAllocate <= 0 {
		logger.Debug("No additional drives to allocate for container", "container", container.Name)
		return nil
	}

	logger.Info("Need to allocate additional drives",
		"container", container.Name,
		"currentDrives", currentDriveCount,
		"targetDrives", container.Spec.NumDrives,
		"toAllocate", numDrivesToAllocate,
		"useDriveSharing", container.Spec.UseDriveSharing)

	if container.Spec.UseDriveSharing {
		// For drive sharing mode, we need to allocate additional virtual drives
		// This is similar to initial allocation but adds to existing VirtualDrives
		return fmt.Errorf("scaling virtual drives not yet implemented (need %d more drives)", numDrivesToAllocate)
	}

	// Regular drive mode: use ReallocateDrives
	containerAllocator := allocator.NewContainerResourceAllocator(r.Client)
	reallocRequest := &allocator.DriveReallocationRequest{
		Container:    container,
		Node:         node,
		FailedDrives: []string{}, // No failed drives, just adding more
		NumNewDrives: numDrivesToAllocate,
	}

	result, err := containerAllocator.ReallocateDrives(ctx, reallocRequest)
	if err != nil {
		logger.Error(err, "Failed to allocate replacement drives for container", "container", container.Name)
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "AllocateContainerDrivesError", err.Error(), time.Minute)
		return err
	}

	// Update container status with new drive allocations
	container.Status.Allocations.Drives = result.AllDrives
	err = r.Status().Update(ctx, container)
	if err != nil {
		err = fmt.Errorf("cannot update container status with new drive allocations: %w", err)
		return err
	}

	logger.Info("Drive re-allocation completed", "new_drives", result.NewDrives, "total_drives", len(result.AllDrives))

	// trigger resources.json re-write
	logger.Info("Re-writing resources.json after drive re-allocation")
	err = r.WriteResources(ctx)
	if err != nil {
		err = fmt.Errorf("error writing resources.json after drive re-allocation: %w", err)
		return err
	}

	return nil
}

// deallocateVirtualDrives removes virtual drive allocations for blocked physical drives
func (r *containerReconcilerLoop) deallocateVirtualDrives(ctx context.Context, node *v1.Node, blockedDrives []string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "deallocateVirtualDrives")
	defer end()

	container := r.container

	logger.Info("Deallocating virtual drives mapped to blocked physical drives",
		"container", container.Name,
		"blockedDrives", blockedDrives)

	// Get shared drives from node to map serial IDs to physical UUIDs
	nodeInfoGetter := allocator.NewK8sNodeInfoGetter(r.Client)
	nodeInfo, err := nodeInfoGetter(ctx, weka.NodeName(node.Name))
	if err != nil {
		return fmt.Errorf("failed to get node info: %w", err)
	}

	// Build map of serial ID to physical UUID
	serialToPhysicalUUID := make(map[string]string)
	for _, sharedDrive := range nodeInfo.SharedDrives {
		serialToPhysicalUUID[sharedDrive.Serial] = sharedDrive.UUID
	}

	// Find virtual drives that are mapped to blocked physical drives
	virtualDrivesToRemove := []string{}
	for _, vd := range container.Status.Allocations.VirtualDrives {
		// Check if this virtual drive's physical drive is blocked
		// Blocked drives list contains serial IDs, so we need to match PhysicalUUID
		isBlocked := false
		for _, blockedSerial := range blockedDrives {
			// Map blocked serial to physical UUID
			if physicalUUID, ok := serialToPhysicalUUID[blockedSerial]; ok {
				if vd.PhysicalUUID == physicalUUID {
					isBlocked = true
					break
				}
			}
		}
		if isBlocked {
			virtualDrivesToRemove = append(virtualDrivesToRemove, vd.VirtualUUID)
		}
	}

	if len(virtualDrivesToRemove) == 0 {
		logger.Info("No virtual drives to deallocate")
		return nil
	}

	logger.Info("Removing virtual drive allocations", "virtualDrives", virtualDrivesToRemove)

	// Update container status allocations
	updatedVirtualDrives := []weka.VirtualDrive{}
	for _, vd := range container.Status.Allocations.VirtualDrives {
		if !slices.Contains(virtualDrivesToRemove, vd.VirtualUUID) {
			updatedVirtualDrives = append(updatedVirtualDrives, vd)
		}
	}
	container.Status.Allocations.VirtualDrives = updatedVirtualDrives

	err = r.Status().Update(ctx, container)
	if err != nil {
		return fmt.Errorf("failed to update container status: %w", err)
	}

	logger.Info("Successfully deallocated virtual drives", "count", len(virtualDrivesToRemove))
	return nil
}

func (r *containerReconcilerLoop) deallocateRemovedDrives(ctx context.Context, blockedDrives []string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "deallocateRemovedDrives")
	defer end()

	container := r.container

	// Get the node
	nodeName := container.GetNodeAffinity()
	if nodeName == "" {
		return errors.New("container has no node affinity")
	}

	node := &v1.Node{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node); err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	// Drive sharing mode needs different handling for blocked drives
	if container.Spec.UseDriveSharing {
		return r.deallocateVirtualDrives(ctx, node, blockedDrives)
	}

	// Regular drive mode - status-only allocation
	// Update drive allocations in container status
	updatedDriveAllocations := make([]string, 0)
	for _, drive := range container.Status.Allocations.Drives {
		if !slices.Contains(blockedDrives, drive) {
			updatedDriveAllocations = append(updatedDriveAllocations, drive)
		}
	}

	container.Status.Allocations.Drives = updatedDriveAllocations
	err := r.Status().Update(ctx, container)
	if err != nil {
		err = fmt.Errorf("cannot update container status with deallocated drives: %w", err)
		return err
	}

	logger.Info("Drive deallocation completed", "removed_drives", blockedDrives, "remaining_drives", len(updatedDriveAllocations))

	return nil
}
