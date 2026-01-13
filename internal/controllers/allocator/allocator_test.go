package allocator

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/pkg/util"
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	writer := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.TimeOnly}
	zeroLogger := zerolog.New(writer).Level(zerolog.DebugLevel).With().Timestamp().Logger()
	logger := zerologr.New(&zeroLogger)

	shutdown, err := instrumentation.SetupOTelSDK(ctx, "allocator-tests", "", logger)
	if err != nil {
		panic(err)
	}
	code := m.Run()
	_ = shutdown(ctx)
	os.Exit(code)
}

func newTestAllocator(existingClusters []weka.WekaCluster) Allocator {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = weka.AddToScheme(scheme)

	clientBuilder := fake.NewClientBuilder().WithScheme(scheme)
	for i := range existingClusters {
		clientBuilder = clientBuilder.WithStatusSubresource(&existingClusters[i])
		clientBuilder = clientBuilder.WithObjects(&existingClusters[i])
	}

	return &ResourcesAllocator{
		client: clientBuilder.Build(),
	}
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

func testWekaClusterWithPorts(name, namespace string, basePort, portRange int) *weka.WekaCluster {
	return &weka.WekaCluster{
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Status: weka.WekaClusterStatus{
			Ports: weka.ClusterPorts{
				BasePort:  basePort,
				PortRange: portRange,
			},
		},
	}
}

func TestAllocatorGlobalRanges(t *testing.T) {
	ctx := context.Background()

	// Create existing clusters with port allocations in their Status
	existingClusters := []weka.WekaCluster{
		*testWekaClusterWithPorts("cluster-obs-test", "default", 46000, 500),
		*testWekaClusterWithPorts("weka-infra", "infra", 35000, 500),
		*testWekaClusterWithPorts("scalenodes", "default", 35500, 500),
		*testWekaClusterWithPorts("crocodile", "weka-operator-system", 36500, 500),
	}

	allocator := newTestAllocator(existingClusters)

	// Feature flags for the test (empty flags = default port range)
	featureFlags := &domain.FeatureFlags{}

	// Create a new cluster to allocate
	cluster1 := testWekaCluster("cluster-1")

	err := allocator.AllocateClusterRange(ctx, cluster1, featureFlags)
	if err != nil {
		t.Errorf("Failed to allocate cluster range: %v", err)
		return
	}

	// The existing clusters occupy:
	// - 35000-35499 (weka-infra)
	// - 35500-35999 (scalenodes)
	// - 36500-36999 (crocodile)
	// - 46000-46499 (cluster-obs-test)
	// So the first available gap is 36000-36499
	if cluster1.Status.Ports.BasePort != 36000 {
		t.Errorf("Expected base port 36000, got: %v", cluster1.Status.Ports.BasePort)
		return
	}
}
