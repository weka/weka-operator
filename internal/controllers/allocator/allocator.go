package allocator

import (
	"context"
	"fmt"
	"strings"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

const (
	DefaultPortsPerContainer = 100
	ReducedPortsPerContainer = 60
	WekaPortRangeSize        = 100 // Used for aggregating container port claims
	// Cluster port range: container ports + headroom for single-port allocations
	DefaultClusterPortRange = 500 // 100 * 5 containers
	ReducedClusterPortRange = 260 // 60 * 4 containers + 20 for single-port allocations
	// Offset where single-port allocations start (at end of container port ranges)
	DefaultSinglePortsOffset = 300 // After 3 containers worth of ports (100*3), leaving room for 2 more + single ports
	ReducedSinglePortsOffset = 240 // After 4 containers worth of ports (60*4), leaving 20 for single ports
)

// getClusterPortRange returns the default cluster port range based on feature flags.
// Returns 260 if agent_validate_60_ports_per_container is set, otherwise returns 500.
// Returns error if feature flags are not available.
func getClusterPortRange(ctx context.Context, image string) (int, error) {
	flags, err := services.FeatureFlagsCache.GetFeatureFlags(ctx, image)
	if err != nil {
		return 0, fmt.Errorf("feature flags not available: %w", err)
	}
	if flags != nil && flags.AgentValidate60PortsPerContainer {
		return ReducedClusterPortRange, nil
	}
	return DefaultClusterPortRange, nil
}

// getSinglePortsOffset returns the offset where single-port allocations start.
// Returns 240 if agent_validate_60_ports_per_container is set (60*4 containers),
// otherwise returns 300.
// Returns error if feature flags are not available.
func getSinglePortsOffset(ctx context.Context, image string) (int, error) {
	flags, err := services.FeatureFlagsCache.GetFeatureFlags(ctx, image)
	if err != nil {
		return 0, fmt.Errorf("feature flags not available: %w", err)
	}
	if flags != nil && flags.AgentValidate60PortsPerContainer {
		return ReducedSinglePortsOffset, nil
	}
	return DefaultSinglePortsOffset, nil
}

// derivePortConfigFromClusterRange derives port configuration from the cluster's
// already-allocated port range size. This ensures container allocation is consistent
// with the cluster's allocation decision, without needing to re-fetch feature flags.
func derivePortConfigFromClusterRange(clusterPortRange int) (portsPerContainer int, singlePortsOffset int) {
	if clusterPortRange == ReducedClusterPortRange {
		return ReducedPortsPerContainer, ReducedSinglePortsOffset
	}
	return DefaultPortsPerContainer, DefaultSinglePortsOffset
}

type AllocateClusterRangeError struct {
	Msg string
}

func (e *AllocateClusterRangeError) Error() string {
	return e.Msg
}

type Allocator interface {
	AllocateClusterRange(ctx context.Context, cluster *weka.WekaCluster) error
	EnsureManagementProxyPort(ctx context.Context, cluster *weka.WekaCluster) error
}

type AllocatorNodeInfo struct {
	AvailableDrives []string
	// SharedDrives contains shared drive information for drive sharing mode (proxy mode)
	// Empty if node doesn't have shared drives or is using non-proxy mode
	SharedDrives []domain.SharedDriveInfo
}

type ResourcesAllocator struct {
	client client.Client
}

func (t *ResourcesAllocator) EnsureManagementProxyPort(ctx context.Context, cluster *weka.WekaCluster) error {
	// Aggregate container port allocations to avoid conflicts
	nodePortClaims, err := t.AggregateContainerPortAllocations(ctx)
	if err != nil {
		return fmt.Errorf("failed to aggregate container port allocations: %w", err)
	}

	// Derive offset from cluster's already-allocated port range
	_, singlePortsOffset := derivePortConfigFromClusterRange(cluster.Status.Ports.PortRange)

	// Allocate management proxy port (EnsureGlobalRangeWithOffset handles idempotency)
	managementProxyPortRange, err := EnsureGlobalRangeWithOffset(cluster, "managementProxy", 1, singlePortsOffset, nodePortClaims)
	if err != nil {
		return fmt.Errorf("failed to allocate management proxy port: %w", err)
	}

	// Update cluster status (caller will persist)
	cluster.Status.Ports.ManagementProxyPort = managementProxyPortRange.Base

	return nil
}

// AggregateContainerPortAllocations aggregates all per-container port allocations from WekaContainer Status
// across all nodes. This is used to ensure port allocations don't conflict with existing
// container allocations.
func (t *ResourcesAllocator) AggregateContainerPortAllocations(ctx context.Context) ([]Range, error) {
	// List all nodes
	nodeList := &v1.NodeList{}
	err := t.client.List(ctx, nodeList)
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	aggregatedRanges := []Range{}
	kubeService := kubernetes.NewKubeService(t.client)

	// Aggregate port allocations from all containers on each node
	for _, node := range nodeList.Items {
		containers, err := kubeService.GetWekaContainersSimple(ctx, "", node.Name, nil)
		if err != nil {
			continue
		}

		for _, container := range containers {
			if container.Status.Allocations == nil {
				continue
			}

			// Add WekaPort range (100 consecutive ports)
			if container.Status.Allocations.WekaPort > 0 {
				aggregatedRanges = append(aggregatedRanges, Range{
					Base: container.Status.Allocations.WekaPort,
					Size: WekaPortRangeSize,
				})
			}

			// Add AgentPort (single port)
			if container.Status.Allocations.AgentPort > 0 {
				aggregatedRanges = append(aggregatedRanges, Range{
					Base: container.Status.Allocations.AgentPort,
					Size: 1,
				})
			}
		}
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
	for _, c := range clusterList.Items {
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

func (t *ResourcesAllocator) AllocateClusterRange(ctx context.Context, cluster *weka.WekaCluster) error {
	// Validate Spec hasn't changed if already allocated
	if cluster.Spec.Ports.BasePort != 0 && cluster.Status.Ports.BasePort != 0 && cluster.Status.Ports.BasePort != cluster.Spec.Ports.BasePort {
		return fmt.Errorf("updating base port is not supported")
	}
	if cluster.Spec.Ports.PortRange != 0 && cluster.Status.Ports.PortRange != 0 && cluster.Status.Ports.PortRange != cluster.Spec.Ports.PortRange {
		return fmt.Errorf("updating port range is not supported")
	}

	// If already allocated in cluster Status, nothing to do
	// The step predicate should prevent re-entry, but we double-check here
	if cluster.Status.Ports.BasePort != 0 {
		return nil
	}

	// List all clusters to get existing port allocations from their Status
	clusterRanges, err := t.aggregateClusterPortRanges(ctx)
	if err != nil {
		return err
	}

	// Determine target port range size
	targetSize := cluster.Spec.Ports.PortRange
	if targetSize == 0 {
		targetSize, err = getClusterPortRange(ctx, cluster.Spec.Image)
		if err != nil {
			return fmt.Errorf("failed to get cluster port range: %w", err)
		}
	}

	// Determine target base port
	targetPort := cluster.Spec.Ports.BasePort
	if targetPort == 0 {
		// Find a free port range by polling existing clusters
		targetPort, err = clusterRanges.GetFreeRange(targetSize)
		if err != nil {
			return err
		}
	}

	// Validate the range is available
	isAvailable := clusterRanges.IsClusterRangeAvailable(Range{Base: targetPort, Size: targetSize})
	if !isAvailable {
		msg := fmt.Sprintf("range %d-%d is not available", targetPort, targetPort+targetSize)
		return &AllocateClusterRangeError{Msg: msg}
	}

	// Set the cluster's port range in Status
	cluster.Status.Ports.BasePort = targetPort
	cluster.Status.Ports.PortRange = targetSize

	// Aggregate per-container port claims to prevent conflicts with singleton ports
	nodePortClaims, err := t.AggregateContainerPortAllocations(ctx)
	if err != nil {
		return fmt.Errorf("failed to aggregate container port allocations: %w", err)
	}

	// Determine singleton ports offset
	singlePortsOffset, err := getSinglePortsOffset(ctx, cluster.Spec.Image)
	if err != nil {
		return fmt.Errorf("failed to get single ports offset: %w", err)
	}

	// Allocate singleton ports (LB, LB Admin, S3)
	// Each allocation updates cluster.Status, so the next call sees the previous allocation

	// Allocate LB port
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

	// Allocate LB Admin port
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

	// Allocate S3 port
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

	// Management proxy port is allocated on-demand when the management proxy is first enabled
	// This avoids wasting a port if the feature is not used

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

func (f *FailedAllocations) Error() string {
	// build new-line separated string of container:original error
	strBuilder := strings.Builder{}
	for _, failed := range *f {
		strBuilder.WriteString(fmt.Sprintf("%s: %s\n", failed.Container.Name, failed.Err.Error()))
	}
	return strBuilder.String()
}

func NewContainerName(role string) string {
	guid := string(uuid.NewUUID())
	return fmt.Sprintf("%s-%s", role, guid)
}

// MarshalYAML implements the yaml.Marshaler interface for CustomType.
func (c Owner) MarshalYAML() (interface{}, error) {
	return fmt.Sprintf("%s;%s;%s;%s", c.ClusterName, c.Namespace, c.Container, c.Role), nil
}

// UnmarshalYAML implements the yaml.Unmarshaler interface for CustomType.
func (c *Owner) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// Temporary variable to hold the combined value during unmarshalling.
	var combined string
	if err := unmarshal(&combined); err != nil {
		return err
	}

	// Custom unmarshalling logic to split the combined string back into FieldA and FieldB.
	parts := strings.Split(combined, ";")
	if len(parts) != 4 {
		return fmt.Errorf("invalid Owner format: %s", combined)
	}
	c.ClusterName = parts[0]
	c.Namespace = parts[1]
	c.Container = parts[2]
	c.Role = parts[3]
	return nil
}

func (o Owner) IsSameClusterAndRole(owner Owner) bool {
	if owner.Namespace != o.Namespace {
		return false
	}
	if owner.ClusterName != o.ClusterName {
		return false
	}
	if owner.Role != o.Role {
		return false
	}
	return true
}

func (o Owner) IsSameOwner(owner Owner) bool {
	if owner.Namespace != o.Namespace {
		return false
	}
	if owner.ClusterName != o.ClusterName {
		return false
	}
	return true
}

func (c Owner) ToOwnerRole() OwnerRole {
	return OwnerRole{
		OwnerCluster: c.OwnerCluster,
		Role:         c.Role,
	}
}

type OwnerRole struct {
	OwnerCluster
	Role string
}

// MarshalYAML implements the yaml.Marshaler interface for CustomType.
func (c OwnerRole) MarshalYAML() (interface{}, error) {
	return fmt.Sprintf("%s;%s;%s", c.ClusterName, c.Namespace, c.Role), nil
}

// UnmarshalYAML implements the yaml.Unmarshaler interface for CustomType.
func (c *OwnerRole) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// Temporary variable to hold the combined value during unmarshalling.
	var combined string
	if err := unmarshal(&combined); err != nil {
		return err
	}

	// Custom unmarshalling logic to split the combined string back into FieldA and FieldB.
	parts := strings.Split(combined, ";")
	if len(parts) != 3 {
		return fmt.Errorf("invalid OwnerRole format: %s", combined)
	}
	c.ClusterName = parts[0]
	c.Namespace = parts[1]
	c.Role = parts[2]
	return nil
}

// MarshalYAML implements the yaml.Marshaler interface for CustomType.
func (c OwnerCluster) MarshalYAML() (interface{}, error) {
	return fmt.Sprintf("%s;%s", c.ClusterName, c.Namespace), nil
}

// UnmarshalYAML implements the yaml.Unmarshaler interface for CustomType.
func (c *OwnerCluster) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// Temporary variable to hold the combined value during unmarshalling.
	var combined string
	if err := unmarshal(&combined); err != nil {
		return err
	}

	// Custom unmarshalling logic to split the combined string back into FieldA and FieldB.
	parts := strings.Split(combined, ";")
	if len(parts) != 2 {
		return fmt.Errorf("invalid OwnerCluster format: %s", combined)
	}
	c.ClusterName = parts[0]
	c.Namespace = parts[1]
	return nil
}

// GetAllocator creates and returns a new ResourcesAllocator instance.
// Port allocations are serialized by polling WekaCluster Status objects,
func GetAllocator(client client.Client) Allocator {
	return &ResourcesAllocator{
		client: client,
	}
}
