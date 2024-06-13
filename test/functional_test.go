package test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/test/e2e/fixtures"
	"github.com/weka/weka-operator/test/e2e/services"
)

func TestWekaCluster(t *testing.T) {
	ctx := context.Background()
	ctx, _, done := instrumentation.GetLogSpan(ctx, "TestWekaCluster")
	defer done()

	// st := setup(t, ctx)
	// defer st.teardown(t)

	clusterTest := setup(t, ctx)

	// -- put tests here ---
	t.Run("Preconditions", (&Precondition{ClusterTest: *clusterTest}).Run(ctx))
	t.Run("Helm Chart", (&Chart{ClusterTest: *clusterTest}).Run(ctx))
	t.Run("Driver Builder", (&DriverBuilder{ClusterTest: *clusterTest}).Run(ctx))
	t.Run("Weka Cluster", (&Cluster{ClusterTest: *clusterTest}).CreateCluster(ctx))
	t.Run("Validate Startup Completed", (&Cluster{ClusterTest: *clusterTest}).ValidateStartupCompleted(ctx))
}

// func TestWekaClusterWithDelayedDriver(t *testing.T) {
// ctx := context.Background()
// ctx, _, done := instrumentation.GetLogSpan(ctx, "TestWekaCluster")
// defer done()

// st := setup(t, ctx)
// defer st.teardown(t)

//// -- put tests here ---
//t.Run("Preconditions", (&Precondition{SystemTest: *st}).Run)
//t.Run("Helm Chart", (&Chart{SystemTest: *st}).Run)
//t.Run("Weka Cluster", (&Cluster{SystemTest: *st}).CreateCluster)
//t.Run("Driver Builder", (&DriverBuilder{SystemTest: *st}).Run)
//t.Run("Validate Startup Completed", (&Cluster{SystemTest: *st}).ValidateStartupCompleted)
//}

func setup(t *testing.T, ctx context.Context) *ClusterTest {
	kubebuilderRelease := "1.26.0"
	kubebuilderOs := runtime.GOOS
	kubebuilderArch := runtime.GOARCH
	kubebuilderVersion := fmt.Sprintf("%s-%s-%s", kubebuilderRelease, kubebuilderOs, kubebuilderArch)
	os.Setenv("KUBEBUILDER_ASSETS", filepath.Join("..", "..", "..", "..", "bin", "k8s", kubebuilderVersion))

	os.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc.cluster.local")
	os.Setenv("KUBERNETES_SERVICE_PORT", "443")
	os.Setenv("UNIT_TEST", "true")

	jobless := services.NewJobless(ctx)
	wekaClusterName := "ft-cluster"
	kubeConfig := os.Getenv("KUBECONFIG")
	k8s := services.NewKubernetes(jobless, wekaClusterName, kubeConfig)
	k8s.Setup(ctx)

	awsClusterName := os.Getenv("CLUSTER_NAME")
	st := &ClusterTest{
		Jobless: jobless,
		Image:   "quay.io/weka.io/weka-in-container:4.2.7.64-s3multitenancy.7",
		Cluster: &fixtures.Cluster{
			Name:              awsClusterName,
			WekaClusterName:   wekaClusterName,
			OperatorNamespace: "weka-operator-system",
		},
	}

	return st
}

//func (st *SystemTest) teardown(t *testing.T) {
//// -- Tear down --
//if err := st.Kubernetes.TearDown(st.Ctx); err != nil {
//t.Fatalf("failed to stop test environment: %v", err)
//}
//}
