// This file contains the ContainerResourceAllocator service for per-container resource allocation
// using the hybrid approach with node annotations
package allocator

import (
	"context"
	"fmt"
	"slices"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ContainerResourceAllocator handles per-container resource allocation
type ContainerResourceAllocator struct {
	client client.Client
}

// NewContainerResourceAllocator creates a new container resource allocator
func NewContainerResourceAllocator(client client.Client) *ContainerResourceAllocator {
	return &ContainerResourceAllocator{
		client: client,
	}
}

// AllocationRequest represents a request to allocate resources for a container
type AllocationRequest struct {
	Container     *weka.WekaContainer
	Node          *v1.Node
	Cluster       *weka.WekaCluster
	NodeClaims    *NodeClaims
	NumDrives     int
	FailureDomain *string
	AllocateWeka  bool // Whether to allocate weka port (100 ports)
	AllocateAgent bool // Whether to allocate agent port (1 port)
}

// AllocationResult represents the result of a resource allocation
type AllocationResult struct {
	Drives    []string
	WekaPort  int
	AgentPort int
}

// AllocateResources allocates drives and ports for a container
func (a *ContainerResourceAllocator) AllocateResources(ctx context.Context, req *AllocationRequest) (*AllocationResult, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ContainerResourceAllocator.AllocateResources")
	defer end()

	result := &AllocationResult{}

	// Allocate drives if needed
	if req.NumDrives > 0 {
		drives, err := a.GetAvailableDrives(ctx, req.Node, req.NodeClaims)
		if err != nil {
			return nil, fmt.Errorf("failed to get available drives: %w", err)
		}

		if len(drives) < req.NumDrives {
			return nil, &InsufficientDrivesError{Needed: req.NumDrives, Available: len(drives)}
		}

		result.Drives = drives[:req.NumDrives]
		logger.Debug("Allocated drives", "count", len(result.Drives))
	}

	// Allocate port ranges based on request flags
	wekaPort, agentPort, err := a.AllocatePortRanges(ctx, req.Cluster, req.NodeClaims, req.AllocateWeka, req.AllocateAgent)
	if err != nil {
		return nil, &PortAllocationError{Cause: err}
	}

	result.WekaPort = wekaPort
	result.AgentPort = agentPort

	logger.Info("Allocated resources",
		"drives", len(result.Drives),
		"wekaPort", result.WekaPort,
		"agentPort", result.AgentPort)

	return result, nil
}

// GetAvailableDrives returns drives that are available for allocation on a node
func (a *ContainerResourceAllocator) GetAvailableDrives(ctx context.Context, node *v1.Node, claims *NodeClaims) ([]string, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetAvailableDrives")
	defer end()

	// Get all drives from node annotation
	nodeInfoGetter := NewK8sNodeInfoGetter(a.client)
	nodeInfo, err := nodeInfoGetter(ctx, weka.NodeName(node.Name))
	if err != nil {
		return nil, fmt.Errorf("failed to get node info: %w", err)
	}

	allDrives := nodeInfo.AvailableDrives
	logger.Debug("Found drives on node", "total", len(allDrives))

	// Filter out claimed drives
	availableDrives := []string{}
	for _, drive := range allDrives {
		if _, claimed := claims.Drives[drive]; !claimed {
			availableDrives = append(availableDrives, drive)
		}
	}

	logger.Debug("Available drives after filtering claims", "available", len(availableDrives))
	return availableDrives, nil
}

// AllocatePortRanges allocates weka and agent port ranges from the cluster's port range
// allocateWeka: if true, allocate weka port (100 ports)
// allocateAgent: if true, allocate agent port (1 port)
func (a *ContainerResourceAllocator) AllocatePortRanges(ctx context.Context, cluster *weka.WekaCluster, claims *NodeClaims, allocateWeka bool, allocateAgent bool) (wekaPort int, agentPort int, err error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "AllocatePortRanges")
	defer end()

	// Get the cluster's allocated port range from global allocator
	resourceAllocator, err := GetAllocator(ctx, a.client)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get allocator: %w", err)
	}

	allocations, err := resourceAllocator.GetAllocations(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get allocations: %w", err)
	}

	ownerCluster := OwnerCluster{
		ClusterName: cluster.Name,
		Namespace:   cluster.Namespace,
	}

	clusterRange, ok := allocations.Global.ClusterRanges[ownerCluster]
	if !ok {
		return 0, 0, fmt.Errorf("no port range allocated for cluster %s", cluster.Name)
	}

	logger.Debug("Cluster port range", "base", clusterRange.Base, "size", clusterRange.Size)

	// Build list of allocated ranges combining node-level claims and global singleton ports
	// This prevents container ports from conflicting with global LB/S3/LbAdmin ports
	allocatedRanges := []Range{}

	// Add node-level port claims
	for portRangeStr := range claims.Ports {
		var base, size int
		fmt.Sscanf(portRangeStr, "%d,%d", &base, &size)
		allocatedRanges = append(allocatedRanges, Range{Base: base, Size: size})
	}

	// Add global singleton ports (LB, S3, LbAdmin) to exclusion list
	if globalRanges, ok := allocations.Global.AllocatedRanges[ownerCluster]; ok {
		for rangeName, rangeData := range globalRanges {
			allocatedRanges = append(allocatedRanges, rangeData)
			logger.Debug("Excluding global port range", "name", rangeName, "base", rangeData.Base, "size", rangeData.Size)
		}
	}

	// Allocate weka port if requested (100 ports from cluster base)
	wekaPortRange := 0
	if allocateWeka {
		wekaPortRange, err = GetFreeRange(clusterRange, allocatedRanges, 100)
		if err != nil {
			return 0, 0, fmt.Errorf("failed to find free weka port range: %w", err)
		}
		logger.Debug("Allocated weka port", "wekaPort", wekaPortRange)
	}

	// Allocate agent port if requested (1 port from offset range)
	agentPortRange := 0
	if allocateAgent {
		agentPortRange, err = GetFreeRangeWithOffset(clusterRange, allocatedRanges, 1, SinglePortsOffset)
		if err != nil {
			return 0, 0, fmt.Errorf("failed to find free agent port: %w", err)
		}
		logger.Debug("Allocated agent port", "agentPort", agentPortRange)
	}

	logger.Info("Allocated port ranges",
		"wekaPort", wekaPortRange,
		"agentPort", agentPortRange,
		"allocatedWeka", allocateWeka,
		"allocatedAgent", allocateAgent)

	return wekaPortRange, agentPortRange, nil
}

