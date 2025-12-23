package allocator

import (
	"testing"
)

func TestGetFreeRange(t *testing.T) {
	ownerCluster := OwnerCluster{
		ClusterName: "test",
		Namespace:   "test",
	}

	clusterRanges := ClusterRanges{}

	r1, _ := clusterRanges.GetFreeRange(500)
	if r1 != StartingPort {
		t.Errorf("Expected %d, got %d", StartingPort, r1)
		return
	}

	clusterRanges[ownerCluster] = Range{
		Base: r1,
		Size: 500,
	}

	r2, err := GetFreeRange(clusterRanges[ownerCluster], []Range{}, 1)
	if err != nil {
		t.Errorf("Expected nil, got %v", err)
		return
	}
	if r2 != StartingPort {
		t.Errorf("Expected %d, got %d", StartingPort, r2)
		return
	}

	r3, err := GetFreeRange(clusterRanges[ownerCluster], []Range{
		{r2, 1},
	}, 1)
	if err != nil {
		t.Errorf("Expected nil, got %v", err)
		return
	}
	if r3 != StartingPort+1 {
		t.Errorf("Expected 14001, got %d", r3)
		return
	}

	cr2, _ := clusterRanges.GetFreeRange(500)
	if cr2 != StartingPort+500 {
		t.Errorf("Expected 14500, got %d", r1)
		return
	}

}

func TestAllocationsRanges(t *testing.T) {
	ownerCluster := OwnerCluster{
		ClusterName: "test",
		Namespace:   "test",
	}

	clusterRanges := ClusterRanges{}

	r1, _ := clusterRanges.GetFreeRange(500)
	if r1 != StartingPort {
		t.Errorf("Expected 14001, got %d", r1)
		return
	}

	allocations := InitAllocationsMap()
	allocations.Global.ClusterRanges[ownerCluster] = Range{
		Base: r1,
		Size: 500,
	}
}

func TestEnsureRangeIsAvailable(t *testing.T) {
	boundaries := Range{
		Base: 0,
		Size: 100,
	}

	tests := []struct {
		allocatedRanges []Range
		newRange        Range
		expected        bool
	}{
		{
			allocatedRanges: []Range{
				{Base: 10, Size: 20},
				{Base: 45, Size: 5},
			},
			newRange: Range{
				Base: 30,
				Size: 10,
			},
			expected: true,
		},
		{
			allocatedRanges: []Range{},
			newRange: Range{
				Base: 30,
				Size: 10,
			},
			expected: true,
		},
		{
			allocatedRanges: []Range{
				{Base: 10, Size: 25},
				{Base: 45, Size: 5},
			},
			newRange: Range{
				Base: 30,
				Size: 10,
			},
			expected: false,
		},
	}

	for _, test := range tests {
		result := IsRangeAvailable(boundaries, test.allocatedRanges, test.newRange)
		if result != test.expected {
			t.Errorf("Expected %v, got %v, on dataset %v and range %v", test.expected, result, test.allocatedRanges, test.newRange)
		}
	}

}

func TestIsRangeAvailable_TargetRangeFree(t *testing.T) {
	// Define boundaries that cover all allocated ranges.
	// Allowed ports: 35000 to 49999.
	boundaries := Range{Base: 35000, Size: 15000}

	allocated := []Range{
		{Base: 46000, Size: 500}, // Ports 46000–46499
		{Base: 35000, Size: 500}, // Ports 35000–35499
		{Base: 35500, Size: 500}, // Ports 35500–35999
		{Base: 36500, Size: 500}, // Ports 36500–36999
	}

	// Target range: ports 36000–36499, which is free.
	target := Range{Base: 36000, Size: 500}

	if !IsRangeAvailable(boundaries, allocated, target) {
		t.Errorf("Expected target range %+v to be available", target)
	}
}

// Optionally, add a negative test case for overlapping ranges.
func TestIsRangeAvailable_TargetRangeNotFree(t *testing.T) {
	boundaries := Range{Base: 35000, Size: 15000}

	allocated := []Range{
		{Base: 46000, Size: 500},
		{Base: 35000, Size: 500},
		{Base: 35500, Size: 500},
		{Base: 36500, Size: 500},
	}

	// This target overlaps with the range 35500–35999.
	target := Range{Base: 35400, Size: 200}

	if IsRangeAvailable(boundaries, allocated, target) {
		t.Errorf("Expected target range %+v to be unavailable due to overlap", target)
	}
}
