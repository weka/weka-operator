package controllers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/testutil"
)

// Each test represents a lifecycle stage of the WekaCluster

// Test from new cluster to pods ready
func TestReconcile(t *testing.T) {
	ctx := pkgCtx

	exec.NewExecService = exec.NullExecService
	manager, err := testingManager()
	if err != nil {
		t.Fatalf("TestingManager() error = %v, want nil", err)
	}

	key := types.NamespacedName{
		Name:      "test-cluster",
		Namespace: "test-namespace",
	}
	reconciler := NewWekaClusterController(manager)
	req := ctrl.Request{
		NamespacedName: key,
	}

	t.Run("FromBeginning", func(t *testing.T) {
		preConditions := []metav1.Condition{}
		_, err = initWekaCluster(ctx, manager, key, preConditions)
		if err != nil {
			t.Fatalf("failed to init weka cluster: %v", err)
		}

		containerList := wekav1alpha1.WekaContainerList{}
		if err := initWekaContainers(ctx, manager, key, containerList.Items); err != nil {
			t.Fatalf("failed to init weka containers: %v", err)
		}

		result, err := reconciler.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("failed to reconcile: %v", err)
		}

		expectedResult := ctrl.Result{Requeue: false, RequeueAfter: 3 * time.Second}
		if result != expectedResult {
			t.Errorf("unexpected result - got: %+v, want: %+v", result, expectedResult)
		}

		after := &wekav1alpha1.WekaCluster{}
		if err := manager.GetClient().Get(ctx, req.NamespacedName, after); err != nil {
			t.Fatalf("failed to get cluster: %v", err)
		}

		completedConditions := []string{
			condition.CondClusterSecretsCreated,
			condition.CondPodsCreated,
			condition.CondContainerResourcesAllocated,
		}
		validateConditions(t, after, completedConditions)
	})

	t.Run("FromPodsReady", func(t *testing.T) {

		containers := wekav1alpha1.WekaContainerList{}
		if err := manager.GetClient().List(ctx, &containers); err != nil {
			t.Fatalf("failed to list containers: %v", err)
		}

		for _, container := range containers.Items {
			container.Status.Status = "Running"

			if err := manager.GetClient().Status().Update(ctx, &container); err != nil {
				t.Fatalf("failed to update container status: %v", err)
			}
		}

		expectedResult := ctrl.Result{Requeue: false, RequeueAfter: 3 * time.Second}
		result, err := reconciler.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("failed to reconcile: %v", err)
		}

		if result != expectedResult {
			t.Errorf("unexpected result - got: %+v, want: %+v", result, expectedResult)
		}

		after := &wekav1alpha1.WekaCluster{}
		if err := manager.GetClient().Get(ctx, req.NamespacedName, after); err != nil {
			t.Fatalf("failed to get cluster: %v", err)
		}

		completedConditions := []string{
			condition.CondClusterSecretsCreated,
			condition.CondPodsCreated,
			condition.CondContainerResourcesAllocated,
			condition.CondPodsReady,
		}

		validateConditions(t, after, completedConditions)
		t.Run("FromClusterCreated", func(t *testing.T) {
			// The CondJoinedCluster condition is set in another controller.  Simulate this update here and re-reconcile.
			containers := wekav1alpha1.WekaContainerList{}
			if err := manager.GetClient().List(ctx, &containers); err != nil {
				t.Fatalf("failed to list containers: %v", err)
			}

			for _, container := range containers.Items {
				meta.SetStatusCondition(&container.Status.Conditions, metav1.Condition{
					Type:   condition.CondJoinedCluster,
					Status: metav1.ConditionTrue,
					Reason: "Init",
				})
				clusterContainerID := 12345
				container.Status.ClusterContainerID = &clusterContainerID

				if err := manager.GetClient().Status().Update(ctx, &container); err != nil {
					t.Fatalf("failed to update container status: %v", err)
				}
			}

			result, err := reconciler.Reconcile(ctx, req)
			if err != nil {
				t.Fatalf("failed to reconcile: %v", err)
			}

			expectedResult := ctrl.Result{Requeue: false, RequeueAfter: 3 * time.Second}
			if result != expectedResult {
				t.Errorf("unexpected result - got: %+v, want: %+v", result, expectedResult)
			}

			completedConditions := []string{
				condition.CondClusterSecretsCreated,
				condition.CondPodsCreated,
				condition.CondContainerResourcesAllocated,
				condition.CondPodsReady,
				condition.CondClusterCreated,
				condition.CondJoinedCluster,
			}

			validateConditions(t, after, completedConditions)
		})
	})

	t.Run("DrivesAdded", func(t *testing.T) {
		// Simulate CondDrivesAdded on each container
		containers := wekav1alpha1.WekaContainerList{}
		if err := manager.GetClient().List(ctx, &containers); err != nil {
			t.Fatalf("failed to list containers: %v", err)
		}

		for _, container := range containers.Items {
			meta.SetStatusCondition(&container.Status.Conditions, metav1.Condition{
				Type:   condition.CondDrivesAdded,
				Status: metav1.ConditionTrue,
				Reason: "DrivesAdded",
			})

			if err := manager.GetClient().Status().Update(ctx, &container); err != nil {
				t.Fatalf("failed to update container status: %v", err)
			}
		}

		// Reconcile again
		result, err := reconciler.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("failed to reconcile: %v", err)
		}

		expectedResult := ctrl.Result{Requeue: true, RequeueAfter: 30 * time.Second}
		if result != expectedResult {
			t.Errorf("unexpected result - got: %+v, want: %+v", result, expectedResult)
		}

		// Verify that the CondDrivesAdded condition is set on the cluster
		updatedCluster := &wekav1alpha1.WekaCluster{}
		if err := manager.GetClient().Get(ctx, req.NamespacedName, updatedCluster); err != nil {
			t.Fatalf("failed to get updated cluster: %v", err)
		}

		completedConditions := []string{
			condition.CondClusterSecretsCreated,
			condition.CondPodsCreated,
			condition.CondContainerResourcesAllocated,
			condition.CondPodsReady,
			condition.CondClusterCreated,
			condition.CondJoinedCluster,
			condition.CondDrivesAdded,
			condition.CondIoStarted,
			condition.CondClusterSecretsApplied,
			condition.CondDefaultFsCreated,
			condition.CondClusterClientSecretsCreated,
			condition.CondClusterClientSecretsApplied,
			condition.CondClusterCSISecretsCreated,
			condition.CondClusterCSISecretsApplied,
			condition.WekaHomeConfigured,
			condition.CondClusterReady,
		}

		validateConditions(t, updatedCluster, completedConditions)
	})

	t.Run("Startup Completed", func(t *testing.T) {
		// Get the latest cluster state
		updatedCluster := &wekav1alpha1.WekaCluster{}
		if err := manager.GetClient().Get(ctx, req.NamespacedName, updatedCluster); err != nil {
			t.Fatalf("failed to get updated cluster: %v", err)
		}

		// Validate all conditions are True
		for _, cond := range updatedCluster.Status.Conditions {
			if cond.Status != metav1.ConditionTrue {
				t.Errorf("condition %s not True, got: %s", cond.Type, cond.Status)
			}
		}

		// Ensure we have at least 6 conditions
		if len(updatedCluster.Status.Conditions) < 6 {
			t.Errorf("not enough conditions found on the cluster, found: %d", len(updatedCluster.Status.Conditions))
		}

		if updatedCluster.Status.Status != "Ready" {
			t.Errorf("cluster status not 'Ready', got: %s", updatedCluster.Status.Status)
		}
	})
}

