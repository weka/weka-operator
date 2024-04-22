//go:generate mockgen -destination=mocks/mock_client.go -package=mocks sigs.k8s.io/controller-runtime/pkg/client Client,StatusWriter
package services

import (
	"context"
	"os"
	"testing"

	"github.com/weka/weka-operator/internal/app/manager/domain"
	"github.com/weka/weka-operator/internal/app/manager/services/mocks"
	"github.com/weka/weka-operator/util"
	"go.uber.org/mock/gomock"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestGetOrInitAllocMap(t *testing.T) {
	os.Setenv("OPERATOR_DEV_MODE", "true")

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tests := []struct {
		name      string
		initMocks func(client *mocks.MockClient)
	}{
		{
			name: "alloc map not found",
			initMocks: func(client *mocks.MockClient) {
				err := apierrors.NewNotFound(schema.GroupResource{}, "weka-operator-allocmap")
				client.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(err)
				client.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
			},
		},
	}
	for _, tt := range tests {
		client := mocks.NewMockClient(ctrl)
		tt.initMocks(client)

		subject := &allocationService{
			Client: client,
		}
		ctx := context.Background()
		allocations, configmap, error := subject.GetOrInitAllocMap(ctx)
		if error != nil {
			t.Errorf("Error: %v", error)
		}
		if allocations == nil {
			t.Errorf("Allocations is nil")
		}
		if configmap == nil {
			t.Errorf("Configmap is nil")
		}

		t.Run("Validate Allocations", validateAllocations(allocations))
		t.Run("Validate ConfigMap", validateConfigMap(configmap))
	}
}

func validateAllocations(allocations *domain.Allocations) func(t *testing.T) {
	return func(t *testing.T) {
		if allocations.NodeMap == nil {
			t.Error("Allocations.NodeMap is nil")
		}
		nodeMap := allocations.NodeMap
		if len(nodeMap) != 0 {
			t.Errorf("Allocations.NodeMap length expected: %d, got: %d", 0, len(nodeMap))
		}

		agentPorts := allocations.Global.AgentPorts
		if agentPorts != nil {
			t.Error("Allocations.Global.AgentPorts is not nil")
		}

		wekaContainerPorts := allocations.Global.WekaContainerPorts
		if wekaContainerPorts != nil {
			t.Error("Allocations.Global.WekaContainerPorts is not nil")
		}
	}
}

func validateConfigMap(configmap *v1.ConfigMap) func(t *testing.T) {
	return func(t *testing.T) {
		if configmap.Name != "weka-operator-allocmap" {
			t.Errorf("ConfigMap name expected: %s, got: %s", "weka-operator-allocmap", configmap.Name)
		}
		if configmap.Namespace != util.DevModeNamespace {
			t.Errorf("ConfigMap namespace: %s, got: %s", util.DevModeNamespace, configmap.Namespace)
		}
		if configmap.Data == nil {
			t.Error("ConfigMap data is nil")
		}
		if configmap.Data["allocations.yaml"] != "" {
			t.Error("ConfigMap allocations.yaml should be empty")
		}
	}
}

func TestUpdateAllocationsConfigmap(t *testing.T) {
	os.Setenv("OPERATOR_DEV_MODE", "true")

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	client := mocks.NewMockClient(ctrl)
	client.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)

	subject := &allocationService{
		Client: client,
	}
	ctx := context.Background()
	allocations := &domain.Allocations{
		NodeMap: domain.AllocationsMap{},
	}
	configmap := &v1.ConfigMap{
		Data: map[string]string{},
	}
	error := subject.UpdateAllocationsConfigmap(ctx, allocations, configmap)
	if error != nil {
		t.Errorf("Error: %v", error)
	}
}
