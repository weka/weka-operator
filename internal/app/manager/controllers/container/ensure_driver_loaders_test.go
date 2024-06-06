//go:generate go run go.uber.org/mock/mockgen@v0.4.0 -destination=mocks/mock_exec_service.go -package=mocks github.com/weka/weka-operator/internal/app/manager/services WekaContainerService
package container

import (
	"context"
	"errors"
	"testing"

	"github.com/weka/weka-operator/internal/app/manager/controllers/container/mocks"
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	werrors "github.com/weka/weka-operator/internal/pkg/errors"

	"go.uber.org/mock/gomock"
)

func TestEnsureDriversLoader(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tests := []struct {
		name          string
		state         *ContainerState
		expectedError error
		setupMocks    func(state *ContainerState) error
	}{
		{
			name:          "no container",
			state:         &ContainerState{},
			expectedError: &werrors.ArgumentError{ArgName: "container", Message: "container is nil"},
			setupMocks:    func(state *ContainerState) error { return nil },
		},
		{
			name: "no drivers dist service",
			state: &ContainerState{
				ReconciliationState: lifecycle.ReconciliationState[*wekav1alpha1.WekaContainer]{
					Subject: &wekav1alpha1.WekaContainer{
						Spec: wekav1alpha1.WekaContainerSpec{
							DriversDistService: "",
						},
					},
				},
			},
			expectedError: nil,
			setupMocks:    func(state *ContainerState) error { return nil },
		},
		{
			name: "ensure loader success",
			state: &ContainerState{
				ReconciliationState: lifecycle.ReconciliationState[*wekav1alpha1.WekaContainer]{
					Subject: &wekav1alpha1.WekaContainer{
						Spec: wekav1alpha1.WekaContainerSpec{
							DriversDistService: "test",
						},
					},
				},
			},
			expectedError: nil,
			setupMocks: func(state *ContainerState) error {
				subject := state.Subject
				if subject == nil {
					return errors.New("subject not set")
				}

				state.ContainerServices = make(map[*wekav1alpha1.WekaContainer]services.WekaContainerService)
				containerService := mocks.NewMockWekaContainerService(ctrl)
				state.ContainerServices[subject] = containerService
				if state.GetWekaContainerService() != containerService {
					return errors.New("container service not set")
				}

				containerService.EXPECT().EnsureDriversLoader(gomock.Any()).Return(nil)
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := tt.state
			if err := tt.setupMocks(state); err != nil {
				t.Fatalf("Error setting up mocks: %v", err)
			}
			err := state.EnsureDriversLoader()(context.Background())

			if !errors.Is(err, tt.expectedError) {
				t.Errorf("Expected error '%v', got '%v'", tt.expectedError, err)
			}
		})
	}
}
