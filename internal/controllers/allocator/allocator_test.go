package allocator

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/pkg/util"
	"gopkg.in/yaml.v3"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newTestAllocatorInfoGetter(numDrives int) NodeInfoGetter {
	return func(ctx context.Context, nodeName weka.NodeName) (*AllocatorNodeInfo, error) {
		drives := []string{}
		for i := 0; i < numDrives; i++ {
			drives = append(drives, fmt.Sprintf("some-longer-drive-%d", i))
		}

		return &AllocatorNodeInfo{
			AvailableDrives: drives,
		}, nil
	}
}

func newTestAllocator(ctx context.Context, numDrives int) (Allocator, *InMemoryConfigStore, error) {
	cs := NewInMemoryConfigStore()
	return &ResourcesAllocator{
		configStore:    cs,
		nodeInfoGetter: newTestAllocatorInfoGetter(numDrives),
	}, cs, nil
}

func testWekaCluster(name string) *weka.WekaCluster {
	return &weka.WekaCluster{
		Spec: weka.WekaClusterSpec{
			Template: "dynamic",
			Dynamic: &weka.WekaConfig{
				DriveContainers:   util.IntRef(5),
				ComputeContainers: util.IntRef(5),
				NumDrives:         4,
			},
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: "testNamespace",
		},
	}
}

// every allocated container will increment the placement by one, nodeNamePool will rotate as needed when reaching the end, by module basically
var rolePlacement = map[string]int{
	"drive":   0,
	"compute": 0,
}

func buildTestContainers(cluster *weka.WekaCluster, nodeNamePool []weka.NodeName) []*weka.WekaContainer {
	containers := []*weka.WekaContainer{}
	pairs := map[string]int{
		"drive":   *cluster.Spec.Dynamic.DriveContainers,
		"compute": *cluster.Spec.Dynamic.ComputeContainers,
	}

	for mode, count := range pairs {
		for i := 0; i < count; i++ {
			placement := nodeNamePool[rolePlacement[mode]]
			rolePlacement[mode] = (rolePlacement[mode] + 1) % len(nodeNamePool)
			numDrives := 0
			switch mode {
			case "drive":
				numDrives = cluster.Spec.Dynamic.NumDrives
			}
			containers = append(containers, &weka.WekaContainer{
				Spec: weka.WekaContainerSpec{
					NodeAffinity: placement,
					Mode:         mode,
					NumDrives:    numDrives,
				},
				ObjectMeta: v1.ObjectMeta{
					Name:      NewContainerName(mode),
					Namespace: "testNamespace",
				},
			})
		}
	}
	return containers
}

func buildTestClusters(numClusters int) []*weka.WekaCluster {
	clusters := []*weka.WekaCluster{}
	for i := 0; i < numClusters; i++ {
		clusters = append(clusters, testWekaCluster(fmt.Sprintf("cluster-%d", i)))
	}
	return clusters
}

func setupLogging(ctx context.Context) (context.Context, func(context.Context) error, error) {
	var logger logr.Logger
	if os.Getenv("DEBUG") == "true" {
		// Debug logger
		writer := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.TimeOnly}
		zeroLogger := zerolog.New(writer).Level(zerolog.DebugLevel).With().Timestamp().Logger()
		logger = zerologr.New(&zeroLogger)
	} else {
		// Logger that drops/silences messages for unit testing
		zeroLogger := zerolog.Nop()
		logger = zerologr.New(&zeroLogger)
	}

	shutdown, err := instrumentation.SetupOTelSDK(ctx, "test-weka-operator", "", logger)
	if err != nil {
		return nil, nil, fmt.Errorf("Failed to set up OTel SDK: %v", err)
	}

	ctx, _ = instrumentation.GetLoggerForContext(ctx, &logger, "")
	return ctx, shutdown, nil
}

