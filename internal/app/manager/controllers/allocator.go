package controllers

import (
	"fmt"
	"github.com/go-logr/logr"
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
		if resourceOwner.IsSameClusterAndRole(owner) {
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
	for resourceOwner, _ := range n.Cpu {
		if resourceOwner.IsSameClusterAndRole(owner) {
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

func contains(alloc []string, searchstring string) bool {
	for _, d := range alloc {
		if d == searchstring {
			return true
		}
	}
	return false
}

type NodeName string
type AllocationsMap map[NodeName]NodeAllocations

type Allocator struct {
	Logger       logr.Logger
	ClusterLevel Topology
}

func NewAllocator(logger logr.Logger, clusterConfig Topology) *Allocator {
	return &Allocator{
		Logger:       logger,
		ClusterLevel: clusterConfig,
	}
}

type OwnerCluster struct {
	ClusterName string
	Namespace   string
}

func (a *Allocator) Allocate(
	ownerCluster OwnerCluster,
	template ClusterTemplate,
	allocationsMap AllocationsMap,
	size int, // Size is multiplication of template
) (AllocationsMap, error, bool) {
	if size == 0 {
		size = 1
	}
	if template.MaxFdsPerNode == 0 {
		template.MaxFdsPerNode = 1
	}
	initAllocationsMap(allocationsMap, a.ClusterLevel.Nodes)

	changed := false
	// Code Improvements post existing tests...might need to change even more

	allocateResources := func(role string, numContainers int) error {
	CONTAINERS:
		for i := 0; i < numContainers; i++ {
			containerName := fmt.Sprintf("%s%d", role, i)
			owner := Owner{ownerCluster, containerName, role}
			_, found := GetOwnedResources(owner, allocationsMap)
			if found {
				continue
			}
			for _, node := range a.ClusterLevel.Nodes {
				nodeAlloc := allocationsMap[NodeName(node)]
				var availableDrives []string
				if role == "drive" {
					availableDrives = nodeAlloc.GetFreeDrives(a.ClusterLevel.Drives)
					if len(availableDrives) < template.NumDrives {
						continue
					}
					if nodeAlloc.NumDriveContainerOwnedByCluster(owner) >= template.MaxFdsPerNode {
						continue
					}
				} else {
					if nodeAlloc.NumComputeContainerOwnedByCluster(owner) >= template.MaxFdsPerNode {
						continue
					}
				}
				freeCpus := nodeAlloc.GetFreeCpus(a.ClusterLevel.GetAvailableCpus())
				requiredCpus := template.ComputeCores
				if len(freeCpus) < requiredCpus {
					continue
				}
				// All looks good, allocating
				changed = true
				if role == "drive" {
					nodeAlloc.Drives[owner] = append(allocationsMap[NodeName(node)].Drives[owner], availableDrives[0:template.NumDrives]...)
				}
				nodeAlloc.Cpu[owner] = append(allocationsMap[NodeName(node)].Cpu[owner], freeCpus[0:requiredCpus]...)
				nodeAlloc.AllocatePort(owner, baseAgentPort, agentPortStep, allocationsMap[NodeName(node)].AgentPorts)
				nodeAlloc.AllocatePort(owner, baseWekaContainerPort, wekaContainerPortStep, allocationsMap[NodeName(node)].WekaContainerPorts)
				continue CONTAINERS
			}
			a.Logger.Info("Not enough resources to allocate request", "role", role, "numContainers", numContainers, "size", size, "template", template, "ownerCluster", ownerCluster, "allocationsMap", allocationsMap, "changed", changed, "containerName", containerName, "owner", owner, "allocMap", allocationsMap)
			return fmt.Errorf("Not enough resources to allocate request")
		}
		return nil
	}

	if err := allocateResources("drive", template.DriveContainers*size); err != nil {
		return allocationsMap, err, changed
	}

	if err := allocateResources("compute", template.ComputeContainers*size); err != nil {
		return allocationsMap, err, changed

	}
	return allocationsMap, nil, changed
}

func (a *Allocator) DeallocateCluster(cluster OwnerCluster, allocMap AllocationsMap) bool {
	changed := false
	for _, alloc := range allocMap {
		for owner, _ := range alloc.Cpu {
			if owner.ClusterName == cluster.ClusterName && owner.Namespace == cluster.Namespace {
				changed = true
				delete(alloc.Cpu, owner)
				delete(alloc.Drives, owner)
				delete(alloc.AgentPorts, owner)
				delete(alloc.WekaContainerPorts, owner)
			}
		}
	}
	return changed
}

func initAllocationsMap(allocationsMap AllocationsMap, nodes []string) {
	for _, node := range nodes {
		if _, ok := allocationsMap[NodeName(node)]; ok {
			continue
		}
		allocationsMap[NodeName(node)] = NodeAllocations{
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

func GetOwnedResources(owner Owner, allocMap AllocationsMap) (OwnedResources, bool) {
	resources := OwnedResources{}
	for node, alloc := range allocMap {
		if _, ok := alloc.Cpu[owner]; ok {
			resources.Node = string(node)
			resources.CoreIds = append(resources.CoreIds, alloc.Cpu[owner]...)
			resources.Port = alloc.WekaContainerPorts[owner]
			resources.AgentPort = alloc.AgentPorts[owner]
			resources.Drives = append(resources.Drives, alloc.Drives[owner]...)
			return resources, true
		}
	}
	return resources, false
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
