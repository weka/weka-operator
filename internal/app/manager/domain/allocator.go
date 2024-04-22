package domain

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"go.opentelemetry.io/otel/codes"
)

const (
	baseAgentPort         = 14101
	baseEnvoyPort         = 13101
	baseEnvoyAdminPort    = 12101
	baseS3Port            = 11101
	baseWekaContainerPort = 16001
	singlePortStep        = 1
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
	EnvoyPorts         map[Owner]int
	EnvoyAdminPorts    map[Owner]int
	S3Ports            map[Owner]int
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

func (n *NodeAllocations) NumS3ContainersInTotal() int {
	count := 0
	for owner := range n.Cpu {
		if owner.Role == "s3" {
			count += 1
		}
	}
	return count
}

func (n *NodeAllocations) NumS3ContainersOwnedByCluster(owner Owner) int {
	count := 0
	for resourceOwner := range n.Cpu {
		if resourceOwner.IsSameOwner(owner) && resourceOwner.Role == "s3" {
			count++
		}
	}
	return count
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
	EnvoyPorts         AllocRoleMap
	EnvoyAdminPorts    AllocRoleMap
	S3Ports            AllocRoleMap
	WekaContainerPorts AllocRoleMap
}

type Allocations struct {
	Global  GlobalAllocations
	NodeMap AllocationsMap
}

type Allocator struct {
	ClusterLevel Topology
}

func NewAllocator(clusterConfig Topology) *Allocator {
	return &Allocator{
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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "Allocate")
	defer end()

	if size == 0 {
		size = 1
	}
	if template.MaxFdsPerNode == 0 {
		template.MaxFdsPerNode = 1
	}
	initAllocationsMap(allocations, a.ClusterLevel.Nodes)
	allocationsMap := allocations.NodeMap
	nodes := a.ClusterLevel.Nodes
	logger.Info("Allocating resources", "ownerCluster", ownerCluster.ClusterName, "size", size, "nodes", len(nodes))
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
	lastAllocFailureReason := ""

	allocateResources := func(role string, numContainers int) error {
	CONTAINERS:
		for i := 0; i < numContainers; i++ {
			logger := logger.WithValues("role", role, "numContainers", numContainers)
			containerName := fmt.Sprintf("%s%d", role, i)
			owner := Owner{ownerCluster, containerName, role}
			resources, found := GetOwnedResources(owner, allocations)
			if found {
				// Does node still exist? If not, lets clean this allocation and look for another node
				if !slices.Contains(nodes, resources.Node) {
					logger.Info("Node does not exist anymore", "node", resources.Node)
					delete(allocationsMap, NodeName(resources.Node))
				} else {
					continue
				}
			}
			for _, node := range nodes {
				nodeAlloc := allocationsMap[NodeName(node)]
				var availableDrives []string
				var requiredCpus int
				if role == "drive" {
					availableDrives = nodeAlloc.GetFreeDrives(a.ClusterLevel.Drives)
					if len(availableDrives) < template.NumDrives {
						logger.Info("Not enough drives to allocate request", "role", role, "availableDrives", availableDrives, "template.NumDrives", template.NumDrives, "topology drives", a.ClusterLevel.Drives)
						logger.Info("NodeAlloc", "nodeAlloc", nodeAlloc)
						continue
					}
					if nodeAlloc.NumDriveContainerOwnedByCluster(owner) >= template.MaxFdsPerNode {
						// logger.Info("MaxFdsPerNode reached", "role", role, "owner", owner, "template.MaxFdsPerNode", template.MaxFdsPerNode)
						continue
					}
					requiredCpus = template.DriveCores
				} else if role == "compute" {
					if nodeAlloc.NumComputeContainerOwnedByCluster(owner) >= template.MaxFdsPerNode {
						// logger.Info("MaxFdsPerNode reached", "role", role, "owner", owner, "template.MaxFdsPerNode", template.MaxFdsPerNode)
						continue
					}
					if nodeAlloc.NumDriveContainerOwnedByCluster(owner) < 1 {
						continue
					}
					requiredCpus = template.ComputeCores
				} else if role == "s3" {
					if nodeAlloc.NumDriveContainerOwnedByCluster(owner) < 1 { // not allocating on nodes that do not host same-tenant drive
						lastAllocFailureReason = "No drive container on host"
						continue
					}
					if nodeAlloc.NumS3ContainersInTotal() >= a.ClusterLevel.MaxS3Containers {
						lastAllocFailureReason = "MaxS3Containers reached"
						continue
					}
					if nodeAlloc.NumS3ContainersOwnedByCluster(owner) >= 1 {
						lastAllocFailureReason = "S3 container already allocated"
						continue // no more then one s3 container on host
					}
					requiredCpus = template.S3Cores + template.S3ExtraCores
				}
				freeCpus := nodeAlloc.GetFreeCpus(a.ClusterLevel.GetAvailableCpus())
				if len(freeCpus) < requiredCpus {
					logger.Info("Not enough CPUs to allocate request", "role", role, "freeCpus", freeCpus, "requiredCpus", requiredCpus)
					lastAllocFailureReason = "Not enough CPUs"
					continue
				}
				// All looks good, allocating
				changed = true
				if role == "drive" {
					nodeAlloc.Drives[owner] = append(allocationsMap[NodeName(node)].Drives[owner], availableDrives[0:template.NumDrives]...)
				}
				nodeAlloc.Cpu[owner] = append(allocationsMap[NodeName(node)].Cpu[owner], freeCpus[0:requiredCpus]...)
				// TODO: Unite agent, envoy, envoy admin and s3 into unified range/allocmap distringuished by OwnerRole. Current Gap - all three s3 ports represent single role
				nodeAlloc.AllocateClusterWideRolePort(owner.ToOwnerRole(), baseAgentPort, singlePortStep, allocations.Global.AgentPorts)
				nodeAlloc.AllocateClusterWideRolePort(owner.ToOwnerRole(), baseWekaContainerPort, wekaContainerPortStep, allocations.Global.WekaContainerPorts)
				if role == "s3" {
					nodeAlloc.AllocateClusterWideRolePort(owner.ToOwnerRole(), baseEnvoyPort, singlePortStep, allocations.Global.EnvoyPorts)
					nodeAlloc.AllocateClusterWideRolePort(owner.ToOwnerRole(), baseEnvoyAdminPort, singlePortStep, allocations.Global.EnvoyAdminPorts)
					nodeAlloc.AllocateClusterWideRolePort(owner.ToOwnerRole(), baseS3Port, singlePortStep, allocations.Global.S3Ports)
				}
				continue CONTAINERS
			}
			logger.Info("Not enough resources to allocate request", "role", role, "numContainers", numContainers, "size", size, "template", template, "ownerCluster", ownerCluster, "changed", changed, "containerName", containerName, "owner", owner, "lastAllocFailureReason", lastAllocFailureReason)
			return fmt.Errorf("Not enough resources to allocate request")
		}
		logger.Info("Exit")
		return nil
	}

	if err := allocateResources("drive", template.DriveContainers*size); err != nil {
		logger.Error(err, "Failed to allocate drive containers")
		return allocations, err, changed
	}

	if err := allocateResources("compute", template.ComputeContainers*size); err != nil {
		logger.Error(err, "Failed to allocate compute containers")
		return allocations, err, changed
	}

	if err := allocateResources("s3", template.S3Containers*size); err != nil {
		logger.Error(err, "Failed to allocate S3 containers")
		return allocations, err, changed
	}

	logger.SetStatus(codes.Ok, "resources allocated")
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

	clearRoleAllocations := func(roleAllocations AllocRoleMap) {
		for owner := range roleAllocations {
			if owner.OwnerCluster == cluster {
				changed = true
				delete(roleAllocations, owner)
			}
		}
	}

	clearRoleAllocations(allocations.Global.AgentPorts)
	clearRoleAllocations(allocations.Global.S3Ports)
	clearRoleAllocations(allocations.Global.EnvoyPorts)
	clearRoleAllocations(allocations.Global.EnvoyAdminPorts)
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
	if allocations.Global.S3Ports == nil {
		allocations.Global.S3Ports = AllocRoleMap{}
	}
	if allocations.Global.EnvoyPorts == nil {
		allocations.Global.EnvoyPorts = AllocRoleMap{}
	}
	if allocations.Global.EnvoyAdminPorts == nil {
		allocations.Global.EnvoyAdminPorts = AllocRoleMap{}
	}

	for _, node := range nodes {
		if _, ok := allocations.NodeMap[NodeName(node)]; ok {
			continue
		}
		allocations.NodeMap[NodeName(node)] = NodeAllocations{
			Cpu:                map[Owner][]int{},
			Drives:             map[Owner][]string{},
			AgentPorts:         map[Owner]int{},
			EnvoyPorts:         map[Owner]int{},
			EnvoyAdminPorts:    map[Owner]int{},
			S3Ports:            map[Owner]int{},
			WekaContainerPorts: map[Owner]int{},
		}
	}
}

type OwnedResources struct {
	CoreIds        []int
	Drives         []string
	Port           int
	AgentPort      int
	S3Port         int
	EnvoyPort      int
	EnvoyAdminPort int
	Node           string
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
			if owner.Role == "s3" {
				resources.S3Port = alloc.S3Ports[owner]
				resources.EnvoyPort = alloc.EnvoyPorts[owner]
				resources.EnvoyAdminPort = alloc.EnvoyAdminPorts[owner]
			}
			break
		}
	}
	if resources.Node == "" {
		return resources, false
	}

	// Assuming that IF we have a node, we have all the resources, this might become wrong going forward
	resources.Port = allocations.Global.WekaContainerPorts[owner.ToOwnerRole()]
	resources.AgentPort = allocations.Global.AgentPorts[owner.ToOwnerRole()]
	resources.S3Port = allocations.Global.S3Ports[owner.ToOwnerRole()]
	resources.EnvoyPort = allocations.Global.EnvoyPorts[owner.ToOwnerRole()]
	resources.EnvoyAdminPort = allocations.Global.EnvoyAdminPorts[owner.ToOwnerRole()]

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
