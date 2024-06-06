//go:generate go run go.uber.org/mock/mockgen@v0.4.0 -destination=mocks/mock_manager.go -package=mocks sigs.k8s.io/controller-runtime/pkg/manager Manager
//go:generate go run go.uber.org/mock/mockgen@v0.4.0 -destination=mocks/mock_services.go -package=mocks github.com/weka/weka-operator/internal/app/manager/services CrdManager,WekaClusterService,ExecService,KubeService
//go:generate go run go.uber.org/mock/mockgen@v0.4.0 -destination=mocks/mock_lifecycle.go -package=mocks github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle Lifecycle
package controllers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/controllers/mocks"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	werrors "github.com/weka/weka-operator/internal/pkg/errors"

	"go.uber.org/mock/gomock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconcileWekaCluster(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	manager := mocks.NewMockManager(ctrl)
	client := mocks.NewMockClient(ctrl)
	scheme := runtime.NewScheme()

	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test",
			Namespace: "test",
		},
	}

	mockCrdManager := mocks.NewMockCrdManager(ctrl)

	testingError := fmt.Errorf("testing error")
	tests := []struct {
		name   string
		result reconcile.Result
		err    error
		steps  []lifecycle.Step[*wekav1alpha1.WekaCluster]
	}{
		{
			name:   "no steps",
			result: reconcile.Result{},
			err:    nil,
			steps:  []lifecycle.Step[*wekav1alpha1.WekaCluster]{},
		},
		{
			name:   "retryable error",
			result: reconcile.Result{Requeue: true},
			err:    nil,
			steps: []lifecycle.Step[*wekav1alpha1.WekaCluster]{
				{
					Condition: "test",
					Reconcile: func(ctx context.Context) error { return &werrors.RetryableError{} },
				},
			},
		},
		{
			name:   "non-retryable error",
			result: reconcile.Result{},
			err:    testingError,
			steps: []lifecycle.Step[*wekav1alpha1.WekaCluster]{
				{
					Condition: "test",
					Reconcile: func(ctx context.Context) error { return testingError },
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := &lifecycle.ReconciliationSteps[*wekav1alpha1.WekaCluster]{
				Reconciler: client,
				State: &lifecycle.ReconciliationState[*wekav1alpha1.WekaCluster]{
					Subject:    &wekav1alpha1.WekaCluster{},
					Conditions: &[]metav1.Condition{},
				},
				Steps: tt.steps,
			}
			controller := &WekaClusterReconciler{
				Client:  client,
				Scheme:  scheme,
				Manager: manager,

				CrdManager: mockCrdManager,

				Steps: steps,
			}

			result, err := controller.Reconcile(ctx, req)
			if !errors.Is(err, tt.err) {
				t.Errorf("Reconcile() error = %v, wantErr %v", err, tt.err)
			}
			if result != tt.result {
				t.Errorf("Reconcile() result = %v, want %v", result, tt.result)
			}
		})
	}
}
