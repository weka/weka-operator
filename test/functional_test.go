package test

import (
	"context"
	"os"
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
	jobless := services.NewJobless(ctx, *BlissVersion)
	wekaClusterName := "ft-cluster"
	kubeConfig := os.Getenv("KUBECONFIG")
	k8s := services.NewKubernetes(jobless, wekaClusterName, kubeConfig)
	k8s.Setup(ctx)

	awsClusterName := os.Getenv("CLUSTER_NAME")
	st := &ClusterTest{
		Jobless: jobless,
		Image:   "quay.io/weka.io/weka-in-container:4.3.4",
		Cluster: &fixtures.Cluster{
			Name:              awsClusterName,
			WekaClusterName:   wekaClusterName,
			OperatorNamespace: "weka-operator-system",
			Kubernetes:        k8s,
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
