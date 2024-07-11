package allocator

const StartingPort = 15000
const MaxPort = 65535

type (
	NodeName     string
	NodeAllocMap map[NodeName]NodeAllocations
)

type AllocRoleMap map[OwnerRole]int

type Allocations struct {
	Global  GlobalAllocations
	NodeMap NodeAllocMap
}

type Range struct {
	Base int
	Size int
}

type OwnerCluster struct {
	ClusterName string
	Namespace   string
}

type Owner struct {
	OwnerCluster
	Container string
	Role      string
}

type ClusterRanges map[OwnerCluster]Range

type GlobalAllocations struct {
	ClusterRanges   ClusterRanges
	AllocatedRanges map[OwnerCluster]map[string]Range
}

// different cases will allocate either into global ranges, or into per-node ranges

type NodeAllocations struct {
	AllocatedRanges map[Owner][]Range
	Cpu             map[Owner][]int
	Drives          map[Owner][]string
	EthSlots        map[Owner][]string
}

type NamespacedObject struct {
	Namespace string
	Name      string
}

func (o Owner) ToNamespacedObject() NamespacedObject {
	return NamespacedObject{
		Namespace: o.Namespace,
		Name:      o.Container,
	}
}
