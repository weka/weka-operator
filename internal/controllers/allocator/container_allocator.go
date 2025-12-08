// This file contains the ContainerResourceAllocator service for per-container resource allocation
// using the hybrid approach with node annotations
package allocator

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/services/kubernetes"
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
	NumDrives     int
	FailureDomain *string
	AllocateWeka  bool // Whether to allocate weka port (100 ports)
	AllocateAgent bool // Whether to allocate agent port (1 port)
}

// AllocationResult represents the result of a resource allocation
type AllocationResult struct {
	Drives        []string            // Regular drives (serial IDs) for non-sharing mode
	VirtualDrives []weka.VirtualDrive // Virtual drives for drive sharing mode
	WekaPort      int
	AgentPort     int
}

// AllocateResources allocates drives and ports for a container using status-only allocation
// Reads existing allocations from all WekaContainer Status objects on the node
func (a *ContainerResourceAllocator) AllocateResources(ctx context.Context, req *AllocationRequest) (*AllocationResult, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ContainerResourceAllocator.AllocateResources")
	defer end()

	// Aggregate existing allocations from all containers on this node
	allocatedDrives, allocatedPorts, err := a.aggregateNodeAllocations(ctx, req.Node.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate node allocations: %w", err)
	}

	result := &AllocationResult{}

	// Allocate drives if needed
	if req.NumDrives > 0 {
		// Check if drive sharing is enabled
		if req.Container.Spec.UseDriveSharing {
			// Allocate shared drives (virtual drives with capacity)
			virtualDrives, err := a.AllocateSharedDrives(ctx, req)
			if err != nil {
				return nil, err
			}
			result.VirtualDrives = virtualDrives
			logger.Debug("Allocated virtual drives", "count", len(result.VirtualDrives))
		} else {
			// Regular drive allocation (exclusive drives)
			drives, err := a.getAvailableDrivesFromStatus(ctx, req.Node, allocatedDrives)
			if err != nil {
				return nil, fmt.Errorf("failed to get available drives: %w", err)
			}

			if len(drives) < req.NumDrives {
				return nil, &InsufficientDrivesError{Needed: req.NumDrives, Available: len(drives)}
			}

			result.Drives = drives[:req.NumDrives]
			logger.Debug("Allocated drives", "count", len(result.Drives))
		}
	}

	// Allocate port ranges based on request flags
	wekaPort, agentPort, err := a.allocatePortRangesFromStatus(ctx, req.Cluster, allocatedPorts, req.AllocateWeka, req.AllocateAgent)
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

// aggregateNodeAllocations reads all WekaContainer Status objects on a node
// and returns maps of allocated drives and ports across all namespaces
// (drives and ports are node-level resources, not namespace-level)
func (a *ContainerResourceAllocator) aggregateNodeAllocations(ctx context.Context, nodeName string) (map[string]bool, map[int]bool, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "aggregateNodeAllocations")
	defer end()

	// List all containers on this node (across all namespaces)
	kubeService := kubernetes.NewKubeService(a.client)
	containers, err := kubeService.GetWekaContainersSimple(ctx, "", nodeName, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list containers on node %s: %w", nodeName, err)
	}

	allocatedDrives := make(map[string]bool)
	allocatedPorts := make(map[int]bool)

	// Aggregate allocations from all containers
	for _, container := range containers {
		if container.Status.Allocations == nil {
			continue
		}

		// Aggregate drive allocations
		for _, drive := range container.Status.Allocations.Drives {
			allocatedDrives[drive] = true
		}

		// Aggregate virtual drive allocations (physical drives are marked as used)
		for _, vd := range container.Status.Allocations.VirtualDrives {
			allocatedDrives[vd.PhysicalUUID] = true
		}

		// Aggregate port allocations
		if container.Status.Allocations.WekaPort > 0 {
			// WekaPort uses 100 consecutive ports
			for i := 0; i < WekaPortRangeSize; i++ {
				port := container.Status.Allocations.WekaPort + i
				allocatedPorts[port] = true
			}
		}

		if container.Status.Allocations.AgentPort > 0 {
			allocatedPorts[container.Status.Allocations.AgentPort] = true
		}
	}

	logger.Info("Aggregated allocations from container status",
		"node", nodeName,
		"containers", len(containers),
		"allocatedDrives", len(allocatedDrives),
		"allocatedPorts", len(allocatedPorts))

	return allocatedDrives, allocatedPorts, nil
}

