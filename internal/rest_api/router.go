package restapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/ant0ine/go-json-rest/rest"
	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func NewClusterAPI(client client.Client, logger logr.Logger) *ClusterAPI {
	api := rest.NewApi()
	api.Use(rest.DefaultDevStack...)

	clusterAPI := &ClusterAPI{
		client: client,
		logger: logger.WithName("ClusterAPI"),
		api:    api,
	}

	clusterAPI.registerRoutes()
	return clusterAPI
}

type ClusterAPI struct {
	client client.Client
	logger logr.Logger
	api    *rest.Api
}

func (api *ClusterAPI) StartServer(ctx context.Context) {
	logger := api.logger.WithName("StartServer")
	logger.Info("Starting Cluster API server", "port", 8082)

	server := &http.Server{
		Addr:    ":8082",
		Handler: api.api.MakeHandler(),
	}

	go func() {
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			logger.Error(err, "Server closed")
		} else if err != nil {
			logger.Error(err, "Failed to start server")
		}
		logger.Info("Server stopped")
	}()
}

func (api *ClusterAPI) index(w rest.ResponseWriter, r *rest.Request) {
	w.WriteJson(map[string]string{"message": "Welcome to the Weka Operator Cluster API"})
}

func (api *ClusterAPI) registerRoutes() {
	router, err := rest.MakeRouter(
		rest.Get("/", api.index),
		rest.Get("/clusters/:namespace/:name", api.getCluster),
		rest.Get("/clusters/:namespace/:name/status", api.getClusterStatus),
		rest.Post("/clusters/:namespace/:name", api.createCluster),
		rest.Get("/clusters", api.listClusters),
		rest.Put("/clusters/:namespace/:name/password", api.updateClusterPassword),
	)
	if err != nil {
		api.logger.Error(err, "Failed to create router")
		return
	}
	api.api.SetApp(router)
}
