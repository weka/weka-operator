package services

import (
	"context"
	"os"
	"testing"

	mocks "github.com/weka/weka-operator/internal/app/manager/services/mocks"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"go.uber.org/mock/gomock"
)

func TestEnsureDriversLoader(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	os.Setenv("OPERATOR_DEV_MODE", "true")

	crdManager := &mockCrdManager{}

	found := &wekav1alpha1.WekaContainer{}

	service := &wekaContainerService{
		Container:  &wekav1alpha1.WekaContainer{},
		CrdManager: crdManager,
	}

	t.Run("Pod Exists", func(t *testing.T) {
		mockClient := mocks.NewMockClient(ctrl)
		mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).SetArg(2, *found).Return(nil).AnyTimes()

		service.Client = mockClient

		ctx := context.Background()
		if err := service.EnsureDriversLoader(ctx); err != nil {
			t.Errorf("Expected nil, got %v", err)
		}
	})

	t.Run("Pod Not Found", func(t *testing.T) {
		notFoundError := apierrors.NewNotFound(schema.GroupResource{}, "test")
		mockClient := mocks.NewMockClient(ctrl)
		mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(notFoundError).Times(1)
		mockClient.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)

		service.Client = mockClient

		if err := service.EnsureDriversLoader(context.Background()); err != nil {
			t.Error("Expected error, got nil")
		}
	})
}

type mockCrdManager struct{}

func (m *mockCrdManager) RefreshPod(ctx context.Context, container *wekav1alpha1.WekaContainer) (*v1.Pod, error) {
	pod := &v1.Pod{
		Spec: v1.PodSpec{
			NodeName: "test",
		},
	}
	return pod, nil
}