// getAvailableDrivesFromStatus returns drives that are available for allocation on a node
// based on aggregated allocations from container Status
func (a *ContainerResourceAllocator) getAvailableDrivesFromStatus(ctx context.Context, node *v1.Node, allocatedDrives map[string]bool) ([]string, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getAvailableDrivesFromStatus")
	defer end()

	// Get all drives from node annotation
	nodeInfoGetter := NewK8sNodeInfoGetter(a.client)
	nodeInfo, err := nodeInfoGetter(ctx, weka.NodeName(node.Name))
	if err != nil {
		return nil, fmt.Errorf("failed to get node info: %w", err)
	}

	allDrives := nodeInfo.AvailableDrives
	logger.Debug("Found drives on node", "total", len(allDrives))

	// Filter out allocated drives
	availableDrives := []string{}
	for _, drive := range allDrives {
		if !allocatedDrives[drive] {
			availableDrives = append(availableDrives, drive)
		}
	}

	logger.Debug("Available drives after filtering allocations", "available", len(availableDrives))
	return availableDrives, nil
}

// allocatePortRangesFromStatus allocates weka and agent port ranges from the cluster's port range
// using aggregated allocations from container Status
// allocateWeka: if true, allocate weka port (100 ports)
// allocateAgent: if true, allocate agent port (1 port)
func (a *ContainerResourceAllocator) allocatePortRangesFromStatus(ctx context.Context, cluster *weka.WekaCluster, allocatedPorts map[int]bool, allocateWeka bool, allocateAgent bool) (wekaPort int, agentPort int, err error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "allocatePortRangesFromStatus")
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

	// Build list of allocated ranges from allocatedPorts map and global singleton ports
	// This prevents container ports from conflicting with global LB/S3/LbAdmin ports
	allocatedRanges := []Range{}

	// Convert allocated ports map to ranges (group consecutive ports)
	portList := make([]int, 0, len(allocatedPorts))
	for port := range allocatedPorts {
		portList = append(portList, port)
	}
	slices.Sort(portList)

	// Group consecutive ports into ranges
	for i := 0; i < len(portList); {
		start := portList[i]
		end := start
		// Find consecutive sequence
		for i+1 < len(portList) && portList[i+1] == portList[i]+1 {
			i++
			end = portList[i]
		}
		allocatedRanges = append(allocatedRanges, Range{Base: start, Size: end - start + 1})
		i++
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

	// Aggregate existing allocations from all containers on this node
	allocatedDrives, _, err := a.aggregateNodeAllocations(ctx, req.Node.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate node allocations: %w", err)
	}

	// Remove this container's current drives from allocated set (they will be freed)
	for _, drive := range req.Container.Status.Allocations.Drives {
		delete(allocatedDrives, drive)
	}

	// Add back the drives that are NOT being replaced (keep these allocated)
	for _, drive := range req.Container.Status.Allocations.Drives {
		if !slices.Contains(req.FailedDrives, drive) {
			allocatedDrives[drive] = true
		}
	}

	logger.Debug("Prepared allocation state for reallocation",
		"totalAllocated", len(allocatedDrives),
		"failedToRemove", len(req.FailedDrives))

	// Get available drives (excluding allocated ones)
	availableDrives, err := a.getAvailableDrivesFromStatus(ctx, req.Node, allocatedDrives)
	if err != nil {
		return nil, fmt.Errorf("failed to get available drives: %w", err)
	}

	if len(availableDrives) < req.NumNewDrives {
		return nil, &InsufficientDrivesError{Needed: req.NumNewDrives, Available: len(availableDrives)}
	}

	// Allocate new drives
	newDrives := availableDrives[:req.NumNewDrives]

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

// AllocateSharedDrives allocates virtual drives from shared physical drives
// Each virtual drive gets a random UUID and is mapped to a physical drive
func (a *ContainerResourceAllocator) AllocateSharedDrives(ctx context.Context, req *AllocationRequest) ([]weka.VirtualDrive, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "AllocateSharedDrives")
	defer end()

	// Get node info to access shared drives
	nodeInfoGetter := NewK8sNodeInfoGetter(a.client)
	nodeInfo, err := nodeInfoGetter(ctx, weka.NodeName(req.Node.Name))
	if err != nil {
		return nil, fmt.Errorf("failed to get node info: %w", err)
	}

	sharedDrives := nodeInfo.SharedDrives
	logger.Debug("Found shared drives on node", "total", len(sharedDrives))

	if len(sharedDrives) < req.NumDrives {
		return nil, &InsufficientDrivesError{Needed: req.NumDrives, Available: len(sharedDrives)}
	}

	// Calculate capacity needed per virtual drive
	driveCapacityGiB := req.Container.Spec.DriveCapacity
	if driveCapacityGiB == 0 {
		return nil, fmt.Errorf("container has UseDriveSharing=true but DriveCapacity is not set")
	}
	totalCapacityNeeded := req.NumDrives * driveCapacityGiB

	// Aggregate claimed capacity from all container Status on this node (across all namespaces)
	// Virtual drives are node-level resources, not namespace-level
	kubeService := kubernetes.NewKubeService(a.client)
	containers, err := kubeService.GetWekaContainersSimple(ctx, "", req.Node.Name, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers on node: %w", err)
	}

	// Build per-physical-drive capacity tracking
	type physicalDriveCapacity struct {
		drive         SharedDriveInfo
		totalCapacity int
		claimedCapacity int
		availableCapacity int
	}

	driveCapacities := make(map[string]*physicalDriveCapacity)
	for _, drive := range sharedDrives {
		driveCapacities[drive.UUID] = &physicalDriveCapacity{
			drive:         drive,
			totalCapacity: drive.CapacityGiB,
			claimedCapacity: 0,
			availableCapacity: drive.CapacityGiB,
		}
	}

	// Calculate claimed capacity per physical drive
	for _, container := range containers {
		if container.Status.Allocations == nil {
			continue
		}
		for _, vd := range container.Status.Allocations.VirtualDrives {
			if driveCapacity, exists := driveCapacities[vd.PhysicalUUID]; exists {
				driveCapacity.claimedCapacity += vd.CapacityGiB
				driveCapacity.availableCapacity = driveCapacity.totalCapacity - driveCapacity.claimedCapacity
			}
		}
	}

	// Log per-drive capacity info
	for uuid, dc := range driveCapacities {
		logger.Debug("Physical drive capacity",
			"uuid", uuid,
			"total", dc.totalCapacity,
			"claimed", dc.claimedCapacity,
			"available", dc.availableCapacity)
	}

	// Find physical drives with sufficient capacity and sort by available capacity (most available first)
	type availableDrive struct {
		driveCapacity *physicalDriveCapacity
		available     int
	}
	availableDrives := make([]availableDrive, 0, len(driveCapacities))

	for _, dc := range driveCapacities {
		if dc.availableCapacity >= driveCapacityGiB {
			availableDrives = append(availableDrives, availableDrive{
				driveCapacity: dc,
				available:     dc.availableCapacity,
			})
		}
	}

	// Sort by available capacity descending (most available first for better distribution)
	sort.Slice(availableDrives, func(i, j int) bool {
		return availableDrives[i].available > availableDrives[j].available
	})

	if len(availableDrives) < req.NumDrives {
		// Calculate total available across all drives for error message
		totalAvailable := 0
		for _, dc := range driveCapacities {
			totalAvailable += dc.availableCapacity
		}

		return nil, &InsufficientDrivesError{
			Needed:    totalCapacityNeeded,
			Available: totalAvailable,
		}
	}

	// Allocate virtual drives to physical drives with sufficient capacity
	// Use round-robin across available drives for even distribution
	virtualDrives := make([]weka.VirtualDrive, 0, req.NumDrives)

	for i := 0; i < req.NumDrives; i++ {
		// Round-robin across drives that have capacity
		driveIndex := i % len(availableDrives)
		selectedDrive := availableDrives[driveIndex].driveCapacity

		virtualDrive := weka.VirtualDrive{
			VirtualUUID:  generateVirtualUUID(),
			PhysicalUUID: selectedDrive.drive.UUID,
			CapacityGiB:  driveCapacityGiB,
			DevicePath:   selectedDrive.drive.DevicePath,
		}
		virtualDrives = append(virtualDrives, virtualDrive)

		// Update available capacity for this drive
		selectedDrive.claimedCapacity += driveCapacityGiB
		selectedDrive.availableCapacity -= driveCapacityGiB

		// Re-sort to maintain even distribution
		sort.Slice(availableDrives, func(i, j int) bool {
			return availableDrives[i].driveCapacity.availableCapacity > availableDrives[j].driveCapacity.availableCapacity
		})
	}

	logger.Info("Allocated virtual drives",
		"count", len(virtualDrives),
		"totalCapacityGiB", totalCapacityNeeded,
		"drivesWithCapacity", len(availableDrives))

	return virtualDrives, nil
}

// generateVirtualUUID generates a random UUID for a virtual drive
func generateVirtualUUID() string {
	return string(uuid.NewUUID())
}
