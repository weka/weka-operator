package services

import (
	"context"
	"testing"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/testutil"
)

func TestEnsureNoContainers(t *testing.T) {
	ctx := context.Background()
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

func TestFormCluster(t *testing.T) {
	ctx := context.Background()
	manager, err := testutil.TestingManager()
	if err != nil {
		t.Fatalf("TestingManager() error = %v, want nil", err)
	}

	clusterService := &wekaClusterService{}
	clusterService.Cluster = &wekav1alpha1.WekaCluster{}
	clusterService.Client = manager.GetClient()
	clusterService.ExecService = testutil.NewTestingExecService()

	containers := []*wekav1alpha1.WekaContainer{
		{
			Spec: wekav1alpha1.WekaContainerSpec{},
		},
	}

	if err := clusterService.FormCluster(ctx, containers); err != nil {
		t.Errorf("FormCluster() error = %v", err)
	}
}
