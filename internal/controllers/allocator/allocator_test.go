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
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
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

func newTestAllocator(ctx context.Context, numDrives int) (Allocator, *InMemoryConfigStore, error) {
	cs := NewInMemoryConfigStore()

	// Create fake client with proper scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = weka.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	return &ResourcesAllocator{
		configStore: cs,
		client:      fakeClient,
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

	// Setup feature flags cache for the test (using empty image as testWekaCluster doesn't set it)
	_ = services.FeatureFlagsCache.SetFeatureFlags(ctx, "", &domain.FeatureFlags{})

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
