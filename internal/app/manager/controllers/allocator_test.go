package controllers

import (
	"fmt"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
	"testing"
)

func TestAllocatePort(t *testing.T) {
	var log logr.Logger
	zapLog, err := zap.NewDevelopment()
	if err != nil {
		panic(fmt.Sprintf("Failed to create logger: %v", err))
	}
	log = zapr.NewLogger(zapLog)

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

	allocMap := AllocationsMap{}

	template := ClusterTemplate{
		DriveCores:        1,
		ComputeCores:      1,
		ComputeContainers: 5,
		DriveContainers:   5,
		NumDrives:         1,
		MaxFdsPerNode:     1,
	}

	allocator := NewAllocator(log, testTopology)

	owner.ClusterName = "a"
	newMap, err, _ := allocator.Allocate(owner, template, allocMap, 1)
	newMap, err, changed := allocator.Allocate(owner, template, allocMap, 1)
	if err != nil {
		t.Errorf("re-allocate should not fail: %v", err)
	}
	// no-change validation
	if changed {
		t.Errorf("re-allocation should not change the map")
	}
	owner.ClusterName = "b"
	newMap, err, _ = allocator.Allocate(owner, template, newMap, 1)

	// ensure that all 10 hosts are filled
	for i, node := range testTopology.Nodes {
		nodeAlloc := newMap[NodeName(node)]
		freeDrives := nodeAlloc.GetFreeDrives(testTopology.Drives)
		if len(freeDrives) == len(testTopology.Drives) {
			t.Errorf("Node %d is not filled", i)
		}
	}

	owner.ClusterName = "c"
	newMap, err, _ = allocator.Allocate(owner, template, newMap, 1)
	if err != nil {
		t.Errorf("Failed to allocate: %v", err)
	}

	owner.ClusterName = "d"
	newMap, err, _ = allocator.Allocate(owner, template, newMap, 1)
	if err != nil {
		t.Errorf("Failed to allocate: %v", err)
	}

	owner.ClusterName = "e"
	newMap, err, _ = allocator.Allocate(owner, template, newMap, 1)
	if err != nil {
		t.Errorf("Failed to allocate: %v", err)
	}
	// recycle validation
	_ = allocator.DeallocateCluster(owner, newMap)

	newMap, err, _ = allocator.Allocate(owner, template, newMap, 1)
	if err != nil {
		t.Errorf("Failed to allocate: %v", err)
	}

	printAsYaml(newMap)
}

func printAsYaml(newMap AllocationsMap) {
	data, _ := yaml.Marshal(newMap)
	fmt.Println(string(data))
}
