package domain

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"gopkg.in/yaml.v3"
)

func TestAllocatePort(t *testing.T) {
	ctx := context.Background()
	buffer := &bytes.Buffer{}
	writer := bufio.NewWriter(buffer)
	ctx = InitTestingLogger(ctx, writer)

	owner := OwnerCluster{
		ClusterName: "testCluster",
		Namespace:   "testNamespace",
	}

	testTopology := Topology{
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
			EthSlots: []string{
				"aws_0", "aws_1", "aws_2",
				"aws_3", "aws_4", "aws_5",
				"aws_6", "aws_7", "aws_8",
				"aws_9", "aws_10", "aws_11",
				"aws_12", "aws_13", "aws_14",
			},
		},
	}

	allocations := &Allocations{
		NodeMap: AllocationsMap{},
	}

	template := ClusterTemplate{
		DriveCores:        1,
		ComputeCores:      1,
		ComputeContainers: 5,
		DriveContainers:   5,
		NumDrives:         1,
		MaxFdsPerNode:     1,
	}

	allocator := NewAllocator(testTopology)

	owner.ClusterName = "a"
	newMap, err, _ := allocator.Allocate(ctx, owner, template, allocations, 1)
	newMap, err, changed := allocator.Allocate(ctx, owner, template, allocations, 1)
	if err != nil {
		t.Errorf("re-allocate should not fail: %v", err)
	}
	// no-change validation
	if changed {
		t.Errorf("re-allocation should not change the map")
	}
	owner.ClusterName = "b"
	newMap, err, _ = allocator.Allocate(ctx, owner, template, newMap, 1)

	// ensure that all 10 hosts are filled
	for i, node := range testTopology.Nodes {
		nodeAlloc := newMap.NodeMap[NodeName(node)]
		freeDrives := nodeAlloc.GetFreeDrives(testTopology.Drives)
		if len(freeDrives) == len(testTopology.Drives) {
			t.Errorf("Node %d is not filled", i)
		}
	}

	owner.ClusterName = "c"
	newMap, err, _ = allocator.Allocate(ctx, owner, template, newMap, 1)
	if err != nil {
		t.Errorf("Failed to allocate: %v", err)
	}

	owner.ClusterName = "d"
	newMap, err, _ = allocator.Allocate(ctx, owner, template, newMap, 1)
	if err != nil {
		t.Errorf("Failed to allocate: %v", err)
	}

	owner.ClusterName = "e"
	newMap, err, _ = allocator.Allocate(ctx, owner, template, newMap, 1)
	if err != nil {
		t.Errorf("Failed to allocate: %v", err)
	}
	// recycle validation
	_ = allocator.DeallocateCluster(owner, newMap)

	newMap, err, _ = allocator.Allocate(ctx, owner, template, newMap, 1)
	if err != nil {
		t.Errorf("Failed to allocate: %v", err)
	}

	// printAsYaml(allocations) // for debugging only
}

func printAsYaml(allocations *Allocations) {
	data, _ := yaml.Marshal(allocations)
	fmt.Println(string(data))
}
