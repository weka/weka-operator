package controllers

import (
	"context"

	"github.com/go-logr/logr"
	clusterApi "github.com/weka/weka-operator/internal/app/manager/rest_api"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func NewClusterApiController(mgr ctrl.Manager) *ClusterApiController {
	logger := mgr.GetLogger().WithName("controllers").WithName("ClusterApiController")
	return &ClusterApiController{
		Client:     mgr.GetClient(),
		logger:     logger,
		clusterAPI: clusterApi.NewClusterAPI(mgr.GetClient(), logger),
	}
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
