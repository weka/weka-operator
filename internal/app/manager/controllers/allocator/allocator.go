package allocator

import (
	"context"
	"fmt"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"slices"
	"strings"

	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

const (
	SinglePortsOffset = 300
)

type Allocator interface {
	// AllocateClusterPorts allocates ranges only, not specific ports, that is responsibility of WekaCluster controller
	AllocateClusterRange(ctx context.Context, cluster *v1alpha1.WekaCluster) error
	AllocateContainers(ctx context.Context, cluster v1alpha1.WekaCluster, containers []*v1alpha1.WekaContainer) error
	DeallocateCluster(ctx context.Context, cluster v1alpha1.WekaCluster) error
	GetAllocations(ctx context.Context) (*Allocations, error)
	//DeallocateContainers(ctx context.Context, containers []v1alpha1.WekaContainer) error
}

type TopologyAllocator struct {
	Topology    domain.Topology
	configStore AllocationsStore
}

func (t *TopologyAllocator) GetAllocations(ctx context.Context) (*Allocations, error) {
	return t.configStore.GetAllocations(ctx)
}

func (t *TopologyAllocator) AllocateClusterRange(ctx context.Context, cluster *v1alpha1.WekaCluster) error {
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
	targetSize := cluster.Spec.Ports.BasePort

	if targetSize == 0 {
		targetSize = 500
	}

	if targetPort == 0 {
		// if still 0 - lets find a free port
		targetPort, err = allocations.Global.ClusterRanges.GetFreeRange(targetSize)
	}

	allocations.Global.ClusterRanges[owner] = Range{
		Base: targetPort,
		Size: targetSize,
	}

	var envoyPort, envoyAdminPort, s3Port Range

	// allocate envoy, envoys3 and envoyadmin ports and ranges
	if cluster.Spec.Ports.LbPort != 0 {
		envoyPort, err = allocations.EnsureSpecificGlobalRange(owner, "lb", Range{Base: cluster.Spec.Ports.LbPort, Size: 1})
	} else {
		envoyPort, err = allocations.EnsureGlobalRangeWithOffset(owner, "lb", 1, SinglePortsOffset)
	}
	if err != nil {
		return err
	}

	if cluster.Spec.Ports.LbAdminPort != 0 {
		envoyAdminPort, err = allocations.EnsureSpecificGlobalRange(owner, "lbAdmin", Range{Base: cluster.Spec.Ports.LbAdminPort, Size: 1})
	} else {
		envoyAdminPort, err = allocations.EnsureGlobalRangeWithOffset(owner, "lbAdmin", 1, SinglePortsOffset)
	}
	if err != nil {
		return err
	}

	if cluster.Spec.Ports.S3Port != 0 {
		s3Port, err = allocations.EnsureSpecificGlobalRange(owner, "s3", Range{Base: cluster.Spec.Ports.S3Port, Size: 1})
	} else {
		s3Port, err = allocations.EnsureGlobalRangeWithOffset(owner, "s3", 1, SinglePortsOffset)
	}
	if err != nil {
		return err
	}

	cluster.Status.Ports.LbPort = envoyPort.Base
	cluster.Status.Ports.LbAdminPort = envoyAdminPort.Base
	cluster.Status.Ports.S3Port = s3Port.Base

	err = t.configStore.UpdateAllocations(ctx, allocations)
	if err != nil {
		return err
	}

	cluster.Status.Ports.PortRange = targetSize
	cluster.Status.Ports.BasePort = targetPort

	return nil
}

func (t *TopologyAllocator) AllocateContainers(ctx context.Context, cluster v1alpha1.WekaCluster, containers []*v1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "AllocateContainers")
	defer end()

	allocations, err := t.configStore.GetAllocations(ctx)
	if err != nil {
		return nil
	}
	nodeMap := allocations.NodeMap

	ownerCluster := OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace}

	nodes := t.Topology.Nodes
	logger.Info("Allocating resources", "ownerCluster", ownerCluster.ClusterName, "containers", len(containers))
	slices.SortFunc(nodes, func(i, j string) int {
		iAlloc := nodeMap[NodeName(i)]
		iDrives := iAlloc.GetFreeDrives(t.Topology.Drives)
		jAlloc := nodeMap[NodeName(j)]
		jDrives := jAlloc.GetFreeDrives(t.Topology.Drives)
		if len(iDrives) < len(jDrives) {
			return 1
		} else if len(iDrives) == len(jDrives) {
			return 0
		} else {
			return -1
		}
	})

