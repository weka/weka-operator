package controllers

import (
	"fmt"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
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

	allocMap := AllocationsMap{}
	clusterConfig := DevboxWekabox

	template := ClusterTemplate{
		DriveCores:        1,
		ComputeCores:      1,
		ComputeContainers: 5,
		DriveContainers:   5,
		NumDrives:         1,
		MaxFdsPerNode:     1,
	}

	allocator := NewAllocator(log, clusterConfig)

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
	owner.ClusterName = "c"
	newMap, err, _ = allocator.Allocate(owner, template, newMap, 1)
	owner.ClusterName = "d"
	newMap, err, _ = allocator.Allocate(owner, template, newMap, 1)
	owner.ClusterName = "e"
	newMap, err, _ = allocator.Allocate(owner, template, newMap, 1)
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
