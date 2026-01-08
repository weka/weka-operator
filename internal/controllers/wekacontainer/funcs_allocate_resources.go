// This file contains functions for allocating resources (drives, ports) for WekaContainer
// using status-only allocation (no node annotations)
package wekacontainer

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// AllocateResources allocates drives and ports for the container using leader election
//
// Allocations are stored only in WekaContainer.Status (no node annotations).
// Uses per-node leader election to ensure only one container allocates at a time,
// preventing race conditions and resource conflicts.
//
// Flow:
//  1. Acquire per-node lease (blocks if another container is allocating)
//  2. Read existing allocations from all container Status on this node
//  3. Allocate available resources
//  4. Update this container's Status
//  5. Release lease (automatic)
func (r *containerReconcilerLoop) AllocateResources(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "AllocateResources")
	defer end()

	logger.Info("Allocating resources for container", "container", r.container.Name)

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return errors.New("container has no node affinity")
	}

	// Create leader election lock for this node
	// All containers on this node (any namespace) compete for the same lease
	restConfig := r.Manager.GetConfig()
	lock := allocator.NewNodeAllocationLock(
		restConfig,
		r.container.Namespace,
		string(nodeName),
		r.container.Name,
	)

	// Execute allocation while holding the lease
	// This ensures only one container allocates at a time on this node
	err := lock.RunWithLease(ctx, func(leaderCtx context.Context) error {
		return r.doAllocateResourcesWithLease(leaderCtx, nodeName)
	})

	return err
}

// doAllocateResourcesWithLease performs the actual allocation while holding the node lease
// This is called by RunWithLease after successfully acquiring the lease
func (r *containerReconcilerLoop) doAllocateResourcesWithLease(ctx context.Context, nodeName weka.NodeName) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "doAllocateResourcesWithLease")
	defer end()

	// Get the node
	node := &v1.Node{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node); err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	// Get cluster info for port range allocation
	cluster, err := r.getOwnerCluster(ctx)
	if err != nil {
		return fmt.Errorf("failed to get owner cluster: %w", err)
	}

	// Get failure domain
	failureDomain := r.getFailureDomain(ctx)

	// Validate drive sharing configuration before allocation
	if err := r.validateDriveSharingConfig(); err != nil {
		var configErr *allocator.InvalidDriveSharingConfigError
		if errors.As(err, &configErr) {
			logger.Error(err, "Invalid drive sharing configuration")
			_ = r.RecordEvent(v1.EventTypeWarning, "InvalidDriveSharingConfig", err.Error())
			return lifecycle.NewWaitErrorWithDuration(err, 30*time.Second)
		}
		return err
	}

	// Determine which port allocations are needed
	// Weka port (100 ports): only for host-network containers that are not Envoy
	allocateWekaPort := r.container.IsHostNetwork() && !r.container.IsEnvoy()
	// Agent port (1 port): only for containers that have an agent
	allocateAgentPort := r.container.HasAgent()

	// Use ContainerResourceAllocator service to allocate resources
	// NOTE: The allocator should read existing allocations from all container Status objects
	containerAllocator := allocator.NewContainerResourceAllocator(r.Client)
	allocationRequest := &allocator.AllocationRequest{
		Container:     r.container,
		Node:          node,
		Cluster:       cluster,
		NumDrives:     r.container.Spec.NumDrives,
		CapacityGiB:   r.container.Spec.ContainerCapacity,
		FailureDomain: failureDomain,
		AllocateWeka:  allocateWekaPort,
		AllocateAgent: allocateAgentPort,
	}

	result, err := containerAllocator.AllocateResources(ctx, allocationRequest)
	if err != nil {
		var insufficientDrivesErr *allocator.InsufficientDrivesError
		if errors.As(err, &insufficientDrivesErr) {
			logger.Error(err, "Insufficient drives on node, will retry")
			_ = r.RecordEvent(v1.EventTypeWarning, "InsufficientDrives", err.Error())
			// Use longer wait to avoid starving other containers waiting for the lease
			// Standard wait is ~5s, use 30s for resource exhaustion
			return lifecycle.NewWaitErrorWithDuration(err, 30*time.Second)
		}

		var insufficientCapacityErr *allocator.InsufficientDriveCapacityError
		if errors.As(err, &insufficientCapacityErr) {
			logger.Error(err, "Insufficient drive capacity on node, will retry")
			_ = r.RecordEvent(v1.EventTypeWarning, "InsufficientDriveCapacity", err.Error())
			// Longer wait for resource exhaustion
			return lifecycle.NewWaitErrorWithDuration(err, 30*time.Second)
		}

		var portAllocationErr *allocator.PortAllocationError
		if errors.As(err, &portAllocationErr) {
			logger.Error(err, "Failed to allocate port ranges, will retry with backoff")
			_ = r.RecordEvent(v1.EventTypeWarning, "PortAllocationFailed", err.Error())
			// Longer wait for port exhaustion as well
			return lifecycle.NewWaitErrorWithDuration(err, 30*time.Second)
		}

		return fmt.Errorf("failed to allocate resources: %w", err)
	}

	allocatedDrives := result.Drives
	allocatedVirtualDrives := result.VirtualDrives
	wekaPort := result.WekaPort
	agentPort := result.AgentPort

	// Update container status with allocations (single source of truth)
	allocations := &weka.ContainerAllocations{
		Drives:        allocatedDrives,
		VirtualDrives: allocatedVirtualDrives,
		WekaPort:      wekaPort,
		AgentPort:     agentPort,
		FailureDomain: failureDomain,
	}

	// Log appropriate message based on drive type
	if r.container.UsesDriveSharing() {
		logger.Info("Successfully allocated resources",
			"virtualDrives", len(allocatedVirtualDrives),
			"wekaPort", wekaPort,
			"agentPort", agentPort)
	} else {
		logger.Info("Successfully allocated resources",
			"drives", len(allocatedDrives),
			"wekaPort", wekaPort,
			"agentPort", agentPort)
	}

	r.container.Status.Allocations = allocations
	err = r.Status().Update(ctx, r.container)
	if err != nil {
		return fmt.Errorf("failed to update container status with allocations: %w", err)
	}

	// Build resource allocation message
	var allocMsg string
	driveCount := len(allocatedDrives)
	if r.container.UsesDriveSharing() {
		driveCount = len(allocatedVirtualDrives)
	}

	if wekaPort > 0 && agentPort > 0 {
		allocMsg = fmt.Sprintf("Allocated %d drives, weka ports %d-%d, agent port %d", driveCount, wekaPort, wekaPort+allocator.WekaPortRangeSize-1, agentPort)
	} else if wekaPort > 0 {
		allocMsg = fmt.Sprintf("Allocated %d drives, weka ports %d-%d", driveCount, wekaPort, wekaPort+allocator.WekaPortRangeSize-1)
	} else if agentPort > 0 {
		allocMsg = fmt.Sprintf("Allocated %d drives, agent port %d", driveCount, agentPort)
	} else {
		allocMsg = fmt.Sprintf("Allocated %d drives", driveCount)
	}
	r.RecordEvent(v1.EventTypeNormal, "ResourcesAllocated", allocMsg)

	return nil
}

