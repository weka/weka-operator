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

	"github.com/weka/weka-operator/internal/pkg/domain"
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
	FeatureFlags  *domain.FeatureFlags // Feature flags for the container's image (determines ports per container)
	NumDrives     int
	CapacityGiB   int // Total capacity that should be allocated for the container (mutually exclusive with NumDrives)
	FailureDomain *string
	AllocateWeka  bool // Whether to allocate weka port (60 or 100 ports based on feature flags)
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
	nodeName := req.Node.Name

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "AllocateResources", "node", nodeName)
	defer end()

	// List all containers on this node (across all namespaces)
	kubeService := kubernetes.NewKubeService(a.client)
	containers, err := kubeService.GetWekaContainersSimple(ctx, "", nodeName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers on node %s: %w", nodeName, err)
	}

	// Aggregate existing allocations from all containers on this node
	allocatedDrives, allocatedPorts, err := a.aggregateNodeAllocations(ctx, containers)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate node allocations: %w", err)
	}

	result := &AllocationResult{}

	// Allocate drives if needed
	if req.Container.UsesDriveSharing() {
		// Allocate shared drives (virtual drives with capacity)
		virtualDrives, err := a.AllocateSharedDrives(ctx, req, containers)
		if err != nil {
			return nil, err
		}
		result.VirtualDrives = virtualDrives
		logger.Debug("Allocated virtual drives", "count", len(result.VirtualDrives))
	} else if req.NumDrives > 0 {
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

	// Allocate port ranges based on request flags and feature flags
	wekaPort, agentPort, err := a.allocatePortRangesFromStatus(ctx, req.Cluster, req.FeatureFlags, allocatedPorts, req.AllocateWeka, req.AllocateAgent)
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
func (a *ContainerResourceAllocator) aggregateNodeAllocations(ctx context.Context, containers []weka.WekaContainer) (map[string]bool, map[int]bool, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "aggregateNodeAllocations")
	defer end()

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
		"containers", len(containers),
		"allocatedDrives", len(allocatedDrives),
		"allocatedPorts", len(allocatedPorts),
	)

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
// featureFlags: feature flags for the container's image (determines ports per container)
// allocateWeka: if true, allocate weka port (60 or 100 ports based on feature flags)
// allocateAgent: if true, allocate agent port (1 port)
func (a *ContainerResourceAllocator) allocatePortRangesFromStatus(ctx context.Context, cluster *weka.WekaCluster, featureFlags *domain.FeatureFlags, allocatedPorts map[int]bool, allocateWeka bool, allocateAgent bool) (wekaPort int, agentPort int, err error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "allocatePortRangesFromStatus")
	defer end()

	// Get the cluster's allocated port range from cluster Status
	if cluster.Status.Ports.BasePort == 0 {
		return 0, 0, fmt.Errorf("no port range allocated for cluster %s", cluster.Name)
	}

	clusterRange := Range{
		Base: cluster.Status.Ports.BasePort,
		Size: cluster.Status.Ports.PortRange,
	}

	logger.Debug("Cluster port range", "base", clusterRange.Base, "size", clusterRange.Size)

	// Build list of allocated ranges from allocatedPorts map and singleton ports from cluster Status
	// This prevents container ports from conflicting with LB/S3/LbAdmin/ManagementProxy ports
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

	// Add global singleton ports (LB, S3, LbAdmin, ManagementProxy) to exclusion list
	allocatedRanges = append(allocatedRanges, GetClusterGlobalAllocatedRanges(cluster)...)

	// Get port configuration from feature flags for the container's image
	portsPerContainer := getPortsPerContainerFromFlags(featureFlags)
	singlePortsOffset := getSinglePortsOffsetFromFlags(featureFlags)

	// Allocate weka port if requested (portsPerContainer ports from cluster base)
	wekaPortRange := 0
	if allocateWeka {
		wekaPortRange, err = GetFreeRange(clusterRange, allocatedRanges, portsPerContainer)
		if err != nil {
			return 0, 0, fmt.Errorf("failed to find free weka port range: %w", err)
		}
		logger.Debug("Allocated weka port", "wekaPort", wekaPortRange)
	}

	// Allocate agent port if requested (1 port from offset range)
	agentPortRange := 0
	if allocateAgent {
		agentPortRange, err = GetFreeRangeWithOffset(clusterRange, allocatedRanges, 1, singlePortsOffset)
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
	FailedDrives []string // Drives to remove (serial IDs for regular drives, virtual UUIDs for virtual drives)
	NumNewDrives int      // Number of replacement drives needed
	CapacityGiB  int      // Total capacity that should be allocated for the container (mutually exclusive with NumNewDrives)
}

