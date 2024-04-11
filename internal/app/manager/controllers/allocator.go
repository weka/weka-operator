package controllers

import (
	"context"
	"fmt"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"go.opentelemetry.io/otel/codes"
	"slices"
	"strings"
)

const (
	baseAgentPort         = 14101
	baseWekaContainerPort = 16001
	agentPortStep         = 1
	wekaContainerPortStep = 100
)

type Owner struct {
	OwnerCluster
	Container string
	Role      string
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

type NodeAllocations struct {
	Cpu                map[Owner][]int
	Drives             map[Owner][]string
	AgentPorts         map[Owner]int
	WekaContainerPorts map[Owner]int
}

func (n *NodeAllocations) GetFreeDrives(availableDrives []string) []string {
	freeDrives := []string{}
AVAILABLE:
	for _, drive := range availableDrives {
		for _, driveAlloc := range n.Drives {
			if slices.Contains(driveAlloc, drive) {
				continue AVAILABLE
			}
		}
		freeDrives = append(freeDrives, drive)
	}
	return freeDrives
}

func (n *NodeAllocations) NumDriveContainerOwnedByCluster(owner Owner) int {
	count := 0
	for resourceOwner, driveAlloc := range n.Drives {
		if resourceOwner.IsSameOwner(owner) {
			count += len(driveAlloc)
		}
	}
	return count
}

func (n *NodeAllocations) AllocatePort(owner Owner, basePort int, portStep int, portMap map[Owner]int) {
	allocatedList := []int{}
	for _, allocatedPort := range portMap {
		allocatedList = append(allocatedList, allocatedPort)
	}
	slices.Sort(allocatedList)

	scanPosition := 0
OUTER:
	for targetPort := basePort; targetPort < 60000; targetPort += portStep {
		for i, allocatedPort := range allocatedList[scanPosition:] {
			if targetPort == allocatedPort {
				scanPosition = i
				continue OUTER
			}
			if targetPort < allocatedPort {
				break
			}
		}
		portMap[owner] = targetPort
		return
	}
}

func (n *NodeAllocations) NumComputeContainerOwnedByCluster(owner Owner) int {
	count := 0
	for resourceOwner := range n.Cpu {
		if resourceOwner.IsSameOwner(owner) && resourceOwner.Role == "compute" {
			count++
		}
	}
	return count
}

func (n *NodeAllocations) GetFreeCpus(cpus []int) []int {
	freeCpus := []int{}
CPULOOP:
	for _, cpu := range cpus {
		for _, cpuAlloc := range n.Cpu {
			if slices.Contains(cpuAlloc, cpu) {
				continue CPULOOP
			}
		}
		freeCpus = append(freeCpus, cpu)
	}
	return freeCpus
}

func (n *NodeAllocations) AllocateClusterWideRolePort(owner OwnerRole, port int, step int, ports AllocRoleMap) {
	if _, ok := ports[owner]; ok {
		return
	}
	allocatedList := []int{}
	for _, allocatedPort := range ports {
		allocatedList = append(allocatedList, allocatedPort)
	}
	slices.Sort(allocatedList)

	scanPosition := 0
OUTER:
	for targetPort := port; targetPort < 60000; targetPort += step {
		for i, allocatedPort := range allocatedList[scanPosition:] {
			if targetPort == allocatedPort {
				scanPosition = i
				continue OUTER
			}
			if targetPort < allocatedPort {
				break
			}
		}
		ports[owner] = targetPort
		return

	}

}

func contains(alloc []string, searchstring string) bool {
	for _, d := range alloc {
		if d == searchstring {
			return true
		}
	}
	return false
}

type (
	NodeName       string
	AllocationsMap map[NodeName]NodeAllocations
)

type AllocRoleMap map[OwnerRole]int

type GlobalAllocations struct {
	AgentPorts         AllocRoleMap
	WekaContainerPorts AllocRoleMap
}

type Allocations struct {
	Global  GlobalAllocations
	NodeMap AllocationsMap
}

type Allocator struct {
	Logger       instrumentation.LogSpan
	ClusterLevel Topology
}

func NewAllocator(logger instrumentation.LogSpan, clusterConfig Topology) *Allocator {
	return &Allocator{
		Logger:       logger,
		ClusterLevel: clusterConfig,
	}
}

type OwnerCluster struct {
	ClusterName string
	Namespace   string
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

func (a *Allocator) Allocate(ctx context.Context,
	ownerCluster OwnerCluster,
	template ClusterTemplate,
	allocations *Allocations,
	size int, // Size is multiplication of template
) (*Allocations, error, bool) {
	ctx, span := instrumentation.Tracer.Start(ctx, "Allocate")
	defer span.End()
	logger := a.Logger.
		WithName("Allocate").
		WithValues("ownerCluster", ownerCluster, "template", template, "size", size).
		WithValues("trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String())
	if size == 0 {
		size = 1
	}
	if template.MaxFdsPerNode == 0 {
		template.MaxFdsPerNode = 1
	}
	initAllocationsMap(allocations, a.ClusterLevel.Nodes)
	allocationsMap := allocations.NodeMap
	nodes := a.ClusterLevel.Nodes
	slices.SortFunc(nodes, func(i, j string) int {
		iAlloc := allocationsMap[NodeName(i)]
		iDrives := iAlloc.GetFreeDrives(a.ClusterLevel.Drives)
		jAlloc := allocationsMap[NodeName(j)]
		jDrives := jAlloc.GetFreeDrives(a.ClusterLevel.Drives)
		if len(iDrives) < len(jDrives) {
			return 1
		} else if len(iDrives) == len(jDrives) {
			return 0
		} else {
			return -1
		}
	})

	changed := false
	// Code Improvements post existing tests...might need to change even more

	allocateResources := func(role string, numContainers int) error {
	CONTAINERS:
		for i := 0; i < numContainers; i++ {
			containerName := fmt.Sprintf("%s%d", role, i)
			owner := Owner{ownerCluster, containerName, role}
			_, found := GetOwnedResources(owner, allocations)
			if found {
				continue
			}
			for _, node := range nodes {
				nodeAlloc := allocationsMap[NodeName(node)]
				var availableDrives []string
				if role == "drive" {
					availableDrives = nodeAlloc.GetFreeDrives(a.ClusterLevel.Drives)
					if len(availableDrives) < template.NumDrives {
						logger.Info("Not enough drives to allocate request", "role", role, "availableDrives", availableDrives, "template.NumDrives", template.NumDrives, "topology drives", a.ClusterLevel.Drives)
						logger.Info("NodeAlloc", "nodeAlloc", nodeAlloc)
						continue
					}
					if nodeAlloc.NumDriveContainerOwnedByCluster(owner) >= template.MaxFdsPerNode {
						logger.Info("MaxFdsPerNode reached", "role", role, "owner", owner, "template.MaxFdsPerNode", template.MaxFdsPerNode)
						continue
					}
				} else if role == "compute" {
					if nodeAlloc.NumComputeContainerOwnedByCluster(owner) >= template.MaxFdsPerNode {
						logger.Info("MaxFdsPerNode reached", "role", role, "owner", owner, "template.MaxFdsPerNode", template.MaxFdsPerNode)
						continue
					}
					if nodeAlloc.NumDriveContainerOwnedByCluster(owner) < 1 {
						continue
					}
				}
				freeCpus := nodeAlloc.GetFreeCpus(a.ClusterLevel.GetAvailableCpus())
				requiredCpus := template.ComputeCores
				if len(freeCpus) < requiredCpus {
					logger.Info("Not enough CPUs to allocate request", "role", role, "freeCpus", freeCpus, "requiredCpus", requiredCpus)
					continue
				}
				// All looks good, allocating
				changed = true
				if role == "drive" {
					nodeAlloc.Drives[owner] = append(allocationsMap[NodeName(node)].Drives[owner], availableDrives[0:template.NumDrives]...)
				}
				nodeAlloc.Cpu[owner] = append(allocationsMap[NodeName(node)].Cpu[owner], freeCpus[0:requiredCpus]...)
				nodeAlloc.AllocateClusterWideRolePort(owner.ToOwnerRole(), baseAgentPort, agentPortStep, allocations.Global.AgentPorts)
				nodeAlloc.AllocateClusterWideRolePort(owner.ToOwnerRole(), baseWekaContainerPort, wekaContainerPortStep, allocations.Global.WekaContainerPorts)
				continue CONTAINERS
			}
			a.Logger.Info("Not enough resources to allocate request", "role", role, "numContainers", numContainers, "size", size, "template", template, "ownerCluster", ownerCluster, "allocationsMap", allocationsMap, "changed", changed, "containerName", containerName, "owner", owner, "allocMap", allocationsMap)
			return fmt.Errorf("Not enough resources to allocate request")
		}
		return nil
	}

	if err := allocateResources("drive", template.DriveContainers*size); err != nil {
		return allocations, err, changed
	}

	if err := allocateResources("compute", template.ComputeContainers*size); err != nil {
		return allocations, err, changed
	}
	span.SetStatus(codes.Ok, "Allocation successful")
	return allocations, nil, changed
}

func (a *Allocator) DeallocateCluster(cluster OwnerCluster, allocations *Allocations) bool {
	allocMap := allocations.NodeMap
	changed := false
	for _, alloc := range allocMap {
		for owner := range alloc.Cpu {
			if owner.ClusterName == cluster.ClusterName && owner.Namespace == cluster.Namespace {
				changed = true
				delete(alloc.Cpu, owner)
				delete(alloc.Drives, owner)
			}
		}
	}

	for owner := range allocations.Global.AgentPorts {
		if owner.OwnerCluster == cluster {
			changed = true
			delete(allocations.Global.AgentPorts, owner)
		}
	}

	clearRoleAllocations := func(roleAllocations AllocRoleMap) {
		for owner := range roleAllocations {
			if owner.OwnerCluster == cluster {
				changed = true
				delete(roleAllocations, owner)
			}
		}
	}

	clearRoleAllocations(allocations.Global.AgentPorts)
	clearRoleAllocations(allocations.Global.WekaContainerPorts)

	return changed
}

func initAllocationsMap(allocations *Allocations, nodes []string) {
	if allocations == nil {
		panic("allocations is nil")
	}
	if allocations.NodeMap == nil {
		allocations.NodeMap = AllocationsMap{}
	}
	if allocations.Global.AgentPorts == nil {
		allocations.Global.AgentPorts = AllocRoleMap{}
	}
	if allocations.Global.WekaContainerPorts == nil {
		allocations.Global.WekaContainerPorts = AllocRoleMap{}
	}

	for _, node := range nodes {
		if _, ok := allocations.NodeMap[NodeName(node)]; ok {
			continue
		}
		allocations.NodeMap[NodeName(node)] = NodeAllocations{
			Cpu:                map[Owner][]int{},
			Drives:             map[Owner][]string{},
			AgentPorts:         map[Owner]int{},
			WekaContainerPorts: map[Owner]int{},
		}
	}
}

type OwnedResources struct {
	CoreIds   []int
	Drives    []string
	Port      int
	AgentPort int
	Node      string
}

func GetOwnedResources(owner Owner, allocations *Allocations) (OwnedResources, bool) {
	allocMap := allocations.NodeMap
	resources := OwnedResources{}
	for node, alloc := range allocMap {
		if _, ok := alloc.Cpu[owner]; ok {
			resources.Node = string(node)
			resources.CoreIds = append(resources.CoreIds, alloc.Cpu[owner]...)
			resources.Port = alloc.WekaContainerPorts[owner]
			resources.AgentPort = alloc.AgentPorts[owner]
			resources.Drives = append(resources.Drives, alloc.Drives[owner]...)
			break
		}
	}
	if resources.Node == "" {
		return resources, false
	}

	// Assuming that IF we have a node, we have all the resources
	resources.Port = allocations.Global.WekaContainerPorts[owner.ToOwnerRole()]
	resources.AgentPort = allocations.Global.AgentPorts[owner.ToOwnerRole()]

	return resources, true
}

func prettyPrintMap(newMap AllocationsMap) {
	fmt.Println("---start current allocation---")
	for node, alloc := range newMap {
		fmt.Printf("Node: %s\n", node)
		for owner, drives := range alloc.Drives {
			fmt.Printf("  DRIVE: %s:%v\n", owner, drives)
		}
		for owner, cpus := range alloc.Cpu {
			fmt.Printf("  CPU: %s:%v\n", owner, cpus)
		}
		for owner, port := range alloc.AgentPorts {
			fmt.Printf("  PORT_A: %s:%v\n", owner, port)
		}
		for owner, port := range alloc.WekaContainerPorts {
			fmt.Printf("  PORT: %s:%v\n", owner, port)
		}
	}
	fmt.Println("---end current allocation---")
}
