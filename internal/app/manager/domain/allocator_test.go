package domain

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"

	"gopkg.in/yaml.v3"
)

func TestAllocatePort(t *testing.T) {
	ctx := context.Background()
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
			EthDevice: "mlnx0",
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
	newMap, err, changed := allocator.Allocate(context.Background(), owner, template, allocations, 1)
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

func TestAllocate(t *testing.T) {
	owner := OwnerCluster{
		ClusterName: "testCluster",
		Namespace:   "testNamespace",
	}
	template := ClusterTemplate{}
	allocations := &Allocations{}
	size := 1

	ctx := context.Background()
	subject := &Allocator{}
	allocations, error, changed := subject.Allocate(ctx, owner, template, allocations, size)
	if error != nil {
		t.Errorf("Error: %v", error)
	}
	if allocations == nil {
		t.Errorf("Allocations is nil")
	}
	if changed {
		t.Errorf("Changed is true")
	}

	global := allocations.Global
	if global.AgentPorts == nil {
		t.Errorf("Allocations.Global.AgentPorts is nil")
	}
	if global.WekaContainerPorts == nil {
		t.Errorf("Allocations.Global.WekaContainerPorts is nil")
	}
	nodeMap := allocations.NodeMap
	if len(nodeMap) != 0 {
		t.Errorf("Allocations.NodeMap length expected: %d, got: %d", 0, len(nodeMap))
	}
}

func TestGetFreeDrives(t *testing.T) {
	owner := Owner{
		OwnerCluster: OwnerCluster{
			ClusterName: "testCluster",
			Namespace:   "testNamespace",
		},
		Container: "testContainer",
		Role:      "testRole",
	}
	tests := []struct {
		name               string
		availableDrives    []string
		allocatedDrives    map[Owner][]string
		expectedFreeDrives []string
	}{
		{
			name:               "empty",
			availableDrives:    []string{},
			allocatedDrives:    map[Owner][]string{},
			expectedFreeDrives: []string{},
		},
		{
			name:               "one drive",
			availableDrives:    []string{"/dev/sdb"},
			allocatedDrives:    map[Owner][]string{},
			expectedFreeDrives: []string{"/dev/sdb"},
		},
		{
			name:               "one drive allocated",
			availableDrives:    []string{"/dev/sdb"},
			allocatedDrives:    map[Owner][]string{owner: {"/dev/sdb"}},
			expectedFreeDrives: []string{},
		},
		{
			name:               "two drives",
			availableDrives:    []string{"/dev/sdb", "/dev/sdc"},
			allocatedDrives:    map[Owner][]string{owner: {"/dev/sdb"}},
			expectedFreeDrives: []string{"/dev/sdc"},
		},
		{
			name:               "two drives 2",
			availableDrives:    []string{"/dev/sdb", "/dev/sdc"},
			allocatedDrives:    map[Owner][]string{owner: {"/dev/sdc"}},
			expectedFreeDrives: []string{"/dev/sdb"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subject := &NodeAllocations{}
			subject.Drives = tt.allocatedDrives

			availableDrives := tt.availableDrives
			freeDrives := subject.GetFreeDrives(availableDrives)

			if !(reflect.DeepEqual(freeDrives, tt.expectedFreeDrives)) {
				t.Errorf("Expected: %v, got: %v", tt.expectedFreeDrives, freeDrives)
			}
		})
	}
}

func printAsYaml(allocations *Allocations) {
	data, _ := yaml.Marshal(allocations)
	fmt.Println(string(data))
}
