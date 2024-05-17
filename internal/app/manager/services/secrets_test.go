package services

import (
	"context"
	"testing"

	"github.com/weka/weka-operator/internal/app/manager/services/mocks"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"go.uber.org/mock/gomock"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestEnsureLoginCredentials(t *testing.T) {
	fixtures := setup(t)
	defer fixtures.teardown()

	ctx := context.Background()
	cluster := &wekav1alpha1.WekaCluster{}
	subject := &secretsService{
		Client: fixtures.mockClient,
		Scheme: fixtures.scheme,
	}

	tests := []struct {
		name       string
		setupMocks func(mockClient *mocks.MockClient)
	}{
		{
			name: "secret exists",
			setupMocks: func(mockClient *mocks.MockClient) {
				mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(2)
			},
		},
		{
			name: "secret does not exist",
			setupMocks: func(mockClient *mocks.MockClient) {
				mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(
					apierrors.NewNotFound(schema.GroupResource{}, "test"),
				).Times(2)
				mockClient.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(2)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupMocks(fixtures.mockClient)
			if err := subject.EnsureLoginCredentials(ctx, cluster); err != nil {
				t.Errorf("Expected nil, got %v", err)
			}
		})
	}
}