CONTAINERS:
	for _, container := range containers {
		var requiredNics int
		role := container.Spec.Mode
		logger := logger.WithValues("role", role, "name", container.ObjectMeta.Name)

		containerName := container.ObjectMeta.Name
		owner := Owner{ownerCluster, containerName, role}

		if len(t.Topology.Network.EthSlots) > 0 {
			if container.IsWekaContainer() {
				requiredNics = container.Spec.NumCores
			}
		}

		numDrives := 0
		if container.Spec.Mode == v1alpha1.WekaContainerModeDrive {
			numDrives = container.Spec.NumDrives
		}

		requiredCpus := container.Spec.NumCores

		for _, node := range nodes {
			node := NodeName(node)
			if _, ok := nodeMap[node]; !ok {
				nodeAlloc := NodeAllocations{
					AllocatedRanges: make(map[Owner][]Range),
					Cpu:             make(map[Owner][]int),
					Drives:          make(map[Owner][]string),
					EthSlots:        make(map[Owner][]string),
				}
				nodeMap[node] = nodeAlloc
			}
			nodeAlloc := nodeMap[node]
			var availableDrives []string

			if numDrives > 0 {
				availableDrives = nodeAlloc.GetFreeDrives(t.Topology.Drives)
				if len(availableDrives) < numDrives {
					logger.Info("Not enough drives to allocate request", "role", role, "availableDrives", availableDrives)
					continue
				}

				orderedDrives := availableDrives[0:numDrives]
				potentialDrives := t.Topology.GetAllNodesDrives(string(node))
				for i := 0; i < len(potentialDrives); i++ {
					if slices.Contains(orderedDrives, potentialDrives[i]) {
						continue
					}
					orderedDrives = append(potentialDrives, availableDrives[i])
				}
				container.Spec.PotentialDrives = potentialDrives
			}

			maxFdsPerNode := cluster.Spec.MaxFdsPerNode
			if maxFdsPerNode == 0 {
				maxFdsPerNode = 1
			}
			if nodeAlloc.NumRoleContainerOwnedByCluster(owner) >= maxFdsPerNode {
				logger.Info("MaxFdsPerNode reached", "role", role, "owner", owner)
				continue
			}
			if nodeAlloc.NumRoleContainerOwnedByCluster(owner) >= 1 && container.IsHostWideSingleton() {
				logger.Info("Role container already allocated on this node", "role", role, "owner", owner, "node", node)
				continue
			}

			if container.Spec.Mode == v1alpha1.WekaContainerModeS3 {
				if nodeAlloc.NumS3ContainersInTotal() >= t.Topology.MaxS3Containers {
					logger.Info("MaxS3Containers reached", "role", role, "owner", owner)
					continue
				}
			}

			if role == v1alpha1.WekaContainerModeS3 {
				requiredCpus = container.Spec.NumCores + container.Spec.ExtraCores
			}

			//TODO: Node-level CPUs intead of homogeneous topology wide
			freeCpus := nodeAlloc.GetFreeCpus(t.Topology.GetAvailableCpus())
			//TODO: Change CPU alloc logic to explicit HT/Non-HT to cut out container.go level games
			if len(freeCpus) < requiredCpus {
				logger.Info("Not enough CPUs to allocate request", "role", role, "freeCpus", freeCpus, "requiredCpus", requiredCpus)
				continue
			}

			//TODO: Node-level NICs instead of homogeneous topology wide
			if requiredNics > 0 {
				freeNics := nodeAlloc.GetFreeEthSlots(t.Topology.Network.EthSlots)
				if len(freeNics) < requiredNics {
					logger.Info("Not enough NICs to allocate request", "role", role, "freeNics", freeNics, "requiredNics", requiredNics)
					continue
				}
			}

			if container.IsWekaContainer() {
				basePortRange, err := allocations.FindNodeRange(owner, node, 100)
				if err != nil {
					logger.Info("failed to allocate base port range", "error", err)
					continue
				}
				container.Spec.Port = basePortRange.Base
				nodeAlloc.AllocatedRanges[owner] = append(nodeAlloc.AllocatedRanges[owner], Range{Base: container.Spec.Port, Size: 100})

			}
			if container.HasAgent() {
				agentPort, err := allocations.FindNodeRangeWithOffset(owner, node, 1, SinglePortsOffset)
				if err != nil {
					nodeAlloc.DeallocateNodeRange(owner, container.Spec.Port)
					logger.Info("failed to allocate agent port", "error", err, "ranges", allocations.Global.ClusterRanges, "node_ranges", nodeAlloc.AllocatedRanges)
					continue
				}
				container.Spec.AgentPort = agentPort.Base
				nodeAlloc.AllocatedRanges[owner] = append(nodeAlloc.AllocatedRanges[owner], Range{Base: container.Spec.AgentPort, Size: 1})
			}

			if container.Spec.Mode == v1alpha1.WekaContainerModeEnvoy {
				container.Spec.Port = cluster.Status.Ports.LbPort
			}

			// All looks good, allocating
			//changed = true

			if requiredNics > 0 {
				freeNics := nodeAlloc.GetFreeEthSlots(t.Topology.Network.EthSlots)
				nodeAlloc.EthSlots[owner] = append(nodeAlloc.EthSlots[owner], freeNics[0:requiredNics]...)
				container.Spec.Network.EthDevices = freeNics[0:requiredNics]
			}

			if numDrives > 0 {
				nodeAlloc.Drives[owner] = append(nodeMap[node].Drives[owner], availableDrives[0:numDrives]...)
			}

			nodeAlloc.Cpu[owner] = append(nodeMap[node].Cpu[owner], freeCpus[0:requiredCpus]...)

			if container.Spec.CpuPolicy == "shared" {
				container.Spec.CoreIds = freeCpus[0:requiredCpus]
			}
			container.Spec.NodeAffinity = string(node)

			if t.Topology.NodeConfigMapPattern != "" {
				container.Spec.NodeInfoConfigMap = fmt.Sprintf(t.Topology.NodeConfigMapPattern, node)
			}

			continue CONTAINERS
		}
		logger.Info("Not enough resources to allocate request")
		return fmt.Errorf("Not enough resources to allocate request")
	}

	err = t.configStore.UpdateAllocations(ctx, allocations)
	if err != nil {
		return err
	}

	return nil
}

