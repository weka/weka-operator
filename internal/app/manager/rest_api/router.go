package restapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/ant0ine/go-json-rest/rest"
	"github.com/go-logr/logr"
	"github.com/kr/pretty"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

type UsernamePassword struct {
	Username string `json:"username"`
	Password string `json:"password"`
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

func (api *ClusterAPI) updateClusterPassword(w rest.ResponseWriter, r *rest.Request) {
	logger := api.logger.WithName("updateClusterPassword")

	ctx := r.Context()
	name := r.PathParam("name")
	namespace := r.PathParam("namespace")
	logger.Info("Change password", "name", name, "namespace", namespace)

	if name == "" || namespace == "" {
		err := pretty.Errorf("name and namespace are required")
		logger.Error(err, "Name and namespace are required")
		rest.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cluster := &wekav1alpha1.DummyCluster{}
	key := client.ObjectKey{Name: name, Namespace: namespace}
	err := api.client.Get(ctx, key, cluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Error(err, "Cluster not found")
			rest.Error(w, "Cluster not found", http.StatusNotFound)
			return
		}
		logger.Error(err, "Failed to get cluster")
		rest.Error(w, "Failed to get cluster", http.StatusInternalServerError)
		return
	}

	// Update the password
	credentials := &UsernamePassword{}
	err = r.DecodeJsonPayload(credentials)
	if err != nil {
		logger.Error(err, "Failed to decode JSON payload")
		rest.Error(w, "Failed to decode JSON payload", http.StatusBadRequest)
		return
	}

	// TODO: Update the password in the cluster

	w.WriteHeader(http.StatusOK)
}

func (api *ClusterAPI) registerRoutes() {
	router, err := rest.MakeRouter(
		rest.Get("/", api.index),
		rest.Get("/clusters/:namespace/:name", api.getCluster),
		rest.Get("/clusters/:namespace/:name/status", api.getClusterStatus),
		rest.Get("/clusters", api.listClusters),
		rest.Put("/clusters/:namespace/:name/password", api.updateClusterPassword),
	)
	if err != nil {
		api.logger.Error(err, "Failed to create router")
		return
	}
	api.api.SetApp(router)
}
