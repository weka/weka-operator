package allocator

import (
	"slices"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

const StartingPort = 35000
const MaxPort = 65535

type (
	NodeAllocMap map[weka.NodeName]NodeAllocations
)

type AllocRoleMap map[OwnerRole]int

type Allocations struct {
	Global  GlobalAllocations
	NodeMap NodeAllocMap
}

func (a *Allocations) InitNodeAllocations(nodeName weka.NodeName) {
	if _, ok := a.NodeMap[nodeName]; !ok {
		a.NodeMap[nodeName] = NodeAllocations{
			AllocatedRanges: map[Owner]map[string]Range{},
		}
	}
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
	AllocatedRanges map[Owner]map[string]Range
	Drives          map[Owner][]string
	EthSlots        map[Owner][]string
}

func (n *NodeAllocations) GetUsedDrivesCount() int {
	return len(n.Drives)
}

func (n *NodeAllocations) calcUnusedDrives(allDrives []string) []string {
	usedDrives := map[string]bool{}
	for _, drives := range n.Drives {
		for _, drive := range drives {
			usedDrives[drive] = true
		}
	}
	unusedDrives := []string{}
	for _, drive := range allDrives {
		if !usedDrives[drive] {
			unusedDrives = append(unusedDrives, drive)
		}
	}
	return unusedDrives
}

func (n *NodeAllocations) allocateDrives(owner Owner, numDrives int, allDrives []string) []string {
	unusedDrives := n.calcUnusedDrives(allDrives)
	if len(unusedDrives) < numDrives {
		return nil
	}
	drives := unusedDrives[:numDrives]
	if n.Drives == nil {
		n.Drives = map[Owner][]string{}
	}
	if _, ok := n.Drives[owner]; !ok {
		n.Drives[owner] = []string{}
	}
	n.Drives[owner] = append(n.Drives[owner], drives...)
	return drives
}

func (n *NodeAllocations) deallocateDrives(owner Owner) {
	delete(n.Drives, owner)
}

func (n *NodeAllocations) dealocateDrivesBySerials(owner Owner, serials []string) {
	if drives, ok := n.Drives[owner]; ok {
		newDrives := []string{}
		for _, drive := range drives {
			if !slices.Contains(serials, drive) {
				newDrives = append(newDrives, drive)
			}
		}
		n.Drives[owner] = newDrives
		if len(n.Drives[owner]) == 0 {
			delete(n.Drives, owner)
		}
	}
}

func (n *NodeAllocations) HasDifferentContainerSameClusterRole(owner Owner) bool {
	for o := range n.AllocatedRanges {
		if owner.IsSameClusterAndRole(o) && owner.Container != o.Container {
			return true
		}
	}
	return false
}

func (o Owner) ToNamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: o.Namespace,
		Name:      o.Container,
	}
}
