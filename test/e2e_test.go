package test

import (
	"context"
	"fmt"
	"os"

	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/test/e2e/fixtures"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

func NewE2ETest(ctx context.Context, cluster *wekav1alpha1.WekaCluster) (*E2ETest, error) {
	ctx, logger, done := instrumentation.GetLogSpan(ctx, "NewE2ETest")
	defer done()

	clusterName := cluster.Name
	wekaClusterName := cluster.Name
	operatorNamespace := "weka-operator-system"

	log.SetLogger(logger.Logger)

	testCase := &ClusterTest{
		Ctx: ctx,
		Cluster: &fixtures.Cluster{
			Name:              clusterName,
			WekaClusterName:   wekaClusterName,
			OperatorNamespace: operatorNamespace,
		},
	}

	if err := testCase.Cluster.SetupK8s(ctx); err != nil {
		return nil, fmt.Errorf("SetupK8s: %v", err)
	}

	return &E2ETest{
		Ctx:      ctx,
		Clusters: []*ClusterTest{testCase},
	}, nil
}

func ValidateTestEnvironment(ctx context.Context) error {
	_, logger, done := instrumentation.GetLogSpan(ctx, "ValidateTestEnvironment")
	defer done()

	requiredEnvVars := []string{"QUAY_USERNAME", "QUAY_PASSWORD", "KUBECONFIG"}
	for _, envVar := range requiredEnvVars {
		logger.Info("Validating environment variable", "variable", envVar)
		v := os.Getenv(envVar)
		if v == "" {
			return fmt.Errorf("%s is not set", envVar)
		}
	}
	return nil
}
