package allocator

import (
	"context"
	"fmt"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

const (
	DefaultPortsPerContainer = 100
	ReducedPortsPerContainer = 60
	// Cluster port range: container ports + headroom for single-port allocations
	DefaultClusterPortRange = 500 // 100 * 5 containers
	ReducedClusterPortRange = 260 // 60 * 4 containers + 20 for single-port allocations
	// Offset where single-port allocations start (at end of container port ranges)
	DefaultSinglePortsOffset = 300 // After 3 containers worth of ports (100*3), leaving room for 2 more + single ports
	ReducedSinglePortsOffset = 240 // After 4 containers worth of ports (60*4), leaving 20 for single ports
)

// GetPortsPerContainerFromFlags returns 60 under agent_validate_60_ports_per_container, else 100.
func GetPortsPerContainerFromFlags(flags *domain.FeatureFlags) int {
	if flags != nil && flags.AgentValidate60PortsPerContainer {
		return ReducedPortsPerContainer
	}
	return DefaultPortsPerContainer
}

// getClusterPortRangeFromFlags returns 260 under the 60-ports flag, else 500.
func getClusterPortRangeFromFlags(flags *domain.FeatureFlags) int {
	if flags != nil && flags.AgentValidate60PortsPerContainer {
		return ReducedClusterPortRange
	}
	return DefaultClusterPortRange
}

// getSinglePortsOffsetFromFlags returns 240 under the 60-ports flag, else 300.
func getSinglePortsOffsetFromFlags(flags *domain.FeatureFlags) int {
	if flags != nil && flags.AgentValidate60PortsPerContainer {
		return ReducedSinglePortsOffset
	}
	return DefaultSinglePortsOffset
}

// getPortConfigFromFlags returns (60, 240) under the 60-ports flag, else (100, 300).
func getPortConfigFromFlags(flags *domain.FeatureFlags) (portsPerContainer, singlePortsOffset int) {
	if flags != nil && flags.AgentValidate60PortsPerContainer {
		return ReducedPortsPerContainer, ReducedSinglePortsOffset
	}
	return DefaultPortsPerContainer, DefaultSinglePortsOffset
}

// AggregatePortRangesFromContainers extracts WekaPort (size portsPerContainer) and AgentPort (size 1)
// ranges from each container's Status.Allocations.
func AggregatePortRangesFromContainers(containers []weka.WekaContainer, portsPerContainer int) []Range {
	var ranges []Range

	for i := range containers {
		container := containers[i]
		if container.Status.Allocations == nil {
			continue
		}

		if container.Status.Allocations.WekaPort > 0 {
			ranges = append(ranges, Range{
				Base: container.Status.Allocations.WekaPort,
				Size: portsPerContainer,
			})
		}

		if container.Status.Allocations.AgentPort > 0 {
			ranges = append(ranges, Range{
				Base: container.Status.Allocations.AgentPort,
				Size: 1,
			})
		}
	}

	return ranges
}

type AllocateClusterRangeError struct {
	Msg string
}

func (e *AllocateClusterRangeError) Error() string {
	return e.Msg
}

type Allocator interface {
	// AllocateClusterRange allocates cluster-level port ranges, sized from featureFlags if unset in spec.
	AllocateClusterRange(ctx context.Context, cluster *weka.WekaCluster, featureFlags *domain.FeatureFlags) error
	// EnsureManagementProxyPort allocates the management proxy port for the cluster.
	EnsureManagementProxyPort(ctx context.Context, cluster *weka.WekaCluster, featureFlags *domain.FeatureFlags) error
}

type AllocatorNodeInfo struct {
	// AvailableDrives contains available (non-blocked) drives for non-proxy mode.
	AvailableDrives []domain.DriveEntry
	// SharedDrives contains shared drive information for drive sharing mode (proxy mode)
	// Empty if node doesn't have shared drives or is using non-proxy mode
	SharedDrives []domain.SharedDriveInfo
	// BlockedDriveCount is the number of serials in the node's weka.io/blocked-drives annotation
	// (already excluded from AvailableDrives/SharedDrives above). Surfaced so callers can report why a
	// drive vanished from the totals without re-parsing the annotation themselves.
	BlockedDriveCount int
}

type ResourcesAllocator struct {
	client client.Client
}

func (t *ResourcesAllocator) EnsureManagementProxyPort(ctx context.Context, cluster *weka.WekaCluster, featureFlags *domain.FeatureFlags) error {
	nodePortClaims, err := t.AggregateContainerPortAllocations(ctx, featureFlags)
	if err != nil {
		return fmt.Errorf("failed to aggregate container port allocations: %w", err)
	}

	_, singlePortsOffset := getPortConfigFromFlags(featureFlags)

	// Allocate management proxy port (EnsureGlobalRangeWithOffset handles idempotency)
	managementProxyPortRange, err := EnsureGlobalRangeWithOffset(cluster, "managementProxy", 1, singlePortsOffset, nodePortClaims)
	if err != nil {
		return fmt.Errorf("failed to allocate management proxy port: %w", err)
	}

	cluster.Status.Ports.ManagementProxyPort = managementProxyPortRange.Base

	return nil
}

// AggregateContainerPortAllocations aggregates per-container port allocations from every WekaContainer
// Status across all nodes, so new allocations don't conflict with existing ones.
func (t *ResourcesAllocator) AggregateContainerPortAllocations(ctx context.Context, featureFlags *domain.FeatureFlags) ([]Range, error) {
	nodeList := &v1.NodeList{}
	err := t.client.List(ctx, nodeList)
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	kubeService := kubernetes.NewKubeService(t.client)
	portsPerContainer := GetPortsPerContainerFromFlags(featureFlags)

	var aggregatedRanges []Range

	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		containers, err := kubeService.GetWekaContainersSimple(ctx, "", node.Name, nil)
		if err != nil {
			continue
		}

		aggregatedRanges = append(aggregatedRanges, AggregatePortRangesFromContainers(containers, portsPerContainer)...)
	}

	return aggregatedRanges, nil
}

