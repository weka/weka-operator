package allocator

import (
	"fmt"
	"sort"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
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

// getClusterPortByName returns the current allocated port from cluster status by name
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
	default:
		return 0
	}
}

// getClusterRange returns the cluster's port range boundaries
func getClusterRange(cluster *weka.WekaCluster) Range {
	return Range{
		Base: cluster.Status.Ports.BasePort,
		Size: cluster.Status.Ports.PortRange,
	}
}

// EnsureSpecificGlobalRange validates that a specific port range is available.
// Similar to the original ConfigMap-based version but reads from cluster status.
func EnsureSpecificGlobalRange(cluster *weka.WekaCluster, name string, target Range, nodePortClaims []Range) (Range, error) {
	existing := getClusterPortByName(cluster, name)

	// Idempotency check - if already allocated, validate it matches
	if existing != 0 {
		if existing != target.Base {
			return Range{}, fmt.Errorf("range %s is already allocated with different base %d", name, existing)
		}
		if target.Size != 1 {
			return Range{}, fmt.Errorf("range %s is already allocated with different size", name)
		}
		return Range{Base: existing, Size: target.Size}, nil
	}

	boundaries := getClusterRange(cluster)
	allocatedRanges := append(GetClusterGlobalAllocatedRanges(cluster), nodePortClaims...)

	SortRanges(allocatedRanges)
	if !IsRangeAvailable(boundaries, allocatedRanges, target) {
		return Range{}, fmt.Errorf("range %s is not available", name)
	}
	return target, nil
}

// EnsureGlobalRangeWithOffset finds a free range within the cluster's port range starting at the given offset.
// Similar to the original ConfigMap-based version but reads from cluster status.
func EnsureGlobalRangeWithOffset(cluster *weka.WekaCluster, name string, size int, offset int, nodePortClaims []Range) (Range, error) {
	existing := getClusterPortByName(cluster, name)

	// Idempotency check - if already allocated, return it
	if existing != 0 {
		return Range{Base: existing, Size: size}, nil
	}

	boundaries := getClusterRange(cluster)
	allocatedRanges := append(GetClusterGlobalAllocatedRanges(cluster), nodePortClaims...)

	SortRanges(allocatedRanges)
	rangeBase, err := GetFreeRangeWithOffset(boundaries, allocatedRanges, size, offset)
	if err != nil {
		return Range{}, err
	}
	return Range{Base: rangeBase, Size: size}, nil
}

