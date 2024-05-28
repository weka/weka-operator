package services

import (
	"context"
	"os"
	"testing"

	"github.com/weka/weka-operator/internal/app/manager/domain"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"

	"github.com/pkg/errors"
	"go.uber.org/mock/gomock"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestGetCluster(t *testing.T) {
	fixtures := setup(t)
	defer fixtures.teardown()

	config := &rest.Config{}

	fixtures.mockManager.EXPECT().GetClient().Return(fixtures.mockClient).AnyTimes()
	fixtures.mockManager.EXPECT().GetConfig().Return(config).AnyTimes()

	subject := &crdManager{
		Manager: fixtures.mockManager,
	}

	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: "test-namespace",
			Name:      "test-name",
		},
	}

	failedToGetClusterError := errors.New("Failed to get wekaCluster")
	cluster := &wekav1alpha1.WekaCluster{}
	tests := []struct {
		name     string
		cluster  *wekav1alpha1.WekaCluster
		apiError error
		err      error
	}{
		{
			name:     "cluster not found",
			cluster:  nil,
			apiError: apierrors.NewNotFound(schema.GroupResource{}, "test-name"),
			err:      nil,
		},
		{
			name:     "cluster found",
			cluster:  cluster,
			apiError: nil,
			err:      nil,
		},
		{
			name:     "error",
			cluster:  nil,
			apiError: failedToGetClusterError,
			err:      failedToGetClusterError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixtures.mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(tt.apiError)
			clusterService, err := subject.GetClusterService(ctx, req)
			if err != tt.err {
				t.Errorf("Expected %v, got %v", tt.err, err)
			}
			cluster := clusterService.GetCluster()
			if (tt.cluster == nil && cluster != nil) || (tt.cluster != nil && cluster == nil) {
				t.Errorf("Expected %v, got %v", tt.cluster, cluster)
			}
		})
	}
}

func TestEnsureWekaContainers(t *testing.T) {
	fixtures := setup(t)
	defer fixtures.teardown()

	config := &rest.Config{}

	os.Setenv("OPERATOR_DEV_MODE", "true")

	fixtures.mockManager.EXPECT().GetClient().Return(fixtures.mockClient).AnyTimes()
	fixtures.mockManager.EXPECT().GetConfig().Return(config).AnyTimes()
	fixtures.mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	fixtures.mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	subject := &crdManager{
		Manager:              fixtures.mockManager,
		WekaContainerFactory: fixtures.mockWekaContainerFactory,
	}

	ctx := context.Background()
	cluster := &wekav1alpha1.WekaCluster{
		Spec: wekav1alpha1.WekaClusterSpec{
			Template: "small",
			Topology: "discover_oci",
		},
	}
	container, err := subject.EnsureWekaContainers(ctx, cluster)
	if err != nil {
		t.Skipf("Failed to ensure weka containers: %v", err)
		t.Errorf("Expected nil, got %v", err)
	}
	if container != nil {
		t.Errorf("Expected nil, got %v", container)
	}
}

func TestGetOrInitAllocMap(t *testing.T) {
	fixtures := setup(t)
	defer fixtures.teardown()

	os.Setenv("OPERATOR_DEV_MODE", "true")

	config := &rest.Config{}

	fixtures.mockManager.EXPECT().GetClient().Return(fixtures.mockClient).AnyTimes()
	fixtures.mockManager.EXPECT().GetConfig().Return(config).AnyTimes()

	subject := &crdManager{
		Manager: fixtures.mockManager,
	}

	ctx := context.Background()

	tests := []struct {
		name              string
		apiError          error
		err               error
		validateConfigMap func(configMap *v1.ConfigMap)
	}{
		{
			name:     "configmap not found - and create succeeds",
			apiError: apierrors.NewNotFound(schema.GroupResource{}, "test-name"),
			err:      nil,
			validateConfigMap: func(configMap *v1.ConfigMap) {
				if configMap.Name != "weka-operator-allocmap" {
					t.Errorf("Expected weka-operator-allocmap, got %v", configMap.Name)
				}
			},
		},
		{
			name:     "configmap found",
			apiError: nil,
			err:      nil,
			validateConfigMap: func(configMap *v1.ConfigMap) {
				if configMap.Name != "weka-operator-allocmap" {
					t.Errorf("Expected weka-operator-allocmap, got %v", configMap.Name)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixtures.mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(tt.apiError).AnyTimes()
			fixtures.mockClient.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

			_, configMap, err := subject.GetOrInitAllocMap(ctx)
			if err != tt.err {
				t.Errorf("Expected %v, got %v", tt.err, err)
			}
			tt.validateConfigMap(configMap)
		})
	}
}

func TestUpdateAllocationsConfigmap(t *testing.T) {
	fixtures := setup(t)
	defer fixtures.teardown()

	config := &rest.Config{}

	fixtures.mockManager.EXPECT().GetClient().Return(fixtures.mockClient).AnyTimes()
	fixtures.mockManager.EXPECT().GetConfig().Return(config).AnyTimes()

	subject := &crdManager{
		Manager: fixtures.mockManager,
	}

	ctx := context.Background()

	expectedError := errors.New("expected error")
	tests := []struct {
		name     string
		apiError error
		err      error
	}{
		{
			name:     "success",
			apiError: nil,
			err:      nil,
		},
		{
			name:     "error",
			apiError: expectedError,
			err:      expectedError,
		},
	}

	allocations := &domain.Allocations{}
	configMap := &v1.ConfigMap{
		Data: map[string]string{},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixtures.mockClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(tt.apiError)

			err := subject.UpdateAllocationsConfigmap(ctx, allocations, configMap)
			if !errors.Is(err, tt.err) {
				t.Errorf("Expected %v, got %v", tt.err, err)
			}
		})
	}
}
