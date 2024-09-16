package controllers

import (
	"context"
	"os"
	"testing"

	"github.com/pkg/errors"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/testutil"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestFetchCluster(t *testing.T) {
	ctx := context.Background()

	manager, err := testingManager()
	if err != nil {
		t.Fatalf("TestingManager() error = %v, want nil", err)
	}
	loop := &wekaClusterReconcilerLoop{
		Manager: manager,
	}

	tests := []struct {
		name      string
		namespace string
		hasError  bool
	}{
		{
			name:      "test-cluster",
			namespace: "test-namespace",
			hasError:  false,
		},
		{
			name:      "does-not-exist",
			namespace: "test-namespace",
			hasError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.name,
					Namespace: tt.namespace,
				},
			}

			err := loop.FetchCluster(ctx, req)
			if tt.hasError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.hasError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			actual := loop.cluster
			if actual == nil {
				t.Fatalf("expected cluster to be set")
			}
			if actual.Name != "test-cluster" {
				t.Errorf("unexpected cluster name: %s", actual.Name)
			}
			if actual.Namespace != "test-namespace" {
				t.Errorf("unexpected cluster namespace: %s", actual.Namespace)
			}
		})
	}
}

func TestHandleDeletion(t *testing.T) {
	os.Setenv("OPERATOR_DEV_MODE", "true")
	ctx := context.Background()
	manager, err := testingManager()
	if err != nil {
		t.Fatalf("TestingManager() error = %v, want nil", err)
	}
	key := types.NamespacedName{Name: "test-cluster", Namespace: "test-namespace"}
	cluster := &wekav1alpha1.WekaCluster{}
	if err := manager.GetClient().Get(ctx, key, cluster); err != nil {
		t.Fatalf("failed to get cluster: %v", err)
	}

	if controllerutil.ContainsFinalizer(cluster, "test-finalizer") {
		t.Fatalf("expected finalizer to not yet be set")
	}

	controllerutil.AddFinalizer(cluster, WekaFinalizer)
	if err := manager.GetClient().Update(ctx, cluster); err != nil {
		t.Fatalf("failed to update cluster: %v", err)
	}

	clusterService := services.NewWekaClusterService(manager, cluster)

	loop := &wekaClusterReconcilerLoop{
		Manager:        manager,
		cluster:        cluster,
		clusterService: clusterService,
	}

	if err := loop.HandleDeletion(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := manager.GetClient().Get(ctx, key, cluster); err != nil {
		t.Fatalf("failed to get cluster: %v", err)
	}
	if controllerutil.ContainsFinalizer(cluster, WekaFinalizer) {
		t.Error("expected finalizer to be removed")
	}
	t.Skip("TODO: ensure containers are deleted")
}

func TestInitState(t *testing.T) {
	ctx := context.Background()
	manager, err := testingManager()
	if err != nil {
		t.Fatalf("TestingManager() error = %v, want nil", err)
	}
	key := types.NamespacedName{Name: "test-cluster", Namespace: "test-namespace"}
	cluster := &wekav1alpha1.WekaCluster{}
	if err := manager.GetClient().Get(ctx, key, cluster); err != nil {
		t.Fatalf("failed to get cluster: %v", err)
	}

	if controllerutil.ContainsFinalizer(cluster, WekaFinalizer) {
		t.Fatalf("expected finalizer to not yet be set")
	}
	if cluster.Status.LastAppliedImage != "" {
		t.Fatalf("expected last applied image to be empty")
	}

	loop := &wekaClusterReconcilerLoop{
		Manager: manager,
		cluster: cluster,
	}

	if err := loop.InitState(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	actual := &wekav1alpha1.WekaCluster{}
	if err := manager.GetClient().Get(ctx, key, actual); err != nil {
		t.Fatalf("failed to get cluster: %v", err)
	}
	if actual.Status.LastAppliedImage == "" {
		t.Error("expected last applied image to be set got &#v", actual.Status)
	}
	if !controllerutil.ContainsFinalizer(actual, WekaFinalizer) {
		t.Error("expected finalizer to be set")
	}
}

func TestEnsureWekaContainers(t *testing.T) {
	os.Setenv("OPERATOR_DEV_MODE", "true")

	ctx := context.Background()
	manager, err := testingManager()
	if err != nil {
		t.Fatalf("TestingManager() error = %v, want nil", err)
	}
	key := types.NamespacedName{Name: "test-cluster", Namespace: "test-namespace"}
	cluster := &wekav1alpha1.WekaCluster{}
	if err := manager.GetClient().Get(ctx, key, cluster); err != nil {
		t.Fatalf("failed to get cluster: %v", err)
	}

	clusterService := services.NewWekaClusterService(manager, cluster)

	loop := &wekaClusterReconcilerLoop{
		Manager:        manager,
		cluster:        cluster,
		clusterService: clusterService,
	}

	if loop.containers != nil {
		t.Fatalf("expected containers to be nil")
	}

	if err := loop.EnsureWekaContainers(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if loop.containers == nil {
		t.Fatalf("expected containers to be set")
	}
	if len(loop.containers) == 0 {
		t.Fatalf("expected containers to be non-empty")
	}
	for _, container := range loop.containers {
		t.Logf("Container: %+v", container)
	}
}

func TestAllContainersReady(t *testing.T) {
	ctx := context.Background()
	manager, err := testingManager()
	if err != nil {
		t.Fatalf("TestingManager() error = %v, want nil", err)
	}
	key := types.NamespacedName{Name: "test-cluster", Namespace: "test-namespace"}
	cluster := &wekav1alpha1.WekaCluster{}
	if err := manager.GetClient().Get(ctx, key, cluster); err != nil {
		t.Fatalf("failed to get cluster: %v", err)
	}

	clusterService := services.NewWekaClusterService(manager, cluster)

	tests := []struct {
		name       string
		containers []*wekav1alpha1.WekaContainer
		expected   error
	}{
		{
			name:       "no containers",
			containers: []*wekav1alpha1.WekaContainer{},
			expected:   nil,
		},
		{
			name: "all containers ready",
			containers: []*wekav1alpha1.WekaContainer{
				{
					Status: wekav1alpha1.WekaContainerStatus{
						Status: "Running",
					},
				},
			},
			expected: nil,
		},
		{
			name: "not all containers ready",
			containers: []*wekav1alpha1.WekaContainer{
				{
					Status: wekav1alpha1.WekaContainerStatus{
						Status: "Running",
					},
				},
				{
					Status: wekav1alpha1.WekaContainerStatus{
						Status: "something else",
					},
				},
			},
			expected: lifecycle.NewWaitError(errors.New("containers not ready")),
		},
	}

	for _, tt := range tests {
		name := tt.name
		t.Run(name, func(t *testing.T) {
			containers := tt.containers
			loop := &wekaClusterReconcilerLoop{
				Manager:        manager,
				cluster:        cluster,
				clusterService: clusterService,
				containers:     containers,
			}

			err := loop.AllContainersReady(ctx)
			if !errors.Is(err, tt.expected) {
				t.Errorf("unexpected error - want: %v, got: %v", tt.expected, err)
			}
		})
	}
}

func TestFormCluster(t *testing.T) {
	os.Setenv("OPERATOR_DEV_MODE", "true")

	ctx := context.Background()
	manager, err := testingManager()
	if err != nil {
		t.Fatalf("TestingManager() error = %v, want nil", err)
	}
	key := types.NamespacedName{Name: "test-cluster", Namespace: "test-namespace"}
	cluster := &wekav1alpha1.WekaCluster{}
	if err := manager.GetClient().Get(ctx, key, cluster); err != nil {
		t.Fatalf("failed to get cluster: %v", err)
	}

	execService := testutil.NewTestingExecService()
	clusterService := services.NewTestingWekaClusterService(manager, execService, cluster)

	loop := &wekaClusterReconcilerLoop{
		Manager:        manager,
		cluster:        cluster,
		clusterService: clusterService,
	}

	if err := loop.EnsureWekaContainers(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := loop.FormCluster(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if loop.containers == nil {
		t.Fatalf("expected containers to be set")
	}
	if len(loop.containers) == 0 {
		t.Fatalf("expected containers to be non-empty")
	}
	for _, container := range loop.containers {
		t.Logf("Container: %+v", container)
	}
}
