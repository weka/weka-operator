package container

import (
	"testing"

	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"go.uber.org/mock/gomock"
)

func TestGetWekaContainerService(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	state := &ContainerState{}
	subject1 := &wekav1alpha1.WekaContainer{}
	subject2 := &wekav1alpha1.WekaContainer{}

	cachedContainerService := services.NewWekaContainerService(nil, nil, nil, subject1)
	state.ContainerServices = make(map[*wekav1alpha1.WekaContainer]services.WekaContainerService)
	state.ContainerServices[subject1] = cachedContainerService

	t.Run("NewWekaContainerService", func(t *testing.T) {
		state.Subject = subject1
		containerService := state.GetWekaContainerService()
		if containerService == nil {
			t.Fatal("container service should not be nil")
		}
		if containerService != cachedContainerService {
			t.Fatal("container service should be cached")
		}
	})
	t.Run("CachedWekaContainerService", func(t *testing.T) {
		state.Subject = subject2
		containerService := state.GetWekaContainerService()
		if containerService == nil {
			t.Fatal("container service should not be nil")
		}
		if containerService == cachedContainerService {
			t.Fatal("container service should not be cached")
		}
	})
}
