package services

import (
	"testing"

	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/testutil"
)

func TestEnsureNoContainers(t *testing.T) {
	ctx, _, done := instrumentation.GetLogSpan(pkgCtx, "TestEnsureNoContainers")
	defer done()

	manager, err := testutil.TestingManager()
	if err != nil {
		t.Fatalf("TestingManager() error = %v, want nil", err)
	}

	clusterService := &wekaClusterService{}
	clusterService.Cluster = &wekav1alpha1.WekaCluster{}
	clusterService.Client = manager.GetClient()

	if err := clusterService.EnsureNoContainers(ctx, "mode"); err != nil {
		t.Errorf("EnsureNoContainers() error = %v", err)
	}
}
