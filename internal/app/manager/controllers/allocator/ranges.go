package allocator

import (
	"fmt"
	"sort"
)

func (r ClusterRanges) GetFreeRange(size int) (int, error) {
	ranges := r.getSortedUsedRanges()
	totalRanges := len(ranges)
	for i, v := range ranges {
		if i == totalRanges-1 {
			// reached the end of the list
			if v.Base+size >= MaxPort {
				return 0, fmt.Errorf("no free range available for size %d", size)
			}
			return v.Base + v.Size, nil
		} else {
			// we are not at the end of the list yet, looking for big enough gaps to fit
			next := ranges[i+1]
			if v.Base+v.Size+size <= next.Base {
				return v.Base + v.Size, nil
			}
		}
	}
	if totalRanges == 0 {
		return StartingPort, nil
	}

	return 0, fmt.Errorf("no free range available for size %d, pre-existing ranges %d", size, totalRanges)
}

func (r ClusterRanges) getSortedUsedRanges() []Range {
	ranges := make([]Range, 0, len(r))
	for _, v := range r {
		ranges = append(ranges, v)
	}
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].Base < ranges[j].Base
	})
	return ranges
}

func SortRanges(ranges []Range) {
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].Base < ranges[j].Base
	})
}

func GetFreeRange(boundaries Range, ranges []Range, size int) (int, error) {
	if size > boundaries.Size {
		return 0, fmt.Errorf("requested size %d is bigger than the boundaries size %d", size, boundaries.Size)
	}

	totalRanges := len(ranges)
	for i, v := range ranges {

		if i == totalRanges-1 {
			// reached the end of the list
			if v.Base+v.Size+size >= boundaries.Base+boundaries.Size {
				return 0, fmt.Errorf("no free range available for size %d, with boundaries %v and ranges %v, %d ranges", size, boundaries, ranges, len(ranges))
			}
			return v.Base + v.Size, nil
		} else {
			// we are not at the end of the list yet, looking for big enough gaps to fit
			next := ranges[i+1]
			if v.Base+v.Size+size <= next.Base {
				return v.Base + v.Size, nil
			}
		}
	}
	if totalRanges == 0 {
		return boundaries.Base, nil
	}

	return 0, fmt.Errorf("no free range available for size %d", size)
}

func (a *Allocations) FindNodeRange(owner Owner, node NodeName, size int) (Range, error) {
	// combine Global.Range and node-specific ranges
	allowedRange := a.Global.ClusterRanges[owner.OwnerCluster]
	globalRanges := []Range{}
	for _, v := range a.Global.AllocatedRanges[owner.OwnerCluster] {
		globalRanges = append(globalRanges, v)
	}
	//globalRanges := a.Global.AllocatedRanges[owner.OwnerCluster]
	nodeRanges := a.NodeMap[node].AllocatedRanges[owner]
	clusterOwnedRanges := make([]Range, 0, len(globalRanges)+len(nodeRanges))
	for o, p := range a.NodeMap[node].AllocatedRanges {
		if o.OwnerCluster == owner.OwnerCluster {
			clusterOwnedRanges = append(clusterOwnedRanges, p...)
		}
	}
	allocatedRanges := make([]Range, 0, len(globalRanges)+len(clusterOwnedRanges))
	allocatedRanges = append(allocatedRanges, globalRanges...)
	allocatedRanges = append(allocatedRanges, clusterOwnedRanges...)
	// sort ranges by base
	SortRanges(allocatedRanges)
	rangeBase, err := GetFreeRange(allowedRange, allocatedRanges, size)
	if err != nil {
		return Range{}, err
	}
	return Range{Base: rangeBase, Size: size}, nil
}

func (a *Allocations) EnsureGlobalRange(Owner OwnerCluster, name string, size int) (Range, error) {
	if existing, ok := a.Global.AllocatedRanges[Owner][name]; ok {
		return existing, nil
	}

	allowedRange := a.Global.ClusterRanges[Owner]
	globalRanges := []Range{}
	for _, v := range a.Global.AllocatedRanges[Owner] {
		globalRanges = append(globalRanges, v)
	}
	//globalRanges := a.Global.AllocatedRanges[Owner]
	allNodesRanges := make([]Range, 0, len(a.NodeMap))
	for _, v := range a.NodeMap {
		for owner, ranges := range v.AllocatedRanges {
			if owner.OwnerCluster == Owner {
				allNodesRanges = append(allNodesRanges, ranges...)
			}
		}
	}

	allRanges := make([]Range, 0, len(allNodesRanges)+len(globalRanges))
	allRanges = append(allRanges, globalRanges...)
	allRanges = append(allRanges, allNodesRanges...)
	SortRanges(allRanges)
	rangeBase, err := GetFreeRange(allowedRange, allRanges, size)
	if err != nil {
		return Range{}, err
	}
	newRange := Range{Base: rangeBase, Size: size}
	if _, ok := a.Global.AllocatedRanges[Owner]; !ok {
		a.Global.AllocatedRanges[Owner] = map[string]Range{}
	}
	a.Global.AllocatedRanges[Owner][name] = newRange
	return newRange, nil
}
