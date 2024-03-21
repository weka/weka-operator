package controllers

import (
	"context"
	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ClusterTemplate struct {
	DriveCores        int
	ComputeCores      int
	ComputeContainers int
	DriveContainers   int
	NumDrives         int
	MaxFdsPerNode     int
	DriveHugepages    int
	ComputeHugepages  int
}

// Topology is a stub of all information we require to make a scheduling
// By using ClusterLevelConfig we can avodid using a lot of querying into k8s and user inputs,
// abstracting them here and then working towards smarter scheduler that wont require such object
// Hugepages is the only resource we let K8s allocate right now, it can't allocate drives and cant allocate CPUs the way we need
type Topology struct {
	Drives   []string                 `json:"drives"`
	Nodes    []string                 `json:"availableHosts"`
	MinCore  int                      `json:"minCore"`
	MaxCore  int                      `json:"maxCore"`
	CoreStep int                      `json:"coreStep"`
	Network  v1alpha1.NetworkSelector `json:"network"`
}

func (k *Topology) GetAvailableCpus() []int {
	cores := []int{}
	for i := k.MinCore; i <= k.MaxCore; i += k.CoreStep {
		cores = append(cores, i)
	}
	return cores
}

// NewK8sClusterLevelConfig creates a new Topology
var DevboxWekabox = Topology{
	Drives: []string{"/dev/sdb", "/dev/sdc", "/dev/sdd", "/dev/sde", "/dev/sdf"},
	Nodes:  []string{"wekabox14.lan", "wekabox15.lan", "wekabox16.lan", "wekabox17.lan", "wekabox18.lan"},
	// TODO: Get from k8s instead, but having it here helps for now with testing, minimizing relying on k8s
	MinCore:  2,
	CoreStep: 1,
	MaxCore:  11,
	Network: v1alpha1.NetworkSelector{
		EthDevice: "mlnx0",
	},
}

var WekaClusterTemplates = map[string]ClusterTemplate{
	"dev": {
		DriveCores:        1,
		ComputeCores:      1,
		ComputeContainers: 5,
		DriveContainers:   5,
		NumDrives:         1,
		MaxFdsPerNode:     1,
		DriveHugepages:    1200,
		ComputeHugepages:  1200,
	},
}

type topologyGetter func(ctx context.Context, reader client.Reader) (Topology, error)

var Topologies = map[string]topologyGetter{
	"dev_wekabox": func(ctx context.Context, reader client.Reader) (Topology, error) {
		return DevboxWekabox, nil
	},
	"discover_oci": getOciDev,
}

func getOciDev(ctx context.Context, reader client.Reader) (Topology, error) {
	// get nodes via reader
	nodeNames, err := getNodeNames(ctx, reader)
	if err != nil {
		return Topology{}, err
	}
	return Topology{
		Drives:   []string{"/dev/oracleoci/oraclevdb"},
		Nodes:    nodeNames,
		MinCore:  2, // TODO: How to determine, other then querying machines?
		CoreStep: 2,
		MaxCore:  4,
	}, nil
}

func getNodeNames(ctx context.Context, reader client.Reader) ([]string, error) {
	nodes := &v1.NodeList{}
	if err := reader.List(ctx, nodes); err != nil {
		return nil, err
	}
	var nodeNames []string
	for _, node := range nodes.Items {
		nodeNames = append(nodeNames, node.Name)
	}
	return nodeNames, nil
}
