package allocator

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/pkg/util"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	SinglePortsOffset = 300
)

type Allocator interface {
	// AllocateClusterPorts allocates ranges only, not specific ports, that is responsibility of WekaCluster controller
	AllocateClusterRange(ctx context.Context, cluster *weka.WekaCluster) error
	AllocateContainers(ctx context.Context, cluster *weka.WekaCluster, containers []*weka.WekaContainer) error
	//ClaimResources(ctx context.Context, cluster v1alpha1.WekaCluster, containers []*v1alpha1.WekaContainer) error
	DeallocateCluster(ctx context.Context, cluster *weka.WekaCluster) error
	GetAllocations(ctx context.Context) (*Allocations, error)
	//DeallocateContainers(ctx context.Context, containers []v1alpha1.WekaContainer) error
}

type AllocatorNodeInfo struct {
	AvailableDrives []string
}

type NodeInfoGetter func(ctx context.Context, nodeName weka.NodeName) (*AllocatorNodeInfo, error)

type ResourcesAllocator struct {
	configStore    AllocationsStore
	client         client.Client
	nodeInfoGetter NodeInfoGetter
}

func NewK8sNodeInfoGetter(k8sClient client.Client) NodeInfoGetter {
	return func(ctx context.Context, nodeName weka.NodeName) (nodeInfo *AllocatorNodeInfo, err error) {
		node := &v1.Node{}
		err = k8sClient.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node)
		if err != nil {
			return
		}

		nodeInfo = &AllocatorNodeInfo{}

		// get from annotations, all serial ids minus blocked-drives serial ids
		allDrivesStr, ok := node.Annotations["weka.io/weka-drives"]
		if !ok {
			nodeInfo.AvailableDrives = []string{}
			return
		}
		blockedDrivesStr, ok := node.Annotations["weka.io/blocked-drives"]
		if !ok {
			blockedDrivesStr = "[]"
		}
		//blockedDrivesStr  is json list, unwrap it
		blockedDrives := []string{}
		_ = json.Unmarshal([]byte(blockedDrivesStr), &blockedDrives)

		availableDrives := []string{}
		allDrives := []string{}
		_ = json.Unmarshal([]byte(allDrivesStr), &allDrives)

		for _, drive := range allDrives {
			if !slices.Contains(blockedDrives, drive) {
				availableDrives = append(allDrives, drive)
			}
		}

		nodeInfo.AvailableDrives = availableDrives
		return
	}
}