// DriveReallocationResult represents the result of drive reallocation
type DriveReallocationResult struct {
	NewDrives        []string            // New drives allocated (regular drives mode)
	AllDrives        []string            // All drives after reallocation (regular drives mode)
	NewVirtualDrives []weka.VirtualDrive // New virtual drives allocated (drive sharing mode)
	AllVirtualDrives []weka.VirtualDrive // All virtual drives after reallocation (drive sharing mode)
}

// ReallocateDrives replaces failed drives with new ones (hot-swap scenario)
// This is used when drives fail and need to be replaced, or when scaling up drives
// Supports both regular drives and virtual drives (drive sharing mode)
func (a *ContainerResourceAllocator) ReallocateDrives(ctx context.Context, req *DriveReallocationRequest) (*DriveReallocationResult, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ContainerResourceAllocator.ReallocateDrives")
	defer end()

	logger.Info("Reallocating drives for container",
		"container", req.Container.Name,
		"failedDrives", req.FailedDrives,
		"numNewDrives", req.NumNewDrives,
		"capacityGiB", req.CapacityGiB,
		"useDriveSharing", req.Container.UsesDriveSharing(),
	)

	// Aggregate claimed capacity from all container Status on this node (across all namespaces)
	// Virtual drives are node-level resources, not namespace-level
	kubeService := kubernetes.NewKubeService(a.client)
	containers, err := kubeService.GetWekaContainersSimple(ctx, "", req.Node.Name, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers on node: %w", err)
	}

	// Check if we're in drive sharing mode (virtual drives)
	if req.Container.UsesDriveSharing() {
		return a.reallocateVirtualDrives(ctx, req, containers)
	}

	// Regular drive mode
	return a.reallocateRegularDrives(ctx, req, containers)
}

// reallocateRegularDrives handles reallocation for regular (non-shared) drives
func (a *ContainerResourceAllocator) reallocateRegularDrives(ctx context.Context, req *DriveReallocationRequest, containers []weka.WekaContainer) (*DriveReallocationResult, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "reallocateRegularDrives")
	defer end()

	// Aggregate existing allocations from all containers on this node
	allocatedDrives, _, err := a.aggregateNodeAllocations(ctx, containers)
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

	logger.Info("Successfully reallocated regular drives",
		"newDrives", newDrives,
		"totalDrives", len(allDrives))

	return &DriveReallocationResult{
		NewDrives: newDrives,
		AllDrives: allDrives,
	}, nil
}

// reallocateVirtualDrives handles reallocation for virtual drives (drive sharing mode)
func (a *ContainerResourceAllocator) reallocateVirtualDrives(ctx context.Context, req *DriveReallocationRequest, containers []weka.WekaContainer) (*DriveReallocationResult, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "reallocateVirtualDrives")
	defer end()

	// Create allocation request for new virtual drives
	allocReq := &AllocationRequest{
		Container:     req.Container,
		Node:          req.Node,
		Cluster:       nil, // Not needed for shared drive allocation
		NumDrives:     req.NumNewDrives,
		CapacityGiB:   req.CapacityGiB,
		FailureDomain: nil,
		AllocateWeka:  false,
		AllocateAgent: false,
	}

	// Allocate new virtual drives using shared drive allocation logic
	newVirtualDrives, err := a.AllocateSharedDrives(ctx, allocReq, containers)
	if err != nil {
		logger.Error(err, "Failed to allocate new virtual drives")
		return nil, fmt.Errorf("failed to allocate new virtual drives: %w", err)
	}

	logger.Debug("Allocated new virtual drives", "count", len(newVirtualDrives))

	// Calculate all virtual drives (existing - failed + new)
	currentVirtualDrives := req.Container.Status.Allocations.VirtualDrives
	allVirtualDrives := make([]weka.VirtualDrive, 0)

	// Add virtual drives that weren't failed
	for _, vd := range currentVirtualDrives {
		// Check against both virtual UUID and serial (for flexibility)
		if !slices.Contains(req.FailedDrives, vd.VirtualUUID) &&
			!slices.Contains(req.FailedDrives, vd.Serial) {
			allVirtualDrives = append(allVirtualDrives, vd)
		}
	}

	// Add new virtual drives
	allVirtualDrives = append(allVirtualDrives, newVirtualDrives...)

	logger.Info("Successfully reallocated virtual drives",
		"newVirtualDrives", len(newVirtualDrives),
		"totalVirtualDrives", len(allVirtualDrives),
		"failedCount", len(req.FailedDrives))

	return &DriveReallocationResult{
		NewVirtualDrives: newVirtualDrives,
		AllVirtualDrives: allVirtualDrives,
	}, nil
}