func validateConditions(t *testing.T, cluster *wekav1alpha1.WekaCluster, completedConditions []string) {
	for _, conditionType := range completedConditions {
		actual := meta.FindStatusCondition(cluster.Status.Conditions, conditionType)
		if actual == nil {
			t.Errorf("missing condition: %s in %+v", conditionType, cluster.Status.Conditions)
		}
	}
}

func initWekaCluster(ctx context.Context, manager testutil.Manager, key types.NamespacedName, conditions []metav1.Condition) (*wekav1alpha1.WekaCluster, error) {
	before := manager.GetObject("*v1alpha1.WekaCluster", key).(*wekav1alpha1.WekaCluster)
	before.Status = wekav1alpha1.WekaClusterStatus{
		Conditions: conditions,
	}
	if err := manager.GetClient().Status().Update(ctx, before); err != nil {
		return nil, err
	}
	// Conditions get cleared out if finalizer is not present
	//controllerutil.AddFinalizer(before, WekaFinalizer)

	if err := manager.GetClient().Update(ctx, before); err != nil {
		return nil, err
	}
	return before, nil
}

func initWekaContainers(ctx context.Context, manager testutil.Manager, key types.NamespacedName, containers []wekav1alpha1.WekaContainer) error {
	for i, container := range containers {
		containerName := fmt.Sprintf("%s-container-%d", key.Name, i)
		container.ObjectMeta = metav1.ObjectMeta{
			Name:      containerName,
			Namespace: key.Namespace,
		}
		if err := manager.GetClient().Create(ctx, &container); err != nil {
			return err
		}
	}
	return nil
}
