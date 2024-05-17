//go:generate go run go.uber.org/mock/mockgen@latest -destination=mocks/mock_factories.go -package=mocks github.com/weka/weka-operator/internal/app/manager/factory WekaContainerFactory
package services

import (
	"testing"

	mocks "github.com/weka/weka-operator/internal/app/manager/services/mocks"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"

	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/runtime"
)

type fixtures struct {
	ctrl *gomock.Controller

	mockClient               *mocks.MockClient
	mockExec                 *mocks.MockExec
	mockExecService          *mocks.MockExecService
	mockManager              *mocks.MockManager
	mockStatus               *mocks.MockStatusWriter
	mockWekaContainerFactory *mocks.MockWekaContainerFactory

	scheme *runtime.Scheme
}

func setup(t *testing.T) *fixtures {
	ctrl := gomock.NewController(t)

	mockClient := mocks.NewMockClient(ctrl)
	mockExec := mocks.NewMockExec(ctrl)
	mockExecService := mocks.NewMockExecService(ctrl)
	mockManager := mocks.NewMockManager(ctrl)
	mockStatus := mocks.NewMockStatusWriter(ctrl)
	mockWekaContainerFactory := mocks.NewMockWekaContainerFactory(ctrl)

	scheme := runtime.NewScheme()
	if err := wekav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add scheme: %v", err)
	}

	return &fixtures{
		ctrl: ctrl,

		mockClient:               mockClient,
		mockExec:                 mockExec,
		mockExecService:          mockExecService,
		mockManager:              mockManager,
		mockStatus:               mockStatus,
		mockWekaContainerFactory: mockWekaContainerFactory,

		scheme: scheme,
	}
}

func (f *fixtures) teardown() {
	f.ctrl.Finish()
}