func (t *ResourcesAllocator) GetAllocations(ctx context.Context) (*Allocations, error) {
	return t.configStore.GetAllocations(ctx)
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

func (t *ResourcesAllocator) GetNode(ctx context.Context, nodeName weka.NodeName) (*v1.Node, error) {
	node := &v1.Node{}
	err := t.client.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node)
	if err != nil {
		return nil, err
	}
	return node, nil
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

func (t *ResourcesAllocator) AllocateContainers(ctx context.Context, cluster *weka.WekaCluster, containers []*weka.WekaContainer) error {
	// At this point containers are landed on specific node, so we dont check what which node to use, we only allocate some resources out of the node to let container know what it should be using
	// Alternative thoughts:
	// - drive _could_ just find for free drive
	// - port-wise, more difficult. we cant just take port, we could share nodemap locally? and acquire from there?
	// for now going for this way, as very close to original implementation...

	//TODO: Re-Do this as operation, as this also clearly have failable and conditional steps

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "AllocateContainers")
	defer end()

	changed := false

	allocations, err := t.configStore.GetAllocations(ctx)
	if err != nil {
		return nil
	}
	nodeMap := allocations.NodeMap
	ownerCluster := OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace}
	//
	//// TODO:If not pre-populated can cause fetches for each node making it serial and impossibly slow on huge clusters
	failedAllocations := FailedAllocations{}

	for _, container := range containers {
		if container.Status.Allocations != nil {
			logger.Info("Container already allocated", "name", container.ObjectMeta.Name)
			continue
		}
		revertFuncs := []func(){}
		revert := func(err error, container *weka.WekaContainer) {
			for _, f := range revertFuncs {
				f()
			}
			logger.Error(err, "Reverted allocation", "container", container.Name)
			failedAllocations = append(failedAllocations, AllocationFailure{Err: err, Container: container})
		}
		//var requiredNics int
		role := container.Spec.Mode
		logger := logger.WithValues("role", role, "name", container.ObjectMeta.Name)

		owner := Owner{OwnerCluster: ownerCluster, Container: container.Name, Role: role}

		nodeName := container.GetNodeAffinity()
		nodeInfo, err := t.nodeInfoGetter(ctx, nodeName)
		if err != nil {
			logger.Info("Failed to get node", "error", err)
			revert(err, container)
			continue
		}

		if _, ok := nodeMap[nodeName]; !ok {
			nodeAlloc := NodeAllocations{
				AllocatedRanges: make(map[Owner]map[string]Range),
				Drives:          make(map[Owner][]string),
				EthSlots:        make(map[Owner][]string),
			}
			nodeMap[nodeName] = nodeAlloc
		}
		nodeAlloc := nodeMap[nodeName]

		if container.Status.Allocations == nil {
			container.Status.Allocations = &weka.ContainerAllocations{}
		}
		if nodeAlloc.AllocatedRanges[owner] == nil {
			nodeAlloc.AllocatedRanges[owner] = make(map[string]Range)
		}

		if container.Spec.NumDrives > 0 {
			if nodeAlloc.Drives[owner] == nil || len(nodeAlloc.Drives[owner]) == 0 {
				allDrives := nodeInfo.AvailableDrives
				if err != nil {
					logger.Info("Failed to get node", "error", err)
					revert(err, container)
					continue
				}
				if len(allDrives) < container.Spec.NumDrives {
					logger.Info("Not enough drives to allocate request even on fully free node", "role", role, "totalDrives", len(allDrives), "requiredDrives", container.Spec.NumDrives)
					revert(fmt.Errorf("Not enough drives to allocate request"), container)
					continue
				}

				//TODO: Merge above check with below, they are doing same, but below might have a bug
				allocatedDrives := nodeAlloc.allocateDrives(owner, container.Spec.NumDrives, allDrives)
				if allocatedDrives == nil {
					logger.Info("Not enough drives free to allocate request", "role", role, "totalDrives", len(allDrives), "requiredDrives", container.Spec.NumDrives)
					revert(fmt.Errorf("Not enough drives to allocate request"), container)
					continue
				}
				revertFuncs = append(revertFuncs, func() {
					nodeAlloc.deallocateDrives(owner)
				})
				container.Status.Allocations.Drives = allocatedDrives
			} else {
				container.Status.Allocations.Drives = nodeAlloc.Drives[owner]
			}
		}

		baseWekaPort := 0
		if container.IsWekaContainer() {
			if currentRanges, ok := nodeAlloc.AllocatedRanges[owner]; !ok || currentRanges["weka"].Base == 0 {
				basePortRange, err := allocations.FindNodeRange(owner, nodeName, 100)
				if err != nil {
					logger.Info("failed to allocate base port range", "error", err)
					revert(err, container)
					continue
				}
				baseWekaPort = basePortRange.Base
				container.Status.Allocations.WekaPort = baseWekaPort
				nodeAlloc.AllocatedRanges[owner]["weka"] = Range{Base: baseWekaPort, Size: 100}
				revertFuncs = append(revertFuncs, func() {
					nodeAlloc.DeallocateNodeRange(owner, baseWekaPort)
				})
			} else {
				container.Status.Allocations.WekaPort = currentRanges["weka"].Base
			}
		}
		if container.HasAgent() {
			if currentRanges, ok := nodeAlloc.AllocatedRanges[owner]; !ok || currentRanges["agent"].Base == 0 {
				agentPort, err := allocations.FindNodeRangeWithOffset(owner, nodeName, 1, SinglePortsOffset)
				if err != nil {
					if baseWekaPort != 0 {
						//TODO: If we reverting here, why we dont revert drives? we probably should? or drop reverting altogether. How? We want to commit all containers at once
						logger.Info("failed to allocate agent port", "error", err, "ranges", allocations.Global.ClusterRanges, "node_ranges", nodeAlloc.AllocatedRanges)
						revert(err, container)
						continue
					}
				}
				container.Status.Allocations.AgentPort = agentPort.Base
				nodeAlloc.AllocatedRanges[owner]["agent"] = Range{Base: agentPort.Base, Size: 1}
			} else {
				container.Status.Allocations.AgentPort = currentRanges["agent"].Base
			}
		}

		//if container.Spec.Mode == weka.WekaContainerModeEnvoy {
		// Strange datab modeling, ignoring for now
		//	container.Spec.Port = cluster.Status.Ports.LbPort
		//}

		//All looks good, allocating
		changed = true

		//TODO: MaxFD not supported, will need to rely on admission controller or custom scheduler
		//TODO: Prioritizing nodes not supported, will need to rely on custom scheduler
		//TODO: (doable) Maximum number of s3 pods not supported, can do with just custom resource on nodes
		//TODO: (doable) EthSlot support removed right now as it was proxied via topology which is dropped, need to integrate into node info

	}

	if changed {
		err = t.configStore.UpdateAllocations(ctx, allocations)
		if err != nil {
			return err
		}
	}

	//	nodeAlloc := nodeMap[node]
	//
	//	maxFdsPerNode := cluster.Spec.MaxFdsPerNode
	//	if maxFdsPerNode == 0 {
	//		maxFdsPerNode = 1
	//	}
	//	if nodeAlloc.NumRoleContainerOwnedByCluster(owner) >= maxFdsPerNode {
	//		logger.Info("MaxFdsPerNode reached", "role", role, "owner", owner)
	//		continue
	//	}
	//	if nodeAlloc.NumRoleContainerOwnedByCluster(owner) >= 1 && container.IsHostWideSingleton() {
	//		logger.Info("Role container already allocated on this node", "role", role, "owner", owner, "node", node)
	//		continue
	//	}
	//
	//	if container.Spec.Mode == v1alpha1.WekaContainerModeS3 {
	//		if nodeAlloc.NumS3ContainersInTotal() >= t.Topology.MaxS3Containers {
	//			logger.Info("MaxS3Containers reached", "role", role, "owner", owner)
	//			continue
	//		}
	//	}
	//
	//	if role == v1alpha1.WekaContainerModeS3 {
	//		requiredCpus = container.Spec.NumCores + container.Spec.ExtraCores
	//	}
	//
	//	availableCpus, err := t.Topology.GetAvailableCpus(ctx, node)
	//	if err != nil {
	//		logger.Info("Failed to get available CPUs", "error", err)
	//		continue
	//	}
	//	freeCpus := availableCpus - nodeAlloc.GetUsedCpuCount()
	//	if freeCpus < requiredCpus {
	//		logger.Info("Not enough CPUs to allocate request", "role", role, "availableCpus", availableCpus, "freeCpus", freeCpus, "requiredCpus", requiredCpus)
	//		continue
	//	}
	//
	//	//TODO: Node-level NICs instead of homogeneous topology wide
	//	if requiredNics > 0 {
	//		freeNics := nodeAlloc.GetFreeEthSlots(t.Topology.Network.EthSlots)
	//		if len(freeNics) < requiredNics {
	//			logger.Info("Not enough NICs to allocate request", "role", role, "freeNics", freeNics, "requiredNics", requiredNics)
	//			continue
	//		}
	//	}
	//
	//
	//	if requiredNics > 0 {
	//		freeNics := nodeAlloc.GetFreeEthSlots(t.Topology.Network.EthSlots)
	//		nodeAlloc.EthSlots[owner] = append(nodeAlloc.EthSlots[owner], freeNics[0:requiredNics]...)
	//		container.Spec.Network.EthDevices = freeNics[0:requiredNics]
	//	}
	//
	//	if numDrives > 0 {
	//		nodeAlloc.DriveCount[owner] += numDrives
	//	}
	//
	//	nodeAlloc.CpuCount[owner] += requiredCpus
	//
	//	if container.Spec.CpuPolicy == "shared" {
	//		container.Spec.NumCores = requiredCpus
	//	}
	//	container.Spec.NodeAffinity = node
	//
	//	if t.Topology.NodeConfigMapPattern != "" {
	//		container.Spec.NodeInfoConfigMap = fmt.Sprintf(t.Topology.NodeConfigMapPattern, node)
	//	}
	//
	//	continue CONTAINERS
	//}
	//	logger.Info("Not enough resources to allocate request")
	//	return fmt.Errorf("Not enough resources to allocate request")
	////}
	//

	if len(failedAllocations) > 0 {
		return &failedAllocations
	}

	return nil
}

