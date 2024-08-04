package allocator

import (
	"context"
	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/pkg/util"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sync"
	"time"
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
type NodeInfoGetter func(ctx context.Context, NodeName v1alpha1.NodeName) (*discovery.DiscoveryNodeInfo, error)
type DriveGetter func(ctx context.Context, nodeName v1alpha1.NodeName) ([]string, error)

type Topology struct {
	Drives               []string                 `json:"drives"`
	Nodes                []string                 `json:"availableHosts"`
	Network              v1alpha1.NetworkSelector `json:"network"`
	ForcedCpuPolicy      v1alpha1.CpuPolicy       `json:"forcedCpuPolicy"`
	MaxS3Containers      int                      `json:"maxS3Containers"`
	NodeConfigMapPattern string                   `json:"nodeConfigMap"`
	ForceSignDrives      bool                     `json:"forceSignDrives"`
	K8sClient            client.Client            `json:"-"`
	nodeInfoGetter       NodeInfoGetter
	driveGetter          DriveGetter
	nodeInfoCache        map[v1alpha1.NodeName]cachedNodeInfo
	drivesCache          map[v1alpha1.NodeName]*cachedDrives
	nodeCacheMutex       sync.Mutex
}

type cachedNodeInfo struct {
	info      *discovery.DiscoveryNodeInfo
	expiresAt time.Time
}

type cachedDrives struct {
	drives    []string
	expiresAt time.Time
}

func (k *Topology) getNodeInfoFromCache(ctx context.Context, nodeName v1alpha1.NodeName) (*discovery.DiscoveryNodeInfo, error) {
	k.nodeCacheMutex.Lock()
	defer k.nodeCacheMutex.Unlock()

	if k.nodeInfoCache == nil {
		k.nodeInfoCache = make(map[v1alpha1.NodeName]cachedNodeInfo)
	}

	now := time.Now()

	cachedInfo, exists := k.nodeInfoCache[nodeName]
	if !exists || now.After(cachedInfo.expiresAt) {
		nodeInfo, err := k.nodeInfoGetter(ctx, nodeName)
		if err != nil {
			return nil, err
		}
		k.nodeInfoCache[nodeName] = cachedNodeInfo{
			info:      nodeInfo,
			expiresAt: now.Add(time.Minute),
		}
		cachedInfo = k.nodeInfoCache[nodeName]
	}

	return cachedInfo.info, nil
}

func (k *Topology) GetAvailableCpus(ctx context.Context, nodeName v1alpha1.NodeName) (int, error) {
	nodeInfo, err := k.getNodeInfoFromCache(ctx, nodeName)
	if err != nil {
		return 0, err
	}
	return nodeInfo.NumCpus, nil
}

func (k *Topology) GetAvailableDrives(ctx context.Context, nodeName v1alpha1.NodeName) (int, error) {
	nodeInfo, err := k.getNodeInfoFromCache(ctx, nodeName)
	if err != nil {
		return 0, err
	}

	return nodeInfo.GetDrivesNumber(), nil
}

// NewK8sClusterLevelConfig creates a new Topology
var DevboxWekabox = Topology{
	Drives: []string{"/dev/nvme0n1", "/dev/sdc", "/dev/sdb", "/dev/sdd", "/dev/sde", "/dev/sdf"}, // skipping N1, since it's used for local storage
	Nodes:  []string{"wekabox14.lan", "wekabox15.lan", "wekabox16.lan", "wekabox17.lan", "wekabox18.lan"},
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

	if config.ComputeContainers == nil {
		config.ComputeContainers = util.IntRef(6)
	}

	if config.DriveContainers == nil {
		config.DriveContainers = util.IntRef(6)
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
		ComputeContainers:   *config.ComputeContainers,
		DriveContainers:     *config.DriveContainers,
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

type topologyGetter func(ctx context.Context, reader client.Reader, nodeSelector map[string]string, nodeInfoGetter NodeInfoGetter) (*Topology, error)

var Topologies = map[string]topologyGetter{
	"dev_wekabox": func(ctx context.Context, reader client.Reader, nodeSelector map[string]string, nodeInfoGetter NodeInfoGetter) (*Topology, error) {
		return &DevboxWekabox, nil
	},
	"discover_oci":          getOciDev,
	"discover_aws_i3en6x":   getAwsI3en6x,
	"aws_i3en6x_udp_bless":  blessUdpi3en6x,
	"aws_i3en6x_bless":      getAwsI3en6xBless,
	"aws_i3en3x_bless":      getAwsI3en3xBless,
	"hp_2drives":            getHpTwoDrives,
	"oci_bless_multitenant": OciBlessMultitenant,
	"discover_all":          discoverAllTopology,
}

func discoverAllTopology(ctx context.Context, reader client.Reader, selector map[string]string, nodeInfoGetter NodeInfoGetter) (*Topology, error) {
	nodeNames, err := GetNodesByLabels(ctx, reader, selector)
	if err != nil {
		return nil, err
	}

	return &Topology{
		Nodes:           nodeNames,
		ForcedCpuPolicy: v1alpha1.CpuPolicyShared, // Default to shared CPU policy
		MaxS3Containers: 1,                        // Default to 1 S3 container
		nodeInfoGetter:  nodeInfoGetter,
		driveGetter:     discoverDrives, // Implement this function to discover drives
	}, nil
}

func discoverDrives(ctx context.Context, nodeName v1alpha1.NodeName) ([]string, error) {
	// TODO: Implement drive discovery logic
	// This function should return a list of discovered drives for the given node
	return []string{}, nil
}

func getOciDev(ctx context.Context, reader client.Reader, nodeSelector map[string]string, nodeInfoGetter NodeInfoGetter) (*Topology, error) {
	// get nodes via reader
	nodeNames, err := getNodeNames(ctx, reader)
	if err != nil {
		return nil, err
	}
	return &Topology{
		Drives:          []string{"/dev/oracleoci/oraclevdb", "/dev/oracleoci/oraclevdc"},
		Nodes:           nodeNames,
		ForcedCpuPolicy: v1alpha1.CpuPolicyDedicatedHT,
		MaxS3Containers: 2,
		nodeInfoGetter:  nodeInfoGetter,
	}, nil
}

func OciBlessMultitenant(ctx context.Context, reader client.Reader, nodeSelector map[string]string, nodeInfoGetter NodeInfoGetter) (*Topology, error) {
	// get nodes via reader
	nodeNames, err := getNodeNames(ctx, reader)
	if err != nil {
		return nil, err
	}
	return &Topology{
		Drives:          []string{"/dev/oracleoci/oraclevdb", "/dev/oracleoci/oraclevdc"},
		Nodes:           nodeNames,
		ForcedCpuPolicy: v1alpha1.CpuPolicyDedicatedHT,
		MaxS3Containers: 1,
		nodeInfoGetter:  nodeInfoGetter,
	}, nil
}

func getAwsI3en6x(ctx context.Context, reader client.Reader, nodeSelector map[string]string, nodeInfoGetter NodeInfoGetter) (*Topology, error) {
	// get nodes via reader
	nodeNames, err := getNodeByAwsType(ctx, reader, "i3en.6xlarge", nodeSelector)
	if err != nil {
		return nil, err
	}
	return &Topology{
		Drives:          []string{"aws_0", "aws_1"}, // container-side discovery by slot num
		Nodes:           nodeNames,
		ForcedCpuPolicy: v1alpha1.CpuPolicyDedicatedHT,
		MaxS3Containers: 1,
		nodeInfoGetter:  nodeInfoGetter,
	}, nil
}

func getHpTwoDrives(ctx context.Context, reader client.Reader, nodeSelector map[string]string, nodeInfoGetter NodeInfoGetter) (*Topology, error) {
	// get nodes via reader
	nodeNames, err := GetNodesByLabels(ctx, reader, nil)
	if err != nil {
		return nil, err
	}
	return &Topology{
		Drives: []string{"/dev/nvme1n1", "/dev/nvme2n1"},
		Network: v1alpha1.NetworkSelector{
			EthDevice: "mlnx0",
		},
		Nodes:           nodeNames,
		ForcedCpuPolicy: v1alpha1.CpuPolicyDedicatedHT,
		MaxS3Containers: 4,
		ForceSignDrives: true,
		nodeInfoGetter:  nodeInfoGetter,
	}, nil
}

func getAwsI3en6xBless(ctx context.Context, reader client.Reader, nodeSelector map[string]string, nodeInfoGetter NodeInfoGetter) (*Topology, error) {
	// get nodes via reader
	nodeNames, err := GetNodesByLabels(ctx, reader, nodeSelector)
	if err != nil {
		return nil, err
	}
	return &Topology{
		Drives: []string{"aws_0", "aws_1"}, // container-side discovery by slot num
		Network: v1alpha1.NetworkSelector{
			EthSlots: []string{"aws_0", "aws_1", "aws_2", "aws_3", "aws_4", "aws_5", "aws_6"},
		},
		//Network: v1alpha1.NetworkSelector{
		//	EthDevice: "ens5",
		//	UdpMode:   true,
		//},
		Nodes:                nodeNames,
		ForcedCpuPolicy:      v1alpha1.CpuPolicyDedicatedHT,
		MaxS3Containers:      2,
		NodeConfigMapPattern: "node-config-%s",
		nodeInfoGetter:       nodeInfoGetter,
	}, nil
}

func getAwsI3en3xBless(ctx context.Context, reader client.Reader, nodeSelector map[string]string, nodeInfoGetter NodeInfoGetter) (*Topology, error) {
	// get nodes via reader
	nodeNames, err := GetNodesByLabels(ctx, reader, nodeSelector)
	if err != nil {
		return nil, err
	}
	return &Topology{
		Drives: []string{"aws_0"}, // container-side discovery by slot num
		Network: v1alpha1.NetworkSelector{
			EthSlots: []string{"aws_0", "aws_1", "aws_2"},
		},
		Nodes:                nodeNames,
		ForcedCpuPolicy:      v1alpha1.CpuPolicyDedicatedHT,
		MaxS3Containers:      1,
		NodeConfigMapPattern: "node-config-%s",
		nodeInfoGetter:       nodeInfoGetter,
	}, nil
}

func blessUdpi3en6x(ctx context.Context, reader client.Reader, nodeSelector map[string]string, nodeInfoGetter NodeInfoGetter) (*Topology, error) {
	// get nodes via reader
	nodeNames, err := GetNodesByLabels(ctx, reader, nodeSelector)
	if err != nil {
		return nil, err
	}
	return &Topology{
		Drives:          []string{"aws_0", "aws_1"}, // container-side discovery by slot num
		Nodes:           nodeNames,
		ForcedCpuPolicy: v1alpha1.CpuPolicyDedicatedHT,
		MaxS3Containers: 2,
		nodeInfoGetter:  nodeInfoGetter,
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

func (k *Topology) GetAllDrives(ctx context.Context, node v1alpha1.NodeName) ([]string, error) {
	// TODO: Implement this function, how will we get information? Probably cluster-level selector and not topology
	// But maybe this should be a part of the NodeInfo? Lets think towards what we actually want to achieve
	// TODO: Elaborate, but 1) auto-discovery of signed drives. This is pre-provisioned physical systems. No PotentialDrives list.
	// Discovery could find them. Discovery might be a correct place to implement
	// TODO: 2) Discovery and force-sign for cloud. We need instruction that this is cloud or different such system

	// Options:
	//		- Selector on cluster - most flexible
	// 		- pre-configured nodeInfo configmap(this is good one for bliss, solves everything at once, nics including)
	//      - what we also could do, consolidate everything into node configmap. Make this a default, and either creating this configmap from pre-created by user configmap or cluster selector or node-nodeinfo discovered
	//      - basically, it brings it down to topology. that right now centric entity for all this options, and we need to detach it
	return k.driveGetter(ctx, node)
}