// AllocateSharedDrives allocates virtual drives from shared physical drives
// Each virtual drive gets a random UUID and is mapped to a physical drive
func (a *ContainerResourceAllocator) AllocateSharedDrives(ctx context.Context, req *AllocationRequest, containers []weka.WekaContainer) ([]weka.VirtualDrive, error) {
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

	if len(sharedDrives) == 0 {
		return nil, fmt.Errorf("no shared drives found on node %s", req.Node.Name)
	}

	if req.CapacityGiB > 0 {
		// Allocate based on total capacity divided by drive types (TLC/QLC)
		// QLC can be disabled by setting driveTypesRatio.qlc=0
		return a.allocateSharedDrivesByCapacityWithTypes(ctx, req, containers, sharedDrives)
	} else if req.NumDrives > 0 {
		// Allocate based on number of drives needed
		return a.allocateSharedDrivesByDrivesNum(ctx, req, containers, sharedDrives)
	} else {
		return nil, fmt.Errorf("either NumDrives or CapacityGiB must be specified for shared drive allocation")
	}
}

// physicalDriveCapacity tracks capacity usage for a physical drive
type physicalDriveCapacity struct {
	drive             domain.SharedDriveInfo
	totalCapacity     int
	claimedCapacity   int
	availableCapacity int
}

// virtualDriveAllocationPlan represents a planned allocation of a virtual drive
type virtualDriveAllocationPlan struct {
	physicalUUID string
	capacityGiB  int
	serial       string
}

// buildDriveCapacityMap builds per-physical-drive capacity tracking
// Returns a map of physical drive UUID to capacity information, with claimed capacity calculated
func buildDriveCapacityMap(ctx context.Context, availableSharedDrives []domain.SharedDriveInfo, containers []weka.WekaContainer) map[string]*physicalDriveCapacity {
	_, logger, end := instrumentation.GetLogSpan(ctx, "buildDriveCapacityMap")
	defer end()

	// Initialize capacity tracking for all available drives
	driveCapacities := make(map[string]*physicalDriveCapacity)
	for _, drive := range availableSharedDrives {
		driveCapacities[drive.PhysicalUUID] = &physicalDriveCapacity{
			drive:             drive,
			totalCapacity:     drive.CapacityGiB,
			claimedCapacity:   0,
			availableCapacity: drive.CapacityGiB,
		}
	}

	// Calculate claimed capacity per physical drive from existing allocations
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
	perDriveCapacities := make([]string, 0, len(driveCapacities))
	for uuid, dc := range driveCapacities {
		perDriveCapacities = append(perDriveCapacities, fmt.Sprintf("Drive %s: Total=%d GiB, Claimed=%d GiB, Available=%d GiB",
			uuid, dc.totalCapacity, dc.claimedCapacity, dc.availableCapacity))
	}
	logger.Debug("Physical drive capacities", "details", perDriveCapacities)

	return driveCapacities
}

// filterAndSortUsableDrives filters drives by minimum capacity and sorts them by available capacity (descending)
func filterAndSortUsableDrives(driveCapacities map[string]*physicalDriveCapacity, minCapacityGiB int) []*physicalDriveCapacity {
	usableDrives := make([]*physicalDriveCapacity, 0, len(driveCapacities))

	for _, dc := range driveCapacities {
		if dc.availableCapacity >= minCapacityGiB {
			usableDrives = append(usableDrives, dc)
		}
	}

	// Sort by available capacity descending (most available first for better distribution)
	sort.Slice(usableDrives, func(i, j int) bool {
		return usableDrives[i].availableCapacity > usableDrives[j].availableCapacity
	})

	return usableDrives
}

