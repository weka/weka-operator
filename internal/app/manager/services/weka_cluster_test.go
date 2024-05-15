//go:generate go run go.uber.org/mock/mockgen@latest -destination=mocks/mock_client.go -package=mocks sigs.k8s.io/controller-runtime/pkg/client Client,StatusWriter
package services

import (
	"context"
	"testing"

	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/services/mocks"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"go.uber.org/mock/gomock"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestGetCluster(t *testing.T) {
	fixtures := setup(t)
	defer fixtures.teardown()

	subject := &wekaClusterService{
		Client: fixtures.mockClient,
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
			cluster, err := subject.GetCluster(ctx, req)
			if err != tt.err {
				t.Errorf("Expected %v, got %v", tt.err, err)
			}
			if (tt.cluster == nil && cluster != nil) || (tt.cluster != nil && cluster == nil) {
				t.Errorf("Expected %v, got %v", tt.cluster, cluster)
			}
		})
	}
}

type fixtures struct {
	ctrl       *gomock.Controller
	mockClient *mocks.MockClient
}

func setup(t *testing.T) *fixtures {
	ctrl := gomock.NewController(t)
	mockClient := mocks.NewMockClient(ctrl)
	return &fixtures{
		ctrl:       ctrl,
		mockClient: mockClient,
	}
}

func (f *fixtures) teardown() {
	f.ctrl.Finish()
}
