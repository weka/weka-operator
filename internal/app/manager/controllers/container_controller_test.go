//go:generate go run go.uber.org/mock/mockgen@v0.4.0 -destination=mocks/mock_manager.go -package=mocks sigs.k8s.io/controller-runtime/pkg/manager Manager
//go:generate go run go.uber.org/mock/mockgen@v0.4.0 -destination=mocks/mock_client.go -package=mocks sigs.k8s.io/controller-runtime/pkg/client Client,StatusWriter
//go:generate go run go.uber.org/mock/mockgen@v0.4.0 -destination=mocks/mock_services.go -package=mocks github.com/weka/weka-operator/internal/app/manager/services CrdManager,ExecService,KubeService
package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/controllers/mocks"
	wekav1alpha "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestWekaContainerController(t *testing.T) {
	t.Skip("TestWekaContainerController is not implemented")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCrdManager := mocks.NewMockCrdManager(ctrl)
	mockKubeService := mocks.NewMockKubeService(ctrl)
	mockExecService := mocks.NewMockExecService(ctrl)

	fixtures := mockFixtures(ctrl)
	if err := wekav1alpha.AddToScheme(fixtures.scheme); err != nil {
		t.Errorf("Failed to add Weka scheme: %v", err)
	}

	subject := &ContainerController{
		Client: fixtures.client,
		Scheme: fixtures.scheme,
		Logger: fixtures.logger,

		CrdManager:  mockCrdManager,
		KubeService: mockKubeService,
		ExecService: mockExecService,
	}

	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test",
			Namespace: "test",
		},
	}

	container := &wekav1alpha.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test",
		},
		Spec: wekav1alpha.WekaContainerSpec{
			CpuPolicy: "dedicated",
		},
	}

	tests := []struct {
		name   string
		steps  []lifecycle.Step[*wekav1alpha.WekaContainer]
		result reconcile.Result
		err    error
	}{
		{
			name:   "Reconcile",
			result: reconcile.Result{},
			err:    nil,
			steps: []lifecycle.Step[*wekav1alpha.WekaContainer]{
				{
					Condition: "Reconcile",
					Reconcile: func(ctx context.Context) error {
						return nil
					},
				},
			},
		},
		{
			name:   "Container Not Found",
			result: reconcile.Result{},
			err:    nil,
			steps: []lifecycle.Step[*wekav1alpha.WekaContainer]{
				{
					Condition: "ContainerNotFound",
					Reconcile: func(ctx context.Context) error {
						return &errors.NotFoundError{}
					},
				},
			},
		},
		{
			name:   "Requeue",
			result: reconcile.Result{Requeue: true, RequeueAfter: 3 * time.Second},
			err:    nil,
			steps: []lifecycle.Step[*wekav1alpha.WekaContainer]{
				{
					Condition: "Requeue",
					Reconcile: func(ctx context.Context) error {
						return &errors.RetryableError{}
					},
				},
			},
		},
	}

	status := mocks.NewMockStatusWriter(ctrl)
	fixtures.client.EXPECT().Status().Return(status).AnyTimes()
	status.EXPECT().Update(gomock.Any(), container).Return(nil).AnyTimes()

	for _, tt := range tests {
		name := tt.name
		t.Run(name, func(t *testing.T) {
			subject.Steps = &lifecycle.ReconciliationSteps[*wekav1alpha.WekaContainer]{
				Reconciler: fixtures.client,
				Steps:      tt.steps,
				State: &lifecycle.ReconciliationState[*wekav1alpha.WekaContainer]{
					Conditions: &[]metav1.Condition{},
					Subject:    container,
				},
			}
			result, err := subject.Reconcile(ctx, req)
			if err != tt.err {
				t.Errorf("Reconcile() error = %v, wantErr %v", err, tt.err)
			}
			if result != tt.result {
				t.Errorf("Reconcile() result = %v, want %v", result, tt.result)
			}
			if (container.Status.Conditions == nil) || (len(container.Status.Conditions) == 0) {
				t.Errorf("Reconcile() did not update container status")
			}
		})
	}
}

func TestNewContainerController(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	fixtures := mockFixtures(ctrl)
	subject := NewContainerController(fixtures.manager)
	if subject == nil {
		t.Errorf("NewContainerController() returned nil")
		return
	}

	if subject.Client == nil {
		t.Errorf("NewContainerController() returned controller with nil client")
	}

	if subject.Scheme == nil {
		t.Errorf("NewContainerController() returned controller with nil scheme")
	}
}

type fixtures struct {
	ctrl    *gomock.Controller
	manager *mocks.MockManager
	client  *mocks.MockClient
	scheme  *runtime.Scheme
	logger  logr.Logger
}

func mockFixtures(ctrl *gomock.Controller) *fixtures {
	manager := mocks.NewMockManager(ctrl)
	config := &rest.Config{}
	manager.EXPECT().GetConfig().Return(config).AnyTimes()

	client := mocks.NewMockClient(ctrl)
	manager.EXPECT().GetClient().Return(client).AnyTimes()

	scheme := runtime.NewScheme()
	manager.EXPECT().GetScheme().Return(scheme).AnyTimes()

	logger := zapr.NewLogger(zap.NewNop())
	manager.EXPECT().GetLogger().Return(logger).AnyTimes()

	return &fixtures{
		ctrl:    ctrl,
		manager: manager,
		client:  client,
		scheme:  scheme,
		logger:  logger,
	}
}
