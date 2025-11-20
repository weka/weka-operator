package allocator

import (
	"context"
	"fmt"
	"strings"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	SinglePortsOffset = 300
	WekaPortRangeSize = 100
)

type AllocateClusterRangeError struct {
	Msg string
}

func (e *AllocateClusterRangeError) Error() string {
	return e.Msg
}

type Allocator interface {
	AllocateClusterRange(ctx context.Context, cluster *weka.WekaCluster) error
	DeallocateCluster(ctx context.Context, cluster *weka.WekaCluster) error
	GetAllocations(ctx context.Context) (*Allocations, error)
	EnsureManagementProxyPort(ctx context.Context, cluster *weka.WekaCluster) error
}

type AllocatorNodeInfo struct {
	AvailableDrives []string
}

type ResourcesAllocator struct {
	configStore AllocationsStore
	client      client.Client
}

func newResourcesAllocator(ctx context.Context, client client.Client) (Allocator, error) {
	cs, err := NewConfigMapStore(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("failed to create config store: %w", err)
	}

	resAlloc := &ResourcesAllocator{
		configStore: cs,
		client:      client,
	}

	return resAlloc, nil
}

func (t *ResourcesAllocator) GetAllocations(ctx context.Context) (*Allocations, error) {
	return t.configStore.GetAllocations(ctx)
}

func (t *ResourcesAllocator) EnsureManagementProxyPort(ctx context.Context, cluster *weka.WekaCluster) error {
	// If port already allocated in cluster status, nothing to do
	if cluster.Status.Ports.ManagementProxyPort != 0 {
		return nil
	}

	owner := OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace}

	allocations, err := t.configStore.GetAllocations(ctx)
	if err != nil {
		return err
	}

	// Check if already allocated in the global allocations (but not in cluster status yet)
	if existingPort, ok := allocations.Global.AllocatedRanges[owner]["managementProxy"]; ok {
		// Port is allocated in ConfigMap but not in cluster status, just update status
		cluster.Status.Ports.ManagementProxyPort = existingPort.Base
		return nil
	}

	nodePortClaims, err := t.AggregateNodePortClaims(ctx, owner)
	if err != nil {
		return fmt.Errorf("failed to aggregate node port claims: %w", err)
	}

	// Allocate management proxy port using the global allocations
	managementProxyPort, err := allocations.EnsureGlobalRangeWithOffset(owner, "managementProxy", 1, SinglePortsOffset, nodePortClaims)
	if err != nil {
		return err
	}

	// Update cluster status
	cluster.Status.Ports.ManagementProxyPort = managementProxyPort.Base

	// Persist the allocations back to the ConfigMap (with optimistic locking)
	err = t.configStore.UpdateAllocations(ctx, allocations)
	if err != nil {
		return err
	}

	return nil
}

// AggregateNodePortClaims aggregates all per-container port claims from node annotations
// across all nodes in the cluster. This is used to ensure global port allocations don't
// conflict with existing container allocations.
func (t *ResourcesAllocator) AggregateNodePortClaims(ctx context.Context, ownerCluster OwnerCluster) ([]Range, error) {
	// List all nodes
	nodeList := &v1.NodeList{}
	err := t.client.List(ctx, nodeList)
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	aggregatedRanges := []Range{}

	// Parse port claims from each node
	for _, node := range nodeList.Items {
		claims, err := ParseNodeClaims(&node)
		if err != nil {
			// Skip nodes with invalid annotations
			continue
		}

		// Extract port ranges for this cluster only
		for portRangeStr, claimKey := range claims.Ports {
			// Parse ClaimKey to check if it belongs to this cluster
			owner, err := ParseClaimKey(claimKey)
			if err != nil {
				continue
			}

			// Only include ranges for the target cluster
			if owner.ClusterName == ownerCluster.ClusterName && owner.Namespace == ownerCluster.Namespace {
				var base, size int
				fmt.Sscanf(portRangeStr, "%d,%d", &base, &size)
				aggregatedRanges = append(aggregatedRanges, Range{Base: base, Size: size})
			}
		}
	}

	return aggregatedRanges, nil
}

