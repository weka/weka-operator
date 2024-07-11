package domain

import (
	"context"

	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ClusterTemplate struct {
	DriveCores          int
	ComputeCores        int
	EnvoyCores          int
	S3Cores             int
	S3ExtraCores        int
	ComputeContainers   int
	DriveContainers     int
	S3Containers        int
	NumDrives           int
	MaxFdsPerNode       int
	DriveHugepages      int
	ComputeHugepages    int
	HugePageSize        string
	HugePagesOverride   string
	S3FrontendHugepages int
}

// Topology is a stub of all information we require to make a scheduling
// By using ClusterLevelConfig we can avodid using a lot of querying into k8s and user inputs,
// abstracting them here and then working towards smarter scheduler that wont require such object
// Hugepages is the only resource we let K8s allocate right now, it can't allocate drives and cant allocate CPUs the way we need
type Topology struct {
	Drives               []string                 `json:"drives"`
	Nodes                []string                 `json:"availableHosts"`
	MinCore              int                      `json:"minCore"`
	MaxCore              int                      `json:"maxCore"`
	CoreStep             int                      `json:"coreStep"`
	Network              v1alpha1.NetworkSelector `json:"network"`
	ForcedCpuPolicy      v1alpha1.CpuPolicy       `json:"forcedCpuPolicy"`
	MaxS3Containers      int                      `json:"maxS3Containers"`
	NodeConfigMapPattern string                   `json:"nodeConfigMap"`
	ForceSignDrives      bool                     `json:"forceSignDrives"`
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
	// Drives: []string{"/dev/sdb", "/dev/sdc", "/dev/sdd", "/dev/sde", "/dev/sdf"},
	// Drives: []string{"/dev/nvme0n1", "/dev/nvme2n1", "/dev/nvme3n1"}, //skipping N1, since it's used for local storage
	Drives: []string{"/dev/nvme0n1", "/dev/sdc", "/dev/sdb", "/dev/sdd", "/dev/sde", "/dev/sdf"}, // skipping N1, since it's used for local storage
	Nodes:  []string{"wekabox14.lan", "wekabox15.lan", "wekabox16.lan", "wekabox17.lan", "wekabox18.lan"},
	// TODO: Get from k8s instead, but having it here helps for now with testing, minimizing relying on k8s
	MinCore:  2,
	CoreStep: 1,
	MaxCore:  11,
	Network: v1alpha1.NetworkSelector{
		EthDevice: "mlnx0",
	},
	ForcedCpuPolicy: v1alpha1.CpuPolicyShared,
}

func BuildDynamicTemplate(config *v1alpha1.WekaConfig) ClusterTemplate {
	hgSize := "2Mi"

	if config.DriveCores == 0 {
		config.DriveCores = 1
	}

	if config.ComputeCores == 0 {
		config.ComputeCores = 1
	}

	if config.ComputeContainers == 0 {
		config.ComputeContainers = 6
	}

	if config.DriveContainers == 0 {
		config.DriveContainers = 6
	}

	if config.S3Cores == 0 {
		config.S3Cores = 1
	}

	if config.S3ExtraCores == 0 {
		config.S3ExtraCores = 1
	}

	if config.NumDrives == 0 {
		config.NumDrives = 1
	}

	if config.DriveHugepages == 0 {
		config.DriveHugepages = 1500 * config.DriveCores
	}

	if config.ComputeHugepages == 0 {
		config.ComputeHugepages = 3000 * config.ComputeCores
	}

	if config.S3FrontendHugepages == 0 {
		config.S3FrontendHugepages = 1400 * config.S3Cores
	}

	if config.EnvoyCores == 0 {
		config.EnvoyCores = 1
	}

	return ClusterTemplate{
		DriveCores:          config.DriveCores,
		ComputeCores:        config.ComputeCores,
		ComputeContainers:   config.ComputeContainers,
		DriveContainers:     config.DriveContainers,
		S3Containers:        config.S3Containers,
		S3Cores:             config.S3Cores,
		S3ExtraCores:        config.S3ExtraCores,
		NumDrives:           config.NumDrives,
		DriveHugepages:      config.DriveHugepages,
		ComputeHugepages:    config.ComputeHugepages,
		S3FrontendHugepages: config.S3FrontendHugepages,
		HugePageSize:        hgSize,
		EnvoyCores:          config.EnvoyCores,
	}

}

func GetTemplateByName(name string, cluster v1alpha1.WekaCluster) (ClusterTemplate, bool) {
	if name == "dynamic" {
		return BuildDynamicTemplate(cluster.Spec.Dynamic), true
	}

	template, ok := WekaClusterTemplates[name]
	return template, ok
}

var WekaClusterTemplates = map[string]ClusterTemplate{
	"small_s3": {
		DriveCores:          1,
		ComputeCores:        1,
		ComputeContainers:   6,
		DriveContainers:     6,
		S3Containers:        6,
		S3Cores:             1,
		S3ExtraCores:        1,
		NumDrives:           1,
		DriveHugepages:      1500,
		ComputeHugepages:    3000,
		S3FrontendHugepages: 1400,
		HugePageSize:        "2Mi",
		EnvoyCores:          1,
	},
	"small": {
		DriveCores:        1,
		ComputeCores:      1,
		ComputeContainers: 6,
		DriveContainers:   6,
		NumDrives:         1,
		DriveHugepages:    1500,
		ComputeHugepages:  3000,
		HugePageSize:      "2Mi",
		EnvoyCores:        1,
	},
	"large": {
		DriveCores:        1,
		ComputeCores:      1,
		ComputeContainers: 20,
		DriveContainers:   20,
		NumDrives:         1,
		DriveHugepages:    1500,
		ComputeHugepages:  3000,
		HugePageSize:      "2Mi",
		EnvoyCores:        2,
		S3Containers:      5,
		S3Cores:           1,
		S3ExtraCores:      2,
	},
}

type topologyGetter func(ctx context.Context, reader client.Reader, nodeSelector map[string]string) (Topology, error)

var Topologies = map[string]topologyGetter{
	"dev_wekabox": func(ctx context.Context, reader client.Reader, nodeSelector map[string]string) (Topology, error) {
		return DevboxWekabox, nil
	},
	"discover_oci":          getOciDev,
	"discover_aws_i3en6x":   getAwsI3en6x,
	"aws_i3en6x_udp_bless":  blessUdpi3en6x,
	"aws_i3en6x_bless":      getAwsI3en6xBless,
	"aws_i3en3x_bless":      getAwsI3en3xBless,
	"hp_2drives":            getHpTwoDrives,
	"oci_bless_multitenant": OciBlessMultitenant,
}

func getOciDev(ctx context.Context, reader client.Reader, nodeSelector map[string]string) (Topology, error) {
	// get nodes via reader
	nodeNames, err := getNodeNames(ctx, reader)
	if err != nil {
		return Topology{}, err
	}
	return Topology{
		Drives:          []string{"/dev/oracleoci/oraclevdb", "/dev/oracleoci/oraclevdc"},
		Nodes:           nodeNames,
		MinCore:         2, // TODO: How to determine, other then querying machines?
		CoreStep:        2,
		MaxCore:         31,
		ForcedCpuPolicy: v1alpha1.CpuPolicyDedicatedHT,
		MaxS3Containers: 2,
	}, nil
}

func OciBlessMultitenant(ctx context.Context, reader client.Reader, nodeSelector map[string]string) (Topology, error) {
	// get nodes via reader
	nodeNames, err := getNodeNames(ctx, reader)
	if err != nil {
		return Topology{}, err
	}
	return Topology{
		Drives:          []string{"/dev/oracleoci/oraclevdb", "/dev/oracleoci/oraclevdc"},
		Nodes:           nodeNames,
		MinCore:         2, // TODO: How to determine, other then querying machines?
		CoreStep:        2,
		MaxCore:         31,
		ForcedCpuPolicy: v1alpha1.CpuPolicyDedicatedHT,
		MaxS3Containers: 1,
	}, nil
}

func getAwsI3en6x(ctx context.Context, reader client.Reader, nodeSelector map[string]string) (Topology, error) {
	// get nodes via reader
	nodeNames, err := getNodeByAwsType(ctx, reader, "i3en.6xlarge", nodeSelector)
	if err != nil {
		return Topology{}, err
	}
	return Topology{
		Drives:          []string{"aws_0", "aws_1"}, // container-side discovery by slot num
		Nodes:           nodeNames,
		MinCore:         2, // TODO: How to determine, other then querying machines?
		CoreStep:        1,
		MaxCore:         11,
		ForcedCpuPolicy: v1alpha1.CpuPolicyDedicatedHT,
		MaxS3Containers: 1,
	}, nil
}

func getHpTwoDrives(ctx context.Context, reader client.Reader, nodeSelector map[string]string) (Topology, error) {
	// get nodes via reader
	nodeNames, err := GetNodesByLabels(ctx, reader, nil)
	if err != nil {
		return Topology{}, err
	}
	return Topology{
		Drives: []string{"/dev/nvme1n1", "/dev/nvme2n1"},
		Network: v1alpha1.NetworkSelector{
			EthDevice: "mlnx0",
		},
		Nodes:           nodeNames,
		MinCore:         2, // TODO: How to determine, other then querying machines?
		CoreStep:        1,
		MaxCore:         47,
		ForcedCpuPolicy: v1alpha1.CpuPolicyDedicatedHT,
		MaxS3Containers: 4,
		ForceSignDrives: true,
	}, nil
}

func getAwsI3en6xBless(ctx context.Context, reader client.Reader, nodeSelector map[string]string) (Topology, error) {
	// get nodes via reader
	nodeNames, err := GetNodesByLabels(ctx, reader, nodeSelector)
	if err != nil {
		return Topology{}, err
	}
	return Topology{
		Drives: []string{"aws_0", "aws_1"}, // container-side discovery by slot num
		Network: v1alpha1.NetworkSelector{
			EthSlots: []string{"aws_0", "aws_1", "aws_2", "aws_3", "aws_4", "aws_5", "aws_6"},
		},
		//Network: v1alpha1.NetworkSelector{
		//	EthDevice: "ens5",
		//	UdpMode:   true,
		//},
		Nodes:                nodeNames,
		MinCore:              2, // TODO: How to determine, other then querying machines?
		CoreStep:             1,
		MaxCore:              23,
		ForcedCpuPolicy:      v1alpha1.CpuPolicyDedicatedHT,
		MaxS3Containers:      2,
		NodeConfigMapPattern: "node-config-%s",
	}, nil
}

func getAwsI3en3xBless(ctx context.Context, reader client.Reader, nodeSelector map[string]string) (Topology, error) {
	// get nodes via reader
	nodeNames, err := GetNodesByLabels(ctx, reader, nodeSelector)
	if err != nil {
		return Topology{}, err
	}
	return Topology{
		Drives: []string{"aws_0"}, // container-side discovery by slot num
		Network: v1alpha1.NetworkSelector{
			EthSlots: []string{"aws_0", "aws_1", "aws_2"},
		},
		Nodes:                nodeNames,
		MinCore:              2, // TODO: How to determine, other then querying machines?
		CoreStep:             1,
		MaxCore:              11,
		ForcedCpuPolicy:      v1alpha1.CpuPolicyDedicatedHT,
		MaxS3Containers:      1,
		NodeConfigMapPattern: "node-config-%s",
	}, nil
}

func blessUdpi3en6x(ctx context.Context, reader client.Reader, nodeSelector map[string]string) (Topology, error) {
	// get nodes via reader
	nodeNames, err := GetNodesByLabels(ctx, reader, nodeSelector)
	if err != nil {
		return Topology{}, err
	}
	return Topology{
		Drives:          []string{"aws_0", "aws_1"}, // container-side discovery by slot num
		Nodes:           nodeNames,
		MinCore:         2, // TODO: How to determine, other then querying machines?
		CoreStep:        2,
		MaxCore:         23,
		ForcedCpuPolicy: v1alpha1.CpuPolicyDedicatedHT,
		MaxS3Containers: 2,
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

func GetNodesByLabels(ctx context.Context, reader client.Reader, selector map[string]string) ([]string, error) {
	nodes := &v1.NodeList{}
	labels := client.MatchingLabels{}
	if selector != nil {
		for k, v := range selector {
			labels[k] = v
		}
	}
	listOpts := []client.ListOption{
		labels,
	}

	if err := reader.List(ctx, nodes, listOpts...); err != nil {
		return nil, err
	}
	var nodeNames []string
	for _, node := range nodes.Items {
		nodeNames = append(nodeNames, node.Name)
	}
	return nodeNames, nil
}

func getNodeByAwsType(ctx context.Context, reader client.Reader, instanceType string, selector map[string]string) ([]string, error) {
	nodes := &v1.NodeList{}
	labels := client.MatchingLabels{"node.kubernetes.io/instance-type": instanceType}
	if selector != nil {
		for k, v := range selector {
			labels[k] = v
		}
	}
	listOpts := []client.ListOption{
		labels,
	}

	if err := reader.List(ctx, nodes, listOpts...); err != nil {
		return nil, err
	}
	var nodeNames []string
	for _, node := range nodes.Items {
		nodeNames = append(nodeNames, node.Name)
	}
	return nodeNames, nil
}

func (t *Topology) GetAllNodesDrives(nodeName string) []string {
	// TODO: Replace all usages with this, stub to allow more dynamic allocations
	return t.Drives
}
