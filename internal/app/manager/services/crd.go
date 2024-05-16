package services

import (
	"context"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type CrdManager interface {
	GetCluster(ctx context.Context, req ctrl.Request) (WekaClusterService, error)
}

func NewCrdManager(mgr ctrl.Manager) CrdManager {
	return &crdManager{
		Manager: mgr,
	}
}

type crdManager struct {
	Manager ctrl.Manager
}

func (r *crdManager) GetCluster(ctx context.Context, req ctrl.Request) (WekaClusterService, error) {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "FetchCluster")
	defer end()

	wekaCluster := &wekav1alpha1.WekaCluster{}
	err := r.getClient().Get(ctx, req.NamespacedName, wekaCluster)
	if err != nil {
		wekaCluster = nil
		if apierrors.IsNotFound(err) {
			err = nil
		}
	}

	wekaClusterService := NewWekaClusterService(r.Manager, wekaCluster)

	return wekaClusterService, err
}

func (r *crdManager) getClient() client.Client {
	return r.Manager.GetClient()
}
