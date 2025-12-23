// This file contains functions for allocating resources (drives, ports) for WekaContainer
// using the hybrid approach with node annotations
package wekacontainer

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// AllocateResources allocates drives and ports for the container using the hybrid approach
// This replaces the WekaCluster-level allocation with per-container allocation
func (r *containerReconcilerLoop) AllocateResources(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "AllocateResources")
	defer end()

	logger.Info("Allocating resources for container", "container", r.container.Name)

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return errors.New("container has no node affinity")
	}

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

	// Validate and rebuild node claims if needed (self-healing)
	rebuilt, err := allocator.ValidateAndRebuildNodeClaims(ctx, r.Client, node, r.container.Namespace)
	if err != nil {
		return fmt.Errorf("failed to validate node claims: %w", err)
	}
	if rebuilt {
		logger.Info("Rebuilt node claims before allocation")
		// Refresh node to get latest ResourceVersion
		if err := r.Client.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node); err != nil {
			return fmt.Errorf("failed to refresh node after rebuild: %w", err)
		}
	}

	// Parse current claims from node
	claims, err := allocator.ParseNodeClaims(node)
	if err != nil {
		return fmt.Errorf("failed to parse node claims: %w", err)
	}

	// Get failure domain
	failureDomain := r.getFailureDomain(ctx)

	// Determine which port allocations are needed (preserving old logic)
	// Weka port (100 ports): only for host-network containers that are not Envoy
	allocateWekaPort := r.container.IsHostNetwork() && !r.container.IsEnvoy()
	// Agent port (1 port): only for containers that have an agent
	allocateAgentPort := r.container.HasAgent()

	// Use ContainerResourceAllocator service to allocate resources
	containerAllocator := allocator.NewContainerResourceAllocator(r.Client)
	allocationRequest := &allocator.AllocationRequest{
		Container:     r.container,
		Node:          node,
		Cluster:       cluster,
		NodeClaims:    claims,
		NumDrives:     r.container.Spec.NumDrives,
		FailureDomain: failureDomain,
		AllocateWeka:  allocateWekaPort,
		AllocateAgent: allocateAgentPort,
	}

	result, err := containerAllocator.AllocateResources(ctx, allocationRequest)
	if err != nil {
		var insufficientDrivesErr *allocator.InsufficientDrivesError
		if errors.As(err, &insufficientDrivesErr) {
			logger.Error(err, "Insufficient drives")
			_ = r.RecordEvent(v1.EventTypeWarning, "InsufficientDrives", err.Error())
			return lifecycle.NewWaitError(err)
		}

		var portAllocationErr *allocator.PortAllocationError
		if errors.As(err, &portAllocationErr) {
			logger.Error(err, "Failed to allocate port ranges")
			_ = r.RecordEvent(v1.EventTypeWarning, "PortAllocationFailed", err.Error())
		}

		return fmt.Errorf("failed to allocate resources: %w", err)
	}

	allocatedDrives := result.Drives
	wekaPort := result.WekaPort
	agentPort := result.AgentPort

	// Get claim key for this container
	claimKey := allocator.ClaimKeyFromContainer(r.container)

	// Add claims to node annotation (atomic operation)
	for _, drive := range allocatedDrives {
		if err := claims.AddDriveClaim(drive, claimKey); err != nil {
			return fmt.Errorf("failed to add drive claim: %w", err)
		}
	}

	// Add port range claims (only if allocated)
	if wekaPort > 0 {
		wekaPortRange := fmt.Sprintf("%d,%d", wekaPort, allocator.WekaPortRangeSize)
		if err := claims.AddPortClaim(wekaPortRange, claimKey); err != nil {
			return fmt.Errorf("failed to add weka port claim: %w", err)
		}
	}

	if agentPort > 0 {
		agentPortRange := fmt.Sprintf("%d,1", agentPort)
		if err := claims.AddPortClaim(agentPortRange, claimKey); err != nil {
			return fmt.Errorf("failed to add agent port claim: %w", err)
		}
	}

	// Save claims to node annotation (with optimistic locking)
	err = claims.SaveToNode(ctx, r.Client, node)
	if err != nil {
		if apierrors.IsConflict(err) {
			// Another container allocated on this node simultaneously, retry
			logger.Info("Node update conflict during allocation, will retry")
			_ = r.RecordEvent(v1.EventTypeWarning, "AllocationConflict", "Node updated by another container, retrying allocation")
			return lifecycle.NewWaitError(fmt.Errorf("allocation conflict, retrying: %w", err))
		}
		logger.Error(err, "Failed to save claims to node")
		return fmt.Errorf("failed to save claims to node: %w", err)
	}

	// Update container status with allocations
	allocations := &weka.ContainerAllocations{
		Drives:        allocatedDrives,
		WekaPort:      wekaPort,
		AgentPort:     agentPort,
		FailureDomain: failureDomain,
	}

	logger.Info("Successfully allocated resources",
		"drives", len(allocatedDrives),
		"wekaPort", wekaPort,
		"agentPort", agentPort)

	r.container.Status.Allocations = allocations
	err = r.Status().Update(ctx, r.container)
	if err != nil {
		// If we fail to update status, we've already claimed resources on the node
		// The self-healing logic will clean this up if the container is deleted
		return fmt.Errorf("failed to update container status with allocations: %w", err)
	}

	// Build resource allocation message
	var allocMsg string
	if wekaPort > 0 && agentPort > 0 {
		allocMsg = fmt.Sprintf("Allocated %d drives, weka ports %d-%d, agent port %d", len(allocatedDrives), wekaPort, wekaPort+allocator.WekaPortRangeSize-1, agentPort)
	} else if wekaPort > 0 {
		allocMsg = fmt.Sprintf("Allocated %d drives, weka ports %d-%d", len(allocatedDrives), wekaPort, wekaPort+allocator.WekaPortRangeSize-1)
	} else if agentPort > 0 {
		allocMsg = fmt.Sprintf("Allocated %d drives, agent port %d", len(allocatedDrives), agentPort)
	} else {
		allocMsg = fmt.Sprintf("Allocated %d drives", len(allocatedDrives))
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