// aggregateClusterPortRanges lists all WekaClusters and builds a map of allocated port ranges
// from their Status.Ports. This is used to find free port ranges for new clusters.
func (t *ResourcesAllocator) aggregateClusterPortRanges(ctx context.Context) (ClusterRanges, error) {
	clusterList := &weka.WekaClusterList{}
	if err := t.client.List(ctx, clusterList); err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}

	clusterRanges := make(ClusterRanges)
	for i := range clusterList.Items {
		c := &clusterList.Items[i]
		if c.Status.Ports.BasePort > 0 {
			owner := OwnerCluster{ClusterName: c.Name, Namespace: c.Namespace}
			clusterRanges[owner] = Range{
				Base: c.Status.Ports.BasePort,
				Size: c.Status.Ports.PortRange,
			}
		}
	}

	return clusterRanges, nil
}

func (t *ResourcesAllocator) AllocateClusterRange(ctx context.Context, cluster *weka.WekaCluster, featureFlags *domain.FeatureFlags) error {
	// Validate Spec hasn't changed if already allocated
	if cluster.Spec.Ports.BasePort != 0 && cluster.Status.Ports.BasePort != 0 && cluster.Status.Ports.BasePort != cluster.Spec.Ports.BasePort {
		return fmt.Errorf("updating base port is not supported")
	}
	if cluster.Spec.Ports.PortRange != 0 && cluster.Status.Ports.PortRange != 0 && cluster.Status.Ports.PortRange != cluster.Spec.Ports.PortRange {
		return fmt.Errorf("updating port range is not supported")
	}

	// The step predicate should prevent re-entry, but we double-check here
	if cluster.Status.Ports.BasePort != 0 {
		return nil
	}

	clusterRanges, err := t.aggregateClusterPortRanges(ctx)
	if err != nil {
		return err
	}

	targetSize := cluster.Spec.Ports.PortRange
	if targetSize == 0 {
		targetSize = getClusterPortRangeFromFlags(featureFlags)
	}

	targetPort := cluster.Spec.Ports.BasePort
	if targetPort == 0 {
		targetPort, err = clusterRanges.GetFreeRange(targetSize)
		if err != nil {
			return err
		}
	}

	isAvailable := clusterRanges.IsClusterRangeAvailable(Range{Base: targetPort, Size: targetSize})
	if !isAvailable {
		msg := fmt.Sprintf("range %d-%d is not available", targetPort, targetPort+targetSize)
		return &AllocateClusterRangeError{Msg: msg}
	}

	cluster.Status.Ports.BasePort = targetPort
	cluster.Status.Ports.PortRange = targetSize

	nodePortClaims, err := t.AggregateContainerPortAllocations(ctx, featureFlags)
	if err != nil {
		return fmt.Errorf("failed to aggregate container port allocations: %w", err)
	}

	singlePortsOffset := getSinglePortsOffsetFromFlags(featureFlags)

	// Each allocation below updates cluster.Status, so the next call sees the previous one.
	var lbPortRange Range
	if cluster.Spec.Ports.LbPort != 0 {
		lbPortRange, err = EnsureSpecificGlobalRange(cluster, "lb", Range{Base: cluster.Spec.Ports.LbPort, Size: 1}, nodePortClaims)
	} else {
		lbPortRange, err = EnsureGlobalRangeWithOffset(cluster, "lb", 1, singlePortsOffset, nodePortClaims)
	}
	if err != nil {
		return fmt.Errorf("failed to allocate LB port: %w", err)
	}
	cluster.Status.Ports.LbPort = lbPortRange.Base

	var lbAdminPortRange Range
	if cluster.Spec.Ports.LbAdminPort != 0 {
		lbAdminPortRange, err = EnsureSpecificGlobalRange(cluster, "lbAdmin", Range{Base: cluster.Spec.Ports.LbAdminPort, Size: 1}, nodePortClaims)
	} else {
		lbAdminPortRange, err = EnsureGlobalRangeWithOffset(cluster, "lbAdmin", 1, singlePortsOffset, nodePortClaims)
	}
	if err != nil {
		return fmt.Errorf("failed to allocate LB Admin port: %w", err)
	}
	cluster.Status.Ports.LbAdminPort = lbAdminPortRange.Base

	var s3PortRange Range
	if cluster.Spec.Ports.S3Port != 0 {
		s3PortRange, err = EnsureSpecificGlobalRange(cluster, "s3", Range{Base: cluster.Spec.Ports.S3Port, Size: 1}, nodePortClaims)
	} else {
		s3PortRange, err = EnsureGlobalRangeWithOffset(cluster, "s3", 1, singlePortsOffset, nodePortClaims)
	}
	if err != nil {
		return fmt.Errorf("failed to allocate S3 port: %w", err)
	}
	cluster.Status.Ports.S3Port = s3PortRange.Base

	// Management proxy port is allocated on-demand when first enabled, to avoid wasting one otherwise.

	return nil
}