// allocateSharedDrivesByCapacityWithTypes allocates virtual drives considering drive types (QLC/TLC)
// based on the DriveTypesRatio specified in the container spec
func (a *ContainerResourceAllocator) allocateSharedDrivesByCapacityWithTypes(ctx context.Context, req *AllocationRequest, containers []weka.WekaContainer, availableSharedDrives []domain.SharedDriveInfo) ([]weka.VirtualDrive, error) {
	numCores := req.Container.Spec.NumCores

	// Calculate TLC and QLC capacities based on ratio
	tlcCapacityNeeded := req.Container.Spec.GetTlcContainerCapacity()
	qlcCapacityNeeded := req.Container.Spec.GetQlcContainerCapacity()

	// Validate minimum drive count constraint
	// Each drive type gets numCores drives if it needs any capacity
	// Each drive must be at least MinChunkSizeGiB (384 GiB)
	if tlcCapacityNeeded > 0 {
		minTlcCapacity := numCores * MinChunkSizeGiB
		if tlcCapacityNeeded < minTlcCapacity {
			return nil, fmt.Errorf(
				"insufficient TLC capacity for %s: need at least %d GiB to allocate %d TLC drives (minimum %d GiB per drive), but only %d GiB available",
				req.Container.Name,
				minTlcCapacity,
				numCores,
				MinChunkSizeGiB,
				tlcCapacityNeeded,
			)
		}
	}

	if qlcCapacityNeeded > 0 {
		minQlcCapacity := numCores * MinChunkSizeGiB
		if qlcCapacityNeeded < minQlcCapacity {
			return nil, fmt.Errorf(
				"insufficient QLC capacity for %s: need at least %d GiB to allocate %d QLC drives (minimum %d GiB per drive), but only %d GiB available",
				req.Container.Name,
				minQlcCapacity,
				numCores,
				MinChunkSizeGiB,
				qlcCapacityNeeded,
			)
		}
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "allocateSharedDrivesByCapacityWithTypes",
		"tlcCapacityNeeded", tlcCapacityNeeded,
		"qlcCapacityNeeded", qlcCapacityNeeded,
		"numCores", numCores,
	)
	defer end()

	logger.Info("Allocating virtual drives by capacity with drive types",
		"numCores", numCores,
	)

	// Separate drives by type
	tlcDrives := make([]domain.SharedDriveInfo, 0)
	qlcDrives := make([]domain.SharedDriveInfo, 0)

	for _, drive := range availableSharedDrives {
		switch drive.Type {
		case "TLC":
			tlcDrives = append(tlcDrives, drive)
		case "QLC":
			qlcDrives = append(qlcDrives, drive)
		}
	}

	logger.Debug("Drives separated by type",
		"tlcDrives", len(tlcDrives),
		"qlcDrives", len(qlcDrives),
	)

	allVirtualDrives := make([]weka.VirtualDrive, 0)

	// Allocate TLC drives if needed
	if tlcCapacityNeeded > 0 {
		if len(tlcDrives) == 0 {
			return nil, fmt.Errorf("no TLC drives available but TLC capacity %d GiB is required", tlcCapacityNeeded)
		}

		tlcDriveCapacities := buildDriveCapacityMap(ctx, tlcDrives, containers)
		generator := NewAllocationStrategyGenerator(tlcCapacityNeeded, numCores, MinChunkSizeGiB, tlcDriveCapacities)

		done := make(chan struct{})
		defer close(done)

		tlcAllocated := false
		for strategy := range generator.GenerateStrategies(done) {
			_, logger, end := instrumentation.GetLogSpan(ctx, "TryingTLCAllocationStrategy",
				"description", strategy.Description,
				"numDrives", strategy.NumDrives(),
				"strategyTotalCapacity", strategy.TotalCapacity(),
				"driveSizes", strategy.DriveSizes,
			)
			defer end()

			usableDrives := filterAndSortUsableDrives(tlcDriveCapacities, MinChunkSizeGiB)
			canAllocate, allocPlan := tryAllocateStrategy(usableDrives, strategy)

			if canAllocate {
				for _, alloc := range allocPlan {
					virtualDrive := weka.VirtualDrive{
						VirtualUUID:  generateVirtualUUID(),
						PhysicalUUID: alloc.physicalUUID,
						CapacityGiB:  alloc.capacityGiB,
						Serial:       alloc.serial,
						Type:         "TLC",
					}
					allVirtualDrives = append(allVirtualDrives, virtualDrive)
				}

				logger.Info("Successfully allocated TLC virtual drives",
					"numVirtualDrives", len(allocPlan),
					"totalAllocatedGiB", strategy.TotalCapacity(),
				)

				tlcAllocated = true
				break
			}
		}

		if !tlcAllocated {
			totalAvailable := 0
			for _, dc := range tlcDriveCapacities {
				totalAvailable += dc.availableCapacity
			}
			return nil, &InsufficientDriveCapacityError{
				NeededGiB:    tlcCapacityNeeded,
				AvailableGiB: totalAvailable,
				Type:         "TLC",
			}
		}
	}

	// Allocate QLC drives if needed
	if qlcCapacityNeeded > 0 {
		if len(qlcDrives) == 0 {
			return nil, fmt.Errorf("no QLC drives available but QLC capacity %d GiB is required", qlcCapacityNeeded)
		}

		qlcDriveCapacities := buildDriveCapacityMap(ctx, qlcDrives, containers)
		generator := NewAllocationStrategyGenerator(qlcCapacityNeeded, numCores, MinChunkSizeGiB, qlcDriveCapacities)

		done := make(chan struct{})
		defer close(done)

		qlcAllocated := false
		for strategy := range generator.GenerateStrategies(done) {
			_, logger, end := instrumentation.GetLogSpan(ctx, "TryingQLCAllocationStrategy",
				"description", strategy.Description,
				"numDrives", strategy.NumDrives(),
				"strategyTotalCapacity", strategy.TotalCapacity(),
				"driveSizes", strategy.DriveSizes,
			)
			defer end()

			usableDrives := filterAndSortUsableDrives(qlcDriveCapacities, MinChunkSizeGiB)
			canAllocate, allocPlan := tryAllocateStrategy(usableDrives, strategy)

			if canAllocate {
				for _, alloc := range allocPlan {
					virtualDrive := weka.VirtualDrive{
						VirtualUUID:  generateVirtualUUID(),
						PhysicalUUID: alloc.physicalUUID,
						CapacityGiB:  alloc.capacityGiB,
						Serial:       alloc.serial,
						Type:         "QLC",
					}
					allVirtualDrives = append(allVirtualDrives, virtualDrive)
				}

				logger.Info("Successfully allocated QLC virtual drives",
					"numVirtualDrives", len(allocPlan),
					"totalAllocatedGiB", strategy.TotalCapacity(),
				)

				qlcAllocated = true
				break
			}
		}

		if !qlcAllocated {
			totalAvailable := 0
			for _, dc := range qlcDriveCapacities {
				totalAvailable += dc.availableCapacity
			}
			return nil, &InsufficientDriveCapacityError{
				NeededGiB:    qlcCapacityNeeded,
				AvailableGiB: totalAvailable,
				Type:         "QLC",
			}
		}
	}

	logger.Info("Successfully allocated all virtual drives with drive types",
		"totalVirtualDrives", len(allVirtualDrives),
		"tlcCapacity", tlcCapacityNeeded,
		"qlcCapacity", qlcCapacityNeeded,
	)

	return allVirtualDrives, nil
}