func (t *ResourcesAllocator) DeallocateCluster(ctx context.Context, cluster *weka.WekaCluster) error {
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

func NewResourcesAllocator(ctx context.Context, client client.Client) (Allocator, error) {
	cs, err := NewConfigMapStore(ctx, client)
	if err != nil {
		return nil, err
	}

	return &ResourcesAllocator{
		configStore:    cs,
		client:         client,
		nodeInfoGetter: NewK8sNodeInfoGetter(client),
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

func DeallocateNamespacedObject(ctx context.Context, namespacedObject util.NamespacedObject, store AllocationsStore) error {
	allocations, err := store.GetAllocations(ctx)
	if err != nil {
		return err
	}

	ownersToDelete := make([]Owner, 0)

	for _, nodeAlloc := range allocations.NodeMap {
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

var lock = &sync.Mutex{}

func DeallocateContainer(ctx context.Context, container *weka.WekaContainer, client2 client.Client) error {
	if !container.IsAllocatable() {
		return nil
	}
	lock.Lock()
	defer lock.Unlock()
	allocationsStore, err := NewConfigMapStore(ctx, client2)
	if err != nil {
		return err
	}

	namespacedObject := util.NamespacedObject{
		Namespace: container.Namespace,
		Name:      container.Name,
	}

	return DeallocateNamespacedObject(ctx, namespacedObject, allocationsStore)
}

func NewContainerName(role string) string {
	guid := string(uuid.NewUUID())
	return fmt.Sprintf("%s-%s", role, guid)
}
