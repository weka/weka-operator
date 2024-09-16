package controllers

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-logr/zapr"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	uzap "go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/controllers/condition"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/internal/testutil"
)

func TestReconcile(t *testing.T) {
	os.Setenv("OPERATOR_DEV_MODE", "true")
	ctx := context.Background()

	internalLogger := zapr.NewLogger(prettyconsole.NewLogger(uzap.DebugLevel))
	ctx, logger := instrumentation.GetLoggerForContext(ctx, &internalLogger, "TestReconcile")
	logger.Info("TestReconcile")

	key := types.NamespacedName{
		Name:      "test-cluster",
		Namespace: "test-namespace",
	}
	tests := []struct {
		name           string
		setupCluster   func(manager testutil.Manager)
		postConditions []metav1.Condition
		result         ctrl.Result
	}{
		// Each test represents a lifecycle stage of the WekaCluster

		// Stage: Container start up until pods created, but not yet ready
		{
			name:         "new cluster",
			setupCluster: func(testutil.Manager) {},
			postConditions: []metav1.Condition{
				{
					Type:   condition.CondClusterSecretsCreated,
					Status: metav1.ConditionTrue,
					Reason: "Init",
				},
				{
					Type:   condition.CondPodsCreated,
					Status: metav1.ConditionTrue,
					Reason: "Init",
				},
				{
					Type:   condition.CondContainerResourcesAllocated,
					Status: metav1.ConditionTrue,
					Reason: "Init",
				},
				{
					Type:   condition.CondPodsReady,
					Status: metav1.ConditionFalse,
					Reason: "Error",
				},
			},
			result: ctrl.Result{Requeue: false, RequeueAfter: 3 * time.Second},
		},

		// Stage: Pods Ready until ...

		{
			name: "pods ready",
			setupCluster: func(manager testutil.Manager) {
				cluster := &wekav1alpha1.WekaCluster{}
				if err := manager.GetClient().Get(ctx, key, cluster); err != nil {
					t.Fatalf("failed to get cluster: %v", err)
				}

				cluster.Status.Conditions = []metav1.Condition{
					{
						Type:   condition.CondClusterSecretsCreated,
						Status: metav1.ConditionTrue,
					},
					{
						Type:   condition.CondPodsCreated,
						Status: metav1.ConditionTrue,
					},
					{
						Type:   condition.CondContainerResourcesAllocated,
						Status: metav1.ConditionTrue,
					},
					{
						Type:   condition.CondPodsReady,
						Status: metav1.ConditionTrue,
					},
				}
				if err := manager.GetClient().Status().Update(ctx, cluster); err != nil {
					t.Fatalf("failed to update cluster status: %v", err)
				}
				controllerutil.AddFinalizer(cluster, WekaFinalizer)
				if err := manager.GetClient().Update(ctx, cluster); err != nil {
					t.Fatalf("failed to add finalizer: %v", err)
				}
			},
			postConditions: []metav1.Condition{
				{
					Type:   condition.CondClusterSecretsCreated,
					Status: metav1.ConditionTrue,
					Reason: "Init",
				},
				{
					Type:   condition.CondPodsCreated,
					Status: metav1.ConditionTrue,
					Reason: "Init",
				},
				{
					Type:   condition.CondContainerResourcesAllocated,
					Status: metav1.ConditionTrue,
					Reason: "Init",
				},
				{
					Type:   condition.CondPodsReady,
					Status: metav1.ConditionTrue,
					Reason: "Init",
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, err := testingManager()
			if err != nil {
				t.Fatalf("TestingManager() error = %v, want nil", err)
			}
			test.setupCluster(manager)
			// Conditions get cleared out if finalizer is not present
			// controllerutil.AddFinalizer(before, WekaFinalizer)

			before := &wekav1alpha1.WekaCluster{}
			if err := manager.GetClient().Get(ctx, key, before); err != nil {
				t.Fatalf("failed to update cluster: %v", err)
			}

			reconciler := NewWekaClusterController(manager)
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-cluster",
					Namespace: "test-namespace",
				},
			}
			result, err := reconciler.Reconcile(ctx, req)
			if err != nil {
				t.Fatalf("failed to reconcile: %v", err)
			}

			if test.result != result {
				t.Errorf("unexpected result - got: %+v, want: %+v", result, test.result)
			}

			after := &wekav1alpha1.WekaCluster{}
			if err := manager.GetClient().Get(ctx, req.NamespacedName, after); err != nil {
				t.Fatalf("failed to get cluster: %v", err)
			}

			for _, postCondition := range test.postConditions {
				actual := meta.FindStatusCondition(after.Status.Conditions, postCondition.Type)
				if actual == nil {
					t.Fatalf("missing condition: %s in %+v", postCondition.Type, after.Status.Conditions)
				}
				if actual.Status != postCondition.Status {
					t.Errorf("unexpected status: %s in %+v", actual.Status, actual)
				}
				if actual.Reason != postCondition.Reason {
					t.Errorf("unexpected reason - want: %s, got: %s", postCondition.Reason, actual.Reason)
				}
			}
		})
	}
}
