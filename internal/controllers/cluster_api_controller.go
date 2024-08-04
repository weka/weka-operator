package controllers

import (
	"context"

	"github.com/go-logr/logr"
	clusterApi "github.com/weka/weka-operator/internal/rest_api"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func NewClusterApiController(mgr ctrl.Manager) *ClusterApiController {
	logger := mgr.GetLogger().WithName("controllers").WithName("ClusterApiController")
	apiController := &ClusterApiController{
		Client:     mgr.GetClient(),
		logger:     logger,
		clusterAPI: clusterApi.NewClusterAPI(mgr.GetClient(), logger),
	}
	// TODO: Move to main-level to have runtime-scoped context and not reconcile request scoped
	go apiController.clusterAPI.StartServer(context.Background())
	return apiController
}

type ClusterApiController struct {
	client.Client
	logger     logr.Logger
	clusterAPI *clusterApi.ClusterAPI
}

func (api *ClusterApiController) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(api)
}

func (api *ClusterApiController) Start(ctx context.Context) error {
	logger := api.logger.WithName("Start")
	logger.Info("Starting ClusterApiController")
	api.clusterAPI.StartServer(ctx)

	return nil
}
