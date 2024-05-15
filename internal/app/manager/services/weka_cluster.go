package services

import (
	"context"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type WekaClusterService interface {
	GetCluster(ctx context.Context, req ctrl.Request) (*wekav1alpha1.WekaCluster, error)
}

func NewWekaClusterService(client client.Client) WekaClusterService {
	return &wekaClusterService{
		Client: client,
	}
}

type wekaClusterService struct {
	Client client.Client
}

func (r *wekaClusterService) GetCluster(ctx context.Context, req ctrl.Request) (*wekav1alpha1.WekaCluster, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "FetchCluster")
	defer end()

	wekaCluster := &wekav1alpha1.WekaCluster{}
	err := r.Client.Get(ctx, req.NamespacedName, wekaCluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("wekaCluster resource not found. Ignoring since object must be deleted")
			return nil, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get wekaCluster")
		return nil, err
	}

	return wekaCluster, nil
}