// tryAllocateStrategy attempts to allocate virtual drives according to the given strategy
// Returns true if allocation is possible, along with the allocation plan
func tryAllocateStrategy(usableDrives []*physicalDriveCapacity, strategy AllocationStrategy) (bool, []virtualDriveAllocationPlan) {
	if len(usableDrives) == 0 {
		return false, nil
	}

	// Make a copy of available capacities for simulation
	availableCapacities := make([]int, len(usableDrives))
	for i, ud := range usableDrives {
		availableCapacities[i] = ud.availableCapacity
	}

	allocPlan := make([]virtualDriveAllocationPlan, 0, len(strategy.DriveSizes))

	// Try to allocate each drive in the strategy
	for _, driveSizeGiB := range strategy.DriveSizes {
		allocated := false

		// Find a physical drive with sufficient capacity
		// Start from the one with most available capacity
		for j := range usableDrives {
			if availableCapacities[j] >= driveSizeGiB {
				// Allocate from this drive
				allocPlan = append(allocPlan, virtualDriveAllocationPlan{
					physicalUUID: usableDrives[j].drive.PhysicalUUID,
					capacityGiB:  driveSizeGiB,
					serial:       usableDrives[j].drive.Serial,
				})
				availableCapacities[j] -= driveSizeGiB
				allocated = true

				// Re-sort to maintain distribution (drive with most capacity first)
				// This is a simple bubble-down of the used drive
				for k := j; k < len(usableDrives)-1; k++ {
					if availableCapacities[k] < availableCapacities[k+1] {
						availableCapacities[k], availableCapacities[k+1] = availableCapacities[k+1], availableCapacities[k]
						usableDrives[k], usableDrives[k+1] = usableDrives[k+1], usableDrives[k]
					} else {
						break
					}
				}
				break
			}
		}

		if !allocated {
			// Cannot allocate this drive
			return false, nil
		}
	}

	return true, allocPlan
}

