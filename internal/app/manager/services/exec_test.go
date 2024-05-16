//go:generate go run go.uber.org/mock/mockgen@latest -destination=mocks/mock_manager.go -package=mocks sigs.k8s.io/controller-runtime/pkg/manager Manager
package services

import (
	"testing"

	mocks "github.com/weka/weka-operator/internal/app/manager/services/mocks"
	"go.uber.org/mock/gomock"
	"k8s.io/client-go/rest"
)

func TestNewExecServiceFromManager(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	config := &rest.Config{}
	manager := mocks.NewMockManager(ctrl)
	manager.EXPECT().GetConfig().Return(config)

	subject := NewExecServiceFromManager(manager)
	if subject == nil {
		t.Fatal("Expected non-nil return value")
	}
	if subject.(*PodExecService).config == nil {
		t.Fatal("Expected config to be set")
	}
}

func TestNewExecService(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	config := &rest.Config{}

	subject := NewExecService(config)
	if subject == nil {
		t.Fatal("Expected non-nil return value")
	}
	if subject.(*PodExecService).config != config {
		t.Fatal("Expected config to be set")
	}
}