// getOwnerCluster returns the WekaCluster that owns this container
func (r *containerReconcilerLoop) getOwnerCluster(ctx context.Context) (*weka.WekaCluster, error) {
	owners := r.container.GetOwnerReferences()
	if len(owners) == 0 {
		return nil, errors.New("container has no owner references")
	}

	clusterName := owners[0].Name
	cluster := &weka.WekaCluster{}
	err := r.Client.Get(ctx, client.ObjectKey{
		Name:      clusterName,
		Namespace: r.container.Namespace,
	}, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get owner cluster: %w", err)
	}

	return cluster, nil
}

// validateDriveSharingConfig validates the drive sharing configuration
// Returns an error if the configuration is invalid
func (r *containerReconcilerLoop) validateDriveSharingConfig() error {
	spec := r.container.Spec

	// Check mutual exclusivity: numDrives and containerCapacity
	if spec.NumDrives > 0 && spec.ContainerCapacity > 0 {
		return &allocator.InvalidDriveSharingConfigError{
			Message: "numDrives and containerCapacity are mutually exclusive; use numDrives with driveCapacity for TLC-only mode, or containerCapacity with driveTypesRatio for mixed drive types",
		}
	}

	// Check numDrives >= numCores when using driveCapacity (TLC-only mode)
	if spec.DriveCapacity > 0 && spec.NumDrives > 0 && spec.NumDrives < spec.NumCores {
		return &allocator.InvalidDriveSharingConfigError{
			Message: fmt.Sprintf("numDrives (%d) must be >= numCores (%d) when using driveCapacity (TLC-only mode); each core requires at least one virtual drive", spec.NumDrives, spec.NumCores),
		}
	}

	return nil
}