// FindFreePortRange and rangesOverlap were removed.
// Per-container port allocation now uses GetFreeRange() and GetFreeRangeWithOffset()
// from ranges.go, which provides consistent range allocation logic for both
// global and per-container allocations.

// DriveReallocationRequest represents a request to reallocate drives (hot-swap)
type DriveReallocationRequest struct {
	Container    *weka.WekaContainer
	Node         *v1.Node
	FailedDrives []string // Drives to remove
	NumNewDrives int      // Number of replacement drives needed
}

// DriveReallocationResult represents the result of drive reallocation
type DriveReallocationResult struct {
	NewDrives []string // New drives allocated
	AllDrives []string // All drives after reallocation (old + new - failed)
}

// ReallocateDrives replaces failed drives with new ones (hot-swap scenario)
// This is used when drives fail and need to be replaced
func (a *ContainerResourceAllocator) ReallocateDrives(ctx context.Context, req *DriveReallocationRequest) (*DriveReallocationResult, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ContainerResourceAllocator.ReallocateDrives")
	defer end()

	logger.Info("Reallocating drives for container",
		"container", req.Container.Name,
		"failedDrives", req.FailedDrives,
		"numNewDrives", req.NumNewDrives)

	// Get node claims
	claims, err := ParseNodeClaims(req.Node)
	if err != nil {
		return nil, fmt.Errorf("failed to parse node claims: %w", err)
	}

	// Get claim key for this container
	claimKey := ClaimKeyFromContainer(req.Container)

	// Remove failed drives from claims
	if len(req.FailedDrives) > 0 {
		claims.RemoveDriveClaims(claimKey, req.FailedDrives)
		logger.Debug("Removed failed drives from claims", "drives", req.FailedDrives)
	}

	// Get available drives
	availableDrives, err := a.GetAvailableDrives(ctx, req.Node, claims)
	if err != nil {
		return nil, fmt.Errorf("failed to get available drives: %w", err)
	}

	if len(availableDrives) < req.NumNewDrives {
		return nil, &InsufficientDrivesError{Needed: req.NumNewDrives, Available: len(availableDrives)}
	}

	// Allocate new drives
	newDrives := availableDrives[:req.NumNewDrives]
	for _, drive := range newDrives {
		if err := claims.AddDriveClaim(drive, claimKey); err != nil {
			return nil, fmt.Errorf("failed to add drive claim: %w", err)
		}
	}

	// Save claims to node annotation
	if err := claims.SaveToNode(ctx, a.client, req.Node); err != nil {
		return nil, fmt.Errorf("failed to save claims to node: %w", err)
	}

	// Calculate all drives (existing - failed + new)
	currentDrives := req.Container.Status.Allocations.Drives
	allDrives := make([]string, 0)

	// Add drives that weren't failed
	for _, drive := range currentDrives {
		if !slices.Contains(req.FailedDrives, drive) {
			allDrives = append(allDrives, drive)
		}
	}

	// Add new drives
	allDrives = append(allDrives, newDrives...)

	logger.Info("Successfully reallocated drives",
		"newDrives", newDrives,
		"totalDrives", len(allDrives))

	return &DriveReallocationResult{
		NewDrives: newDrives,
		AllDrives: allDrives,
	}, nil
}

// DriveDeallocRequest represents a request to deallocate specific drives
type DriveDeallocRequest struct {
	Container      *weka.WekaContainer
	Node           *v1.Node
	DrivesToRemove []string // Drives to deallocate by serial ID
}

// DeallocateDrives removes specific drives from allocation
// This is used when drives are removed or blocked
func (a *ContainerResourceAllocator) DeallocateDrives(ctx context.Context, req *DriveDeallocRequest) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ContainerResourceAllocator.DeallocateDrives")
	defer end()

	logger.Info("Deallocating drives for container",
		"container", req.Container.Name,
		"drives", req.DrivesToRemove)

	// Get node claims
	claims, err := ParseNodeClaims(req.Node)
	if err != nil {
		return fmt.Errorf("failed to parse node claims: %w", err)
	}

	// Get claim key for this container
	claimKey := ClaimKeyFromContainer(req.Container)

	// Remove drives from claims
	claims.RemoveDriveClaims(claimKey, req.DrivesToRemove)
	logger.Debug("Removed drives from claims", "drives", req.DrivesToRemove)

	// Save claims to node annotation
	if err := claims.SaveToNode(ctx, a.client, req.Node); err != nil {
		return fmt.Errorf("failed to save claims to node: %w", err)
	}

	logger.Info("Successfully deallocated drives", "count", len(req.DrivesToRemove))

	return nil
}