func (a *ContainerResourceAllocator) allocateSharedDrivesByDrivesNum(ctx context.Context, req *AllocationRequest, containers []weka.WekaContainer, availableSharedDrives []domain.SharedDriveInfo) ([]weka.VirtualDrive, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "allocateSharedDrivesByDrivesNum")
	defer end()

	// Filter for TLC drives only in driveCapacity + numDrives mode
	tlcDrives := make([]domain.SharedDriveInfo, 0)
	for _, drive := range availableSharedDrives {
		if drive.Type == "TLC" {
			tlcDrives = append(tlcDrives, drive)
		}
	}

	logger.Debug("Filtered for TLC drives only",
		"totalSharedDrives", len(availableSharedDrives),
		"tlcDrives", len(tlcDrives),
	)

	if len(tlcDrives) < req.NumDrives {
		return nil, &InsufficientDrivesError{Needed: req.NumDrives, Available: len(tlcDrives)}
	}

	// Calculate capacity needed per virtual drive
	driveCapacityGiB := req.Container.Spec.DriveCapacity
	if driveCapacityGiB == 0 {
		return nil, fmt.Errorf("container has UseDriveSharing=true but DriveCapacity is not set")
	}
	totalCapacityNeeded := req.NumDrives * driveCapacityGiB

	driveCapacities := buildDriveCapacityMap(ctx, tlcDrives, containers)

	// Find physical drives with sufficient capacity and sort by available capacity (most available first)
	availableDrives := filterAndSortUsableDrives(driveCapacities, driveCapacityGiB)

	availableCapacities := make([]int, len(availableDrives))
	for i, ad := range availableDrives {
		availableCapacities[i] = ad.availableCapacity
	}

	logger.Info("Available drives based on required drive capacity",
		"requiredDriveCapacityGiB", driveCapacityGiB,
		"num", len(availableDrives),
		"availableDriveCapacities", availableCapacities,
	)

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
		selectedDrive := availableDrives[0] // Drive with most available capacity

		msg := fmt.Sprintf("Allocating virtual drive %d from physical drive %s (available: %d GiB, needed: %d GiB)",
			i+1, selectedDrive.drive.PhysicalUUID, selectedDrive.availableCapacity, driveCapacityGiB)
		logger.Debug(msg)

		virtualDrive := weka.VirtualDrive{
			VirtualUUID:  generateVirtualUUID(),
			PhysicalUUID: selectedDrive.drive.PhysicalUUID,
			CapacityGiB:  driveCapacityGiB,
			Serial:       selectedDrive.drive.Serial,
			Type:         "TLC",
		}
		virtualDrives = append(virtualDrives, virtualDrive)

		// Update available capacity for this drive
		selectedDrive.claimedCapacity += driveCapacityGiB
		selectedDrive.availableCapacity -= driveCapacityGiB

		// Re-sort to maintain even distribution (least occupied drive moves to front)
		sort.Slice(availableDrives, func(i, j int) bool {
			return availableDrives[i].availableCapacity > availableDrives[j].availableCapacity
		})
	}

	logger.Info("Allocated TLC virtual drives",
		"count", len(virtualDrives),
		"totalCapacityGiB", totalCapacityNeeded,
		"drivesWithCapacity", len(availableDrives))

	return virtualDrives, nil
}

// generateVirtualUUID generates a random UUID for a virtual drive
func generateVirtualUUID() string {
	return string(uuid.NewUUID())
}
