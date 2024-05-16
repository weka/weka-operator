//go:generate go run go.uber.org/mock/mockgen@latest -destination=mocks/mock_client.go -package=mocks sigs.k8s.io/controller-runtime/pkg/client Client,StatusWriter
//go:generate go run go.uber.org/mock/mockgen@latest -destination=mocks/mock_exec.go -package=mocks github.com/weka/weka-operator/util Exec
//go:generate go run go.uber.org/mock/mockgen@latest -destination=mocks/mock_exec_service.go -package=mocks github.com/weka/weka-operator/internal/app/manager/services ExecService
//go:generate go run go.uber.org/mock/mockgen@latest -destination=mocks/mock_manager.go -package=mocks sigs.k8s.io/controller-runtime/pkg/manager Manager
package services

import (
	"bytes"
	"context"
	"testing"

	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/services/mocks"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"go.uber.org/mock/gomock"
)

func TestCreate(t *testing.T) {
	fixtures := setup(t)
	defer fixtures.teardown()

	cluster := &wekav1alpha1.WekaCluster{}
	subject := &wekaClusterService{
		Client:      fixtures.mockClient,
		ExecService: fixtures.mockExecService,
		Cluster:     cluster,
	}

	ctx := context.Background()
	containers := []*wekav1alpha1.WekaContainer{
		{
			Spec:   wekav1alpha1.WekaContainerSpec{},
			Status: wekav1alpha1.WekaContainerStatus{},
		},
	}

	tests := []struct {
		name       string
		err        error
		containers []*wekav1alpha1.WekaContainer
	}{
		{
			name:       "empty containers",
			err:        errors.New("containers list is empty"),
			containers: []*wekav1alpha1.WekaContainer{},
		},
		{
			name:       "with containers",
			err:        nil,
			containers: containers,
		},
		//{
		//name:     "success",
		//apiError: nil,
		//err:      nil,
		//},
		//{
		//name:     "error",
		//apiError: failure,
		//err:      failure,
		//},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixtures.mockExecService.EXPECT().GetExecutor(gomock.Any(), gomock.Any()).Return(fixtures.mockExec, nil).AnyTimes()
			stdout := bytes.Buffer{}
			stderr := bytes.Buffer{}
			fixtures.mockExec.EXPECT().ExecNamed(gomock.Any(), gomock.Any(), gomock.Any()).Return(stdout, stderr, nil).AnyTimes()

			fixtures.mockClient.EXPECT().Status().Return(fixtures.mockStatus).AnyTimes()
			fixtures.mockStatus.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

			err := subject.Create(ctx, tt.containers)
			if tt.err != nil || err != nil {
				if err.Error() != tt.err.Error() {
					t.Errorf("Expected %v, got %v for %d contaienrs", tt.err, err, len(tt.containers))
				}
			}
		})
	}
}

type fixtures struct {
	ctrl *gomock.Controller

	mockClient      *mocks.MockClient
	mockExec        *mocks.MockExec
	mockExecService *mocks.MockExecService
	mockManager     *mocks.MockManager
	mockStatus      *mocks.MockStatusWriter
}

func setup(t *testing.T) *fixtures {
	ctrl := gomock.NewController(t)

	mockClient := mocks.NewMockClient(ctrl)
	mockExec := mocks.NewMockExec(ctrl)
	mockExecService := mocks.NewMockExecService(ctrl)
	mockManager := mocks.NewMockManager(ctrl)
	mockStatus := mocks.NewMockStatusWriter(ctrl)

	return &fixtures{
		ctrl: ctrl,

		mockClient:      mockClient,
		mockExec:        mockExec,
		mockExecService: mockExecService,
		mockManager:     mockManager,
		mockStatus:      mockStatus,
	}
}

func (f *fixtures) teardown() {
	f.ctrl.Finish()
}
