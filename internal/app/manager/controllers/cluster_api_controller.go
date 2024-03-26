package controllers

import (
	"context"

	"github.com/go-logr/logr"
	clusterApi "github.com/weka/weka-operator/internal/app/manager/rest_api"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func NewClusterApiController(mgr ctrl.Manager) *ClusterApiController {
	logger := mgr.GetLogger().WithName("controllers").WithName("ClusterApiController")
	return &ClusterApiController{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		logger:     logger,
		clusterAPI: clusterApi.NewClusterAPI(mgr.GetClient(), logger),
	}
}

type ClusterApiController struct {
	client.Client
	Scheme     *runtime.Scheme
	logger     logr.Logger
	clusterAPI *clusterApi.ClusterAPI
}

func (r *ClusterApiController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.logger.WithName("Reconcile")
	logger.Info("Reconciling ClusterApi")

	cluster := &wekav1alpha1.DummyCluster{}
	if err := r.Get(ctx, req.NamespacedName, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			// Wait for cluster to be created
			logger.Info("Cluster not found")
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Error(err, "Failed to get cluster")
		return ctrl.Result{}, err
	}

	if !r.clusterAPI.IsStarted() {
		go r.clusterAPI.StartServer(ctx)
	}

	return ctrl.Result{}, nil
}

func (api *ClusterApiController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.DummyCluster{}).
		Complete(api)
}