func (t *TopologyAllocator) DeallocateCluster(ctx context.Context, cluster v1alpha1.WekaCluster) error {
	owner := OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace}

	allocations, err := t.configStore.GetAllocations(ctx)
	if err != nil {
		return nil
	}

	if _, ok := allocations.Global.ClusterRanges[owner]; ok {
		delete(allocations.Global.ClusterRanges, owner)
	}

	if _, ok := allocations.Global.AllocatedRanges[owner]; ok {
		delete(allocations.Global.AllocatedRanges, owner)
	}

	// clear nodes as well
	for _, nodeAlloc := range allocations.NodeMap {
		for allocOwner, _ := range nodeAlloc.Cpu {
			if allocOwner.OwnerCluster == owner {
				delete(nodeAlloc.Cpu, allocOwner)
			}
		}
		for allocOwner, _ := range nodeAlloc.Drives {
			if allocOwner.OwnerCluster == owner {
				delete(nodeAlloc.Drives, allocOwner)
			}
		}
		for allocOwner, _ := range nodeAlloc.EthSlots {
			if allocOwner.OwnerCluster == owner {
				delete(nodeAlloc.EthSlots, allocOwner)
			}
		}

		for allocOwner, _ := range nodeAlloc.AllocatedRanges {
			if allocOwner.OwnerCluster == owner {
				delete(nodeAlloc.AllocatedRanges, allocOwner)
			}
		}
	}

	err = t.configStore.UpdateAllocations(ctx, allocations)
	if err != nil {
		return err
	}

	return nil
}

func NewTopologyAllocator(ctx context.Context, client client.Client, topology domain.Topology) (Allocator, error) {
	cs, err := NewConfigMapStore(ctx, client)
	if err != nil {
		return nil, err
	}
	return &TopologyAllocator{
		Topology:    topology,
		configStore: cs,
	}, nil
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

func DeallocateNamespacedObject(ctx context.Context, namespacedObject NamespacedObject, store AllocationsStore) error {
	allocations, err := store.GetAllocations(ctx)
	if err != nil {
		return err
	}

	ownersToDelete := make([]Owner, 0)

	for _, nodeAlloc := range allocations.NodeMap {
		for owner, _ := range nodeAlloc.Cpu {
			if owner.ToNamespacedObject() == namespacedObject {
				ownersToDelete = append(ownersToDelete, owner)
			}
		}
		for owner, _ := range nodeAlloc.AllocatedRanges {
			if owner.ToNamespacedObject() == namespacedObject {
				ownersToDelete = append(ownersToDelete, owner)
			}
		}

		for owner, _ := range nodeAlloc.EthSlots {
			if owner.ToNamespacedObject() == namespacedObject {
				ownersToDelete = append(ownersToDelete, owner)
			}
		}

		for owner, _ := range nodeAlloc.Drives {
			if owner.ToNamespacedObject() == namespacedObject {
				ownersToDelete = append(ownersToDelete, owner)
			}
		}

	}

	for _, owner := range ownersToDelete {
		for _, nodeAlloc := range allocations.NodeMap {
			delete(nodeAlloc.Cpu, owner)
			delete(nodeAlloc.AllocatedRanges, owner)
			delete(nodeAlloc.EthSlots, owner)
			delete(nodeAlloc.Drives, owner)
		}
	}

	err = store.UpdateAllocations(ctx, allocations)
	if err != nil {
		return err
	}

	return nil
}

func DeallocateContainer(ctx context.Context, container v1alpha1.WekaContainer, client2 client.Client) error {
	allocationsStore, err := NewConfigMapStore(ctx, client2)
	if err != nil {
		return err
	}

	namespacedObject := NamespacedObject{
		Namespace: container.Namespace,
		Name:      container.Name,
	}

	return DeallocateNamespacedObject(ctx, namespacedObject, allocationsStore)
}
