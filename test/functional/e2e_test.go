package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestWekaCluster(t *testing.T) {
	ctx := context.Background()
	ctx, _, done := instrumentation.GetLogSpan(ctx, "TestWekaCluster")
	defer done()

	st := setup(t, ctx)
	defer st.teardown(t)

	// -- put tests here ---
	t.Run("Preconditions", (&Precondition{SystemTest: *st}).Run)
	t.Run("Helm Chart", (&Chart{SystemTest: *st}).Run)
	t.Run("Driver Builder", (&DriverBuilder{SystemTest: *st}).Run)
	t.Run("Weka Cluster", (&Cluster{SystemTest: *st}).CreateCluster)
	t.Run("Validate Startup Completed", (&Cluster{SystemTest: *st}).ValidateStartupCompleted)
}

func TestWekaClusterWithDelayedDriver(t *testing.T) {
	ctx := context.Background()
	ctx, _, done := instrumentation.GetLogSpan(ctx, "TestWekaCluster")
	defer done()

	st := setup(t, ctx)
	defer st.teardown(t)

	// -- put tests here ---
	t.Run("Preconditions", (&Precondition{SystemTest: *st}).Run)
	t.Run("Helm Chart", (&Chart{SystemTest: *st}).Run)
	t.Run("Weka Cluster", (&Cluster{SystemTest: *st}).CreateCluster)
	t.Run("Driver Builder", (&DriverBuilder{SystemTest: *st}).Run)
	t.Run("Validate Startup Completed", (&Cluster{SystemTest: *st}).ValidateStartupCompleted)
}

func setup(t *testing.T, ctx context.Context) *SystemTest {
	kubebuilderRelease := "1.26.0"
	kubebuilderOs := runtime.GOOS
	kubebuilderArch := runtime.GOARCH
	kubebuilderVersion := fmt.Sprintf("%s-%s-%s", kubebuilderRelease, kubebuilderOs, kubebuilderArch)
	os.Setenv("KUBEBUILDER_ASSETS", filepath.Join("..", "..", "..", "..", "bin", "k8s", kubebuilderVersion))

	os.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc.cluster.local")
	os.Setenv("KUBERNETES_SERVICE_PORT", "443")
	os.Setenv("UNIT_TEST", "true")

	environment := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "charts", "weka-operator", "crds")},
		ErrorIfCRDPathMissing: true,
		UseExistingCluster:    func(b bool) *bool { return &b }(true),
	}
	cfg, err := environment.Start()
	if err != nil {
		t.Errorf("failed to start test environment: %v", err)
		if os.Getenv("KUBECONFIG") == "" {
			t.Errorf("Warning: KUBECONFIG is not set")
			t.SkipNow()
		}
	}

	if err := wekav1alpha1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatalf("failed to add scheme: %v", err)
	}

	client, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	st := &SystemTest{
		Client:          client,
		Ctx:             ctx,
		Cfg:             cfg,
		Namespace:       "default",
		SystemNamespace: "weka-operator-system",
		ClusterName:     "ft-cluster",
		Image:           "quay.io/weka.io/weka-in-container:4.2.7.64-s3multitenancy.2",

		environment: environment,
	}

	return st
}

func (st *SystemTest) teardown(t *testing.T) {
	// -- Tear down --
	if err := st.environment.Stop(); err != nil {
		t.Fatalf("failed to stop test environment: %v", err)
	}
}
