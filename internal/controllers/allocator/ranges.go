package allocator

import (
	"fmt"
	"sort"

	"github.com/weka/weka-k8s-api/api/v1alpha1"
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

func (r ClusterRanges) IsClusterRangeAvailable(targetRange Range) bool {
	// we do not respect global starting point here, due to possible migration, we just validate one very specific range to be not taken
	return IsRangeAvailable(Range{0, MaxPort}, r.getSortedUsedRanges(), targetRange)
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

func GetFreeRangeWithOffset(boundaries Range, ranges []Range, size int, offset int) (int, error) {
	// Adjust boundaries base by offset
	startBoundary := boundaries.Base + offset

	// Sort the ranges based on the base point
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].Base < ranges[j].Base
	})

	// Check for a free range before the first existing range
	if len(ranges) > 0 && ranges[0].Base-startBoundary >= size {
		return startBoundary, nil
	}

	// Check for free ranges between existing ranges
	for i := 0; i < len(ranges)-1; i++ {
		start := max(startBoundary, ranges[i].Base+ranges[i].Size)
		end := ranges[i+1].Base

		if end-start >= size {
			return start, nil
		}
	}

	// Check for a free range after the last existing range
	if len(ranges) > 0 {
		start := max(startBoundary, ranges[len(ranges)-1].Base+ranges[len(ranges)-1].Size)
		if boundaries.Base+boundaries.Size-start >= size {
			return start, nil
		}
	}

	// If no ranges exist, just check within the adjusted boundaries
	if len(ranges) == 0 && boundaries.Size >= size {
		return startBoundary, nil
	}

	return -1, fmt.Errorf("no free range found with size %d and offset %d", size, offset)
}

func GetFreeRange(boundaries Range, ranges []Range, size int) (int, error) {
	return GetFreeRangeWithOffset(boundaries, ranges, size, 0)
}

func IsRangeAvailable(boundaries Range, ranges []Range, targetRange Range) bool {
	// Ensure targetRange is completely within boundaries.
	if targetRange.Base < boundaries.Base ||
		targetRange.Base+targetRange.Size-1 > boundaries.Base+boundaries.Size-1 {
		return false
	}

	targetStart := targetRange.Base
	targetEnd := targetRange.Base + targetRange.Size - 1

	// Check for overlap with any allocated range.
	for _, r := range ranges {
		allocatedStart := r.Base
		allocatedEnd := r.Base + r.Size - 1

		// Overlap exists if the target range starts before the allocated range ends
		// and the allocated range starts before the target range ends.
		if targetStart <= allocatedEnd && allocatedStart <= targetEnd {
			return false
		}
	}

	return true
}

// getClusterPortByNameV} returns the current allocated port from cluster status by name
func getClusterPortByName(cluster *weka.WekaCluster, name string) int {
	switch name {
	case "lb":
		return cluster.Status.Ports.LbPort
	case "lbAdmin":
		return cluster.Status.Ports.LbAdminPort
	case "s3":
		return cluster.Status.Ports.S3Port
	case "managementProxy":
		return cluster.Status.Ports.ManagementProxyPort
	case "dataServices":
		return cluster.Status.Ports.DataServicesPort
	default:
		return 0
	}
}

func (a *Allocations) FindNodeRangeWithOffset(owner Owner, node v1alpha1.NodeName, size int, offset int) (Range, error) {
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
			for _, r := range p {
				clusterOwnedRanges = append(clusterOwnedRanges, r)
			}
		}
	}
	allocatedRanges := make([]Range, 0, len(globalRanges)+len(clusterOwnedRanges))
	allocatedRanges = append(allocatedRanges, globalRanges...)
	allocatedRanges = append(allocatedRanges, clusterOwnedRanges...)
	// sort ranges by base
	SortRanges(allocatedRanges)
	rangeBase, err := GetFreeRangeWithOffset(allowedRange, allocatedRanges, size, offset)
	if err != nil {
		return Range{}, err
	}
	return Range{Base: rangeBase, Size: size}, nil
}

func (a *Allocations) FindNodeRange(owner Owner, node v1alpha1.NodeName, size int) (Range, error) {
	return a.FindNodeRangeWithOffset(owner, node, size, 0)
}

func (a *Allocations) EnsureSpecificGlobalRange(Owner OwnerCluster, name string, target Range) (Range, error) {
	if existing, ok := a.Global.AllocatedRanges[Owner][name]; ok {
		if existing.Base != target.Base {
			return Range{}, fmt.Errorf("range %s is already allocated with different base %d", name, existing.Base)
		}
		if existing.Size != target.Size {
			return Range{}, fmt.Errorf("range %s is already allocated with different size %d", name, existing.Size)
		}
		return existing, nil
	}

	boundaries := a.Global.ClusterRanges[Owner]
	allocatedRanges := []Range{}
	for _, v := range a.Global.AllocatedRanges[Owner] {
		allocatedRanges = append(allocatedRanges, v)
	}

	for _, v := range a.NodeMap {
		for owner, ranges := range v.AllocatedRanges {
			if owner.OwnerCluster == Owner {
				for _, r := range ranges {
					allocatedRanges = append(allocatedRanges, r)
				}
			}
		}
	}

	SortRanges(allocatedRanges)
	if !IsRangeAvailable(boundaries, allocatedRanges, target) {
		return Range{}, fmt.Errorf("range %s is not available", name)
	} else {
		if _, ok := a.Global.AllocatedRanges[Owner]; !ok {
			a.Global.AllocatedRanges[Owner] = map[string]Range{}
		}
		a.Global.AllocatedRanges[Owner][name] = target
		return target, nil
	}
}

func (a *Allocations) EnsureGlobalRangeWithOffset(Owner OwnerCluster, name string, size int, offset int) (Range, error) {
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
				for _, r := range ranges {
					allNodesRanges = append(allNodesRanges, r)
				}
			}
		}
	}

	allRanges := make([]Range, 0, len(allNodesRanges)+len(globalRanges))
	allRanges = append(allRanges, globalRanges...)
	allRanges = append(allRanges, allNodesRanges...)
	SortRanges(allRanges)
	rangeBase, err := GetFreeRangeWithOffset(allowedRange, allRanges, size, offset)
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

func (a *Allocations) EnsureGlobalRange(Owner OwnerCluster, name string, size int) (Range, error) {
	return a.EnsureGlobalRangeWithOffset(Owner, name, size, 0)
}
