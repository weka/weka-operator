package allocator

import (
	"k8s.io/apimachinery/pkg/types"
)

const StartingPort = 35000
const MaxPort = 65535

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

// ClusterRanges maps cluster owners to their allocated port ranges.
// Used for finding free port ranges when allocating new clusters.
type ClusterRanges map[OwnerCluster]Range

func (o Owner) ToNamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: o.Namespace,
		Name:      o.Container,
	}
}