func (t *ResourcesAllocator) AllocateClusterRange(ctx context.Context, cluster *weka.WekaCluster) error {
	owner := OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace}

	allocations, err := t.configStore.GetAllocations(ctx)
	if err != nil {
		return nil
	}

	if currentAllocation, ok := allocations.Global.ClusterRanges[owner]; ok {
		if cluster.Spec.Ports.BasePort != 0 {
			if currentAllocation.Base != cluster.Spec.Ports.BasePort {
				return fmt.Errorf("updating port range is not supported yet")
			}
		}

		if cluster.Spec.Ports.BasePort != 0 {
			if currentAllocation.Size != cluster.Spec.Ports.BasePort {
				return fmt.Errorf("updating port range is not supported yet")
			}
		}

		cluster.Status.Ports.LbPort = allocations.Global.AllocatedRanges[owner]["lb"].Base
		cluster.Status.Ports.LbAdminPort = allocations.Global.AllocatedRanges[owner]["lbAdmin"].Base
		cluster.Status.Ports.S3Port = allocations.Global.AllocatedRanges[owner]["s3"].Base

		cluster.Status.Ports.PortRange = currentAllocation.Size
		cluster.Status.Ports.BasePort = currentAllocation.Base
		return nil
	}

	targetPort := cluster.Spec.Ports.BasePort
	targetSize := cluster.Spec.Ports.PortRange

	if targetSize == 0 {
		targetSize = 500
	}

	if targetPort == 0 {
		// if still 0 - lets find a free port
		targetPort, err = allocations.Global.ClusterRanges.GetFreeRange(targetSize)
		if err != nil {
			return err
		}
	}

	isAvailable := allocations.Global.ClusterRanges.IsClusterRangeAvailable(Range{Base: targetPort, Size: targetSize})
	if !isAvailable {
		msg := fmt.Sprintf("range %d-%d is not available", targetPort, targetPort+targetSize)
		return &AllocateClusterRangeError{Msg: msg}
	}

	allocations.Global.ClusterRanges[owner] = Range{
		Base: targetPort,
		Size: targetSize,
	}

	// Aggregate per-container port claims from node annotations to prevent conflicts
	// with global singleton ports (LB, S3, LbAdmin)
	nodePortClaims, err := t.AggregateNodePortClaims(ctx, owner)
	if err != nil {
		return fmt.Errorf("failed to aggregate node port claims: %w", err)
	}

	var envoyPort, envoyAdminPort, s3Port Range

	// allocate envoy, envoys3 and envoyadmin ports and ranges
	if cluster.Spec.Ports.LbPort != 0 {
		envoyPort, err = allocations.EnsureSpecificGlobalRange(owner, "lb", Range{Base: cluster.Spec.Ports.LbPort, Size: 1}, nodePortClaims)
	} else {
		envoyPort, err = allocations.EnsureGlobalRangeWithOffset(owner, "lb", 1, SinglePortsOffset, nodePortClaims)
	}
	if err != nil {
		return err
	}

	if cluster.Spec.Ports.LbAdminPort != 0 {
		envoyAdminPort, err = allocations.EnsureSpecificGlobalRange(owner, "lbAdmin", Range{Base: cluster.Spec.Ports.LbAdminPort, Size: 1}, nodePortClaims)
	} else {
		envoyAdminPort, err = allocations.EnsureGlobalRangeWithOffset(owner, "lbAdmin", 1, SinglePortsOffset, nodePortClaims)
	}
	if err != nil {
		return err
	}

	if cluster.Spec.Ports.S3Port != 0 {
		s3Port, err = allocations.EnsureSpecificGlobalRange(owner, "s3", Range{Base: cluster.Spec.Ports.S3Port, Size: 1}, nodePortClaims)
	} else {
		s3Port, err = allocations.EnsureGlobalRangeWithOffset(owner, "s3", 1, SinglePortsOffset, nodePortClaims)
	}
	if err != nil {
		return err
	}

	cluster.Status.Ports.LbPort = envoyPort.Base
	cluster.Status.Ports.LbAdminPort = envoyAdminPort.Base
	cluster.Status.Ports.S3Port = s3Port.Base

	// Management proxy port is allocated on-demand when the management proxy is first enabled
	// This avoids wasting a port if the feature is not used

	err = t.configStore.UpdateAllocations(ctx, allocations)
	if err != nil {
		return err
	}

	cluster.Status.Ports.PortRange = targetSize
	cluster.Status.Ports.BasePort = targetPort

	return nil
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

// DeallocateCluster removes global cluster port range allocations from ConfigMap.
// Per-container resources (drives, ports) are cleaned up via node annotations
// when containers are deleted (see CleanupClaimsOnNode in funcs_node_claims.go).
func (t *ResourcesAllocator) DeallocateCluster(ctx context.Context, cluster *weka.WekaCluster) error {
	owner := OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace}

	allocations, err := t.configStore.GetAllocations(ctx)
	if err != nil {
		return nil
	}

	// Only clean up global cluster port ranges
	delete(allocations.Global.ClusterRanges, owner)
	delete(allocations.Global.AllocatedRanges, owner)

	err = t.configStore.UpdateAllocations(ctx, allocations)
	if err != nil {
		return err
	}

	return nil
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
// Each instance maintains its own cached view of allocations from the shared ConfigMap.
// The ConfigMapStore handles synchronization through Kubernetes optimistic locking,
// ensuring consistent resource allocation across multiple controller instances.
func GetAllocator(ctx context.Context, client client.Client) (Allocator, error) {
	return newResourcesAllocator(ctx, client)
}
