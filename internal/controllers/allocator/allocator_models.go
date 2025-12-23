package allocator

import (
	"k8s.io/apimachinery/pkg/types"
)

const StartingPort = 35000
const MaxPort = 65535

type AllocRoleMap map[OwnerRole]int

// Allocations now only contains global cluster port ranges.
// Per-container resources (drives, ports) are tracked via node annotations (NodeClaims).
type Allocations struct {
	Global GlobalAllocations
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

func (o Owner) ToNamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: o.Namespace,
		Name:      o.Container,
	}
}
