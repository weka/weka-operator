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

	allocations.NodeMap["wekabox14.lan"] = NodeAllocations{
		AllocatedRanges: map[Owner]map[string]Range{},
	}

	owner := Owner{
		OwnerCluster: ownerCluster,
		Container:    "test",
		Role:         "compute",
	}

	rN1, err := allocations.FindNodeRange(owner, "wekabox14.lan", 1)
	if err != nil {
		t.Errorf("Expected nil, got %v", err)
		return
	}

	if rN1.Base != StartingPort {
		t.Errorf("Expected %d, got %d", StartingPort, rN1.Base)
		return
	}
	allocations.NodeMap["wekabox14.lan"].AllocatedRanges[owner]["test"] = rN1

	owner.Container = "test2"
	rN2, err := allocations.FindNodeRangeWithOffset(owner, "wekabox14.lan", 1, SinglePortsOffset)
	if err != nil {
		t.Errorf("Expected nil, got %v", err)
		return
	}

	if rN2.Base != StartingPort+SinglePortsOffset {
		t.Errorf("Expected %d, got %d", StartingPort+SinglePortsOffset, rN2.Base)
		return
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