func TestAllocatePort(t *testing.T) {
	//TODO: Test commented out as it is using factory creating cyclic import that needs to be broken. Fix this test it is important
	ctx := context.Background()

	ctx, shutdown, err := setupLogging(ctx)
	if err != nil {
		t.Fatalf("Failed to setup test environment: %v", err)
	}
	defer shutdown(ctx)

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "TestAllocatePort")
	defer end()

	allocator, cs, err := newTestAllocator(ctx, 24)
	if err != nil {
		t.Errorf("Failed to create allocator: %v", err)
	}

	clusters := buildTestClusters(10)
	nodes := buildTestNodeNames(9)

	for _, cluster := range clusters {
		err = allocator.AllocateClusterRange(ctx, cluster)
		if err != nil {
			t.Errorf("Failed to allocate cluster range: %v", err)
			return
		}

		containers := buildTestContainers(cluster, nodes)

		err = allocator.AllocateContainers(ctx, cluster, containers)
		if err != nil {
			if failedAllocs, ok := err.(*FailedAllocations); ok {
				// we proceed despite failures, as partial might be sufficient(?)
				if len(*failedAllocs) != 0 {
					t.Errorf("some allocations have failed %v", err)
					return
				}
			} else {
				t.Errorf("Failed to allocate containers: %v", err)
				return
			}
		}

		resultAllocations, err := allocator.GetAllocations(ctx)
		if err != nil {
			t.Errorf("Failed to get allocations: %v", err)
			return
		}

		// validating that no two containers have the same agent port on the same node
		agentNodePorts := map[string]bool{}
		for _, container := range containers {
			owner := Owner{
				OwnerCluster: OwnerCluster{
					Namespace:   cluster.Namespace,
					ClusterName: cluster.Name,
				},
				Container: container.Name,
				Role:      container.Spec.Mode,
			}
			nodeName := container.GetNodeAffinity()
			nodeAlloc := resultAllocations.NodeMap[nodeName]

			ranges := nodeAlloc.AllocatedRanges[owner]
			agentPort := ranges["agent"].Base

			if !container.IsHostNetwork() || container.IsEnvoy() {
				continue
			}
			found := agentNodePorts[fmt.Sprintf("%s:%d", nodeName, agentPort)]
			if found {
				t.Errorf("Node port already allocated: %s:%d", nodeName, agentPort)
				allocations, _ := allocator.GetAllocations(ctx)
				printAsYaml(allocations)
				return
			} else {
				agentNodePorts[fmt.Sprintf("%s:%d", nodeName, agentPort)] = true
			}
		}
	}

	marshalled, _ := yaml.Marshal(cs.allocations)
	logger.Info("bytes in use by configmap", "bytes", len(marshalled))

	// compress and print copmressed size as well
	compressed, _ := compressBytes(marshalled)
	logger.Info("compressed bytes in use by configmap", "bytes", len(compressed))

	if err != nil {
		t.Errorf("Failed to compress bytes: %v", err)
		return
	}

	for _, cluster := range clusters {
		err = allocator.DeallocateCluster(ctx, cluster)
		if err != nil {
			t.Errorf("Failed to deallocate cluster: %v", err)
		}
	}
}

func buildTestNodeNames(i int) []weka.NodeName {
	nodeNames := []weka.NodeName{}
	for j := 0; j < i; j++ {
		nodeNames = append(nodeNames, weka.NodeName(fmt.Sprintf("some-longer-node-%d", j)))
	}
	return nodeNames
}

func compressBytes(input []byte) ([]byte, error) {
	var buf bytes.Buffer
	gzipWriter := gzip.NewWriter(&buf)

	// Write the input byte array to the gzip writer
	_, err := gzipWriter.Write(input)
	if err != nil {
		return nil, err
	}

	// Close the writer to flush any remaining data
	err = gzipWriter.Close()
	if err != nil {
		return nil, err
	}

	// Return the compressed data
	return buf.Bytes(), nil
}

func printAsYaml(allocations *Allocations) {
	data, _ := yaml.Marshal(allocations)
	fmt.Println(string(data))
}

func TestAllocatorGlobalRanges(t *testing.T) {
	badYaml := `clusterranges:
  cluster-obs-test;default:
    base: 46000
    size: 500
  weka-infra;infra:
    base: 35000
    size: 500
  scalenodes;default:
    base: 35500
    size: 500
  crocodile;weka-operator-system:
    base: 36500
    size: 500
allocatedranges:
  weka-infra;infra:
    lb:
      base: 35300
      size: 1
    lbAdmin:
      base: 35301
      size: 1
    s3:
      base: 35302
      size: 1
  crocodile;weka-operator-system:
    lb:
      base: 36800
      size: 1
    lbAdmin:
      base: 36801
      size: 1
    s3:
      base: 36802
      size: 1
  scalenodes;default:
    lb:
      base: 35800
      size: 1
    lbAdmin:
      base: 35801
      size: 1
    s3:
      base: 35802
      size: 1
  cluster-obs-test;default:
    lb:
      base: 46300
      size: 1
    lbAdmin:
      base: 46301
      size: 1
    s3:
      base: 46302
      size: 1`

	ctx := context.Background()
	allocator, config, err := newTestAllocator(ctx, 4)
	if err != nil {
		t.Errorf("Failed to create allocator: %v", err)
		return
	}
	//unmarshal
	startingAllocations := GlobalAllocations{}
	err = yaml.Unmarshal([]byte(badYaml), &startingAllocations)
	if err != nil {
		t.Errorf("Failed to unmarshal yaml: %v", err)
		return
	}
	config.allocations.Global = startingAllocations
	cluster1 := testWekaCluster("cluster-1")
	err = allocator.AllocateClusterRange(ctx, cluster1)
	if err != nil {
		t.Errorf("Failed to allocate cluster range: %v", err)
		return
	}
	// would expect to recieve range of 36000 here
	if cluster1.Status.Ports.BasePort != 36000 {
		t.Errorf("Failed to allocate correct base port: %v", cluster1.Status.Ports.BasePort)
		return
	}
}
