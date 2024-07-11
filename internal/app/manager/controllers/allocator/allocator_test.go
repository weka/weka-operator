package allocator

import (
	"context"
	"fmt"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	"github.com/weka/weka-operator/internal/app/manager/factory"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"

	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"gopkg.in/yaml.v3"
)

func newTestAllocator(ctx context.Context, topology domain.Topology) (Allocator, error) {
	cs := NewInMemoryConfigStore()
	return &TopologyAllocator{
		Topology:    topology,
		configStore: cs,
	}, nil
}

func testWekaCluster(name string) *v1alpha1.WekaCluster {
	return &v1alpha1.WekaCluster{
		Spec: v1alpha1.WekaClusterSpec{
			Template: "small",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: "testNamespace",
		},
	}
}

func TestAllocatePort(t *testing.T) {
	ctx := context.Background()

	testTopology := domain.Topology{
		Drives: []string{"/dev/sdb", "/dev/sdc", "/dev/sdd", "/dev/sde", "/dev/sdf"},
		Nodes: []string{
			"wekabox14.lan", "wekabox15.lan", "wekabox16.lan", "wekabox17.lan", "wekabox18.lan",
			"wekabox19.lan", "wekabox20.lan", "wekabox21.lan", "wekabox22.lan", "wekabox23.lan",
		},
		// TODO: Get from k8s instead, but having it here helps for now with testing, minimizing relying on k8s
		MinCore:  2,
		CoreStep: 1,
		MaxCore:  11,
		Network: v1alpha1.NetworkSelector{
			EthSlots: []string{"aws_0", "aws_1", "aws_2",
				"aws_3", "aws_4", "aws_5",
				"aws_6", "aws_7", "aws_8",
				"aws_9", "aws_10", "aws_11",
				"aws_12", "aws_13", "aws_14",
			},
		},
	}

	template := domain.ClusterTemplate{
		DriveCores:        1,
		ComputeCores:      1,
		ComputeContainers: 5,
		DriveContainers:   5,
		NumDrives:         1,
		MaxFdsPerNode:     1,
	}

	allocator, err := newTestAllocator(ctx, testTopology)
	if err != nil {
		t.Errorf("Failed to create allocator: %v", err)
	}

	clusters := []*v1alpha1.WekaCluster{
		testWekaCluster("a"),
		testWekaCluster("b"),
		testWekaCluster("c"),
		testWekaCluster("d"),
		testWekaCluster("e"),
		//
		testWekaCluster("f"),
		testWekaCluster("g"),
		testWekaCluster("h"),
		testWekaCluster("i"),
		testWekaCluster("j"),
	}

	// TODO: Make smarter test, this is just fitting 10 clusters on 10 machines

	for _, cluster := range clusters {
		containers, err := factory.BuildMissingContainers(cluster, template, testTopology, nil)
		if err != nil {
			t.Errorf("Failed to build containers: %v", err)
			return
		}

		err = allocator.AllocateClusterRange(ctx, cluster)
		if err != nil {
			t.Errorf("Failed to allocate cluster range: %v", err)
			return
		}

		err = allocator.AllocateContainers(ctx, *cluster, containers)
		if err != nil {
			t.Errorf("Failed to allocate containers: %v", err)
			return
		}

		agentNodePorts := map[string]bool{}
		for _, container := range containers {
			if !container.IsWekaContainer() {
				continue
			}
			found := agentNodePorts[fmt.Sprintf("%s:%d", container.Spec.NodeAffinity, container.Spec.AgentPort)]
			if found {
				t.Errorf("Node port already allocated: %s:%d", container.Spec.NodeAffinity, container.Spec.AgentPort)
				allocations, _ := allocator.GetAllocations(ctx)
				printAsYaml(allocations)
				return
			} else {
				agentNodePorts[fmt.Sprintf("%s:%d", container.Spec.NodeAffinity, container.Spec.AgentPort)] = true
			}
		}
	}

	for _, cluster := range clusters {
		err = allocator.DeallocateCluster(ctx, *cluster)
		if err != nil {
			t.Errorf("Failed to deallocate cluster: %v", err)
		}
	}

	//allocator owner.ClusterName = "a"
	//newMap, err, _ := allocator.Allocate(ctx, owner, template, allocations, 1)
	//newMap, err, changed := allocator.Allocate(context.Background(), owner, template, allocations, 1)
	//if err != nil {
	//	t.Errorf("re-allocate should not fail: %v", err)
	//}
	//// no-change validation
	//if changed {
	//	t.Errorf("re-allocation should not change the map")
	//}
	//owner.ClusterName = "b"
	//newMap, err, _ = allocator.Allocate(ctx, owner, template, newMap, 1)
	//
	//// ensure that all 10 hosts are filled
	//for i, node := range testTopology.Nodes {
	//	nodeAlloc := newMap.NodeMap[NodeName(node)]
	//	freeDrives := nodeAlloc.GetFreeDrives(testTopology.Drives)
	//	if len(freeDrives) == len(testTopology.Drives) {
	//		t.Errorf("Node %d is not filled", i)
	//	}
	//}
	//
	//owner.ClusterName = "c"
	//newMap, err, _ = allocator.Allocate(ctx, owner, template, newMap, 1)
	//if err != nil {
	//	t.Errorf("Failed to allocate: %v", err)
	//}
	//
	//owner.ClusterName = "d"
	//newMap, err, _ = allocator.Allocate(ctx, owner, template, newMap, 1)
	//if err != nil {
	//	t.Errorf("Failed to allocate: %v", err)
	//}
	//
	//owner.ClusterName = "e"
	//newMap, err, _ = allocator.Allocate(ctx, owner, template, newMap, 1)
	//if err != nil {
	//	t.Errorf("Failed to allocate: %v", err)
	//}
	//// recycle validation
	//_ = allocator.DeallocateCluster(owner, newMap)
	//
	//newMap, err, _ = allocator.Allocate(ctx, owner, template, newMap, 1)
	//if err != nil {
	//	t.Errorf("Failed to allocate: %v", err)
	//}
	//
}

func printAsYaml(allocations *Allocations) {
	data, _ := yaml.Marshal(allocations)
	fmt.Println(string(data))
}
