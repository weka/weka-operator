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

	"github.com/weka/weka-operator/internal/controllers/condition"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

func TestReconcile(t *testing.T) {
	os.Setenv("OPERATOR_DEV_MODE", "true")
	ctx := context.Background()

	internalLogger := zapr.NewLogger(prettyconsole.NewLogger(uzap.DebugLevel))
	ctx, logger := instrumentation.GetLoggerForContext(ctx, &internalLogger, "TestReconcile")
	logger.Info("TestReconcile")

	tests := []struct {
		name           string
		preConditions  []metav1.Condition
		postConditions []metav1.Condition
		result         ctrl.Result
	}{
		// Each test represents a lifecycle stage of the WekaCluster

		// Stage: Container start up until pods created, but not yet ready
		{
			name:          "new cluster",
			preConditions: []metav1.Condition{},
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
		/*
			{
				name: "pods ready",
				preConditions: []metav1.Condition{
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
		*/
	}

	key := types.NamespacedName{
		Name:      "test-cluster",
		Namespace: "test-namespace",
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, err := testingManager()
			if err != nil {
				t.Fatalf("TestingManager() error = %v, want nil", err)
			}
			before := manager.GetObject("*v1alpha1.WekaCluster", key).(*wekav1alpha1.WekaCluster)
			before.Status = wekav1alpha1.WekaClusterStatus{
				Conditions: test.preConditions,
			}
			if err := manager.GetClient().Status().Update(ctx, before); err != nil {
				t.Fatalf("failed to update cluster: %v", err)
			}
			// Conditions get cleared out if finalizer is not present
			//controllerutil.AddFinalizer(before, WekaFinalizer)

			if err := manager.GetClient().Update(ctx, before); err != nil {
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
					t.Errorf("unexpected status: %s", actual.Status)
				}
				if actual.Reason != postCondition.Reason {
					t.Errorf("unexpected reason - want: %s, got: %s", postCondition.Reason, actual.Reason)
				}
			}
		})
	}
}
