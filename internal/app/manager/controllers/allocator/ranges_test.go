package allocator

import (
	"github.com/weka/weka-operator/internal/app/manager/domain"
	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"testing"
)

func getTestTopology() domain.Topology {
	testTopology := domain.Topology{
		Drives: []string{"/dev/sdb", "/dev/sdc", "/dev/sdd", "/dev/sde", "/dev/sdf"},
		Nodes: []string{
			"wekabox14.lan", "wekabox15.lan", "wekabox16.lan", "wekabox17.lan", "wekabox18.lan",
			"wekabox19.lan", "wekabox20.lan", "wekabox21.lan", "wekabox22.lan", "wekabox23.lan",
		},
		// TODO: Get from k8s instead, but having it here helps for now with testing, minimizing relying on k8s
		MinCore:  2,
		CoreStep: 1,
		MaxCore:  7,
		Network: v1alpha1.NetworkSelector{
			EthSlots: []string{"aws_0", "aws_1", "aws_2",
				"aws_3", "aws_4", "aws_5",
				"aws_6", "aws_7", "aws_8",
				"aws_9", "aws_10", "aws_11",
				"aws_12", "aws_13", "aws_14",
			},
		},
	}
	return testTopology
}

func TestGetFreeRange(t *testing.T) {
	ownerCluster := OwnerCluster{
		ClusterName: "test",
		Namespace:   "test",
	}

	clusterRanges := ClusterRanges{}

	r1, _ := clusterRanges.GetFreeRange(500)
	if r1 != 14000 {
		t.Errorf("Expected 14001, got %d", r1)
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
	if r2 != 14000 {
		t.Errorf("Expected 14000, got %d", r2)
		return
	}

	r3, err := GetFreeRange(clusterRanges[ownerCluster], []Range{
		{r2, 1},
	}, 1)
	if err != nil {
		t.Errorf("Expected nil, got %v", err)
		return
	}
	if r3 != 14001 {
		t.Errorf("Expected 14001, got %d", r3)
		return
	}

	cr2, _ := clusterRanges.GetFreeRange(500)
	if cr2 != 14500 {
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
	if r1 != 14000 {
		t.Errorf("Expected 14001, got %d", r1)
		return
	}

	allocations := InitAllocationsMap()
	allocations.Global.ClusterRanges[ownerCluster] = Range{
		Base: r1,
		Size: 500,
	}

	allocations.NodeMap["wekabox14.lan"] = NodeAllocations{
		AllocatedRanges: map[Owner][]Range{},
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

	if rN1.Base != 14000 {
		t.Errorf("Expected 14000, got %d", rN1.Base)
		return
	}
	allocations.NodeMap["wekabox14.lan"].AllocatedRanges[owner] = append(allocations.NodeMap["wekabox14.lan"].AllocatedRanges[owner], rN1)

	owner.Container = "test2"
	rN2, err := allocations.FindNodeRange(owner, "wekabox14.lan", 1)
	if err != nil {
		t.Errorf("Expected nil, got %v", err)
		return
	}

	if rN2.Base != 14000 {
		t.Errorf("Expected 14001, got %d", rN2.Base)
		return
	}
}