func GetClusterGlobalAllocatedRanges(cluster *weka.WekaCluster) (allocatedRanges []Range) {
	if cluster.Status.Ports.LbPort > 0 {
		allocatedRanges = append(allocatedRanges, Range{Base: cluster.Status.Ports.LbPort, Size: 1})
	}
	if cluster.Status.Ports.LbAdminPort > 0 {
		allocatedRanges = append(allocatedRanges, Range{Base: cluster.Status.Ports.LbAdminPort, Size: 1})
	}
	if cluster.Status.Ports.S3Port > 0 {
		allocatedRanges = append(allocatedRanges, Range{Base: cluster.Status.Ports.S3Port, Size: 1})
	}
	if cluster.Status.Ports.ManagementProxyPort > 0 {
		allocatedRanges = append(allocatedRanges, Range{Base: cluster.Status.Ports.ManagementProxyPort, Size: 1})
	}
	return
}

type AllocationFailure struct {
	Err       error
	Container *weka.WekaContainer
}

type FailedAllocations []AllocationFailure

func NewContainerName(role string) string {
	guid := string(uuid.NewUUID())
	return fmt.Sprintf("%s-%s", role, guid)
}

type OwnerRole struct {
	OwnerCluster
	Role string
}

// GetAllocator creates and returns a new ResourcesAllocator instance.
// Port allocations are serialized by polling WekaCluster Status objects,
func GetAllocator(k8sClient client.Client) Allocator {
	return &ResourcesAllocator{
		client: k8sClient,
	}
}
