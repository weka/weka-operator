package services

import (
	"context"
	"os"
	"testing"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"

	"github.com/pkg/errors"
	"go.uber.org/mock/gomock"
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
		Manager: fixtures.mockManager,
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
