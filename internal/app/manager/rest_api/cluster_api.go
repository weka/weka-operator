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
	cancel context.CancelFunc
}

func (api *ClusterAPI) IsStarted() bool {
	return api.cancel != nil
}

func (api *ClusterAPI) StartServer(ctx context.Context) {
	logger := api.logger.WithName("StartServer")
	logger.Info("Starting Cluster API server", "port", 8082)

	_, api.cancel = context.WithCancel(ctx)
	server := &http.Server{
		Addr:    ":8082",
		Handler: api.api.MakeHandler(),
	}
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		logger.Error(err, "Server closed")
	} else if err != nil {
		logger.Error(err, "Failed to start server")
	}
	api.cancel()
	api.cancel = nil
}

func (api *ClusterAPI) index(w rest.ResponseWriter, r *rest.Request) {
	w.WriteJson(map[string]string{"message": "Welcome to the Weka Operator Cluster API"})
}

func (api *ClusterAPI) getCluster(w rest.ResponseWriter, r *rest.Request) {
	logger := api.logger.WithName("getCluster")

	// Get the cluster object
	ctx := r.Context()
	name := r.URL.Query().Get("name")
	namespace := r.URL.Query().Get("namespace")

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

	// Write the JSON response
	w.Header().Set("Content-Type", "application/json")
	w.WriteJson(cluster)
	return
}

func (api *ClusterAPI) registerRoutes() {
	router, err := rest.MakeRouter(
		rest.Get("/", api.index),
		rest.Get("/cluster", api.getCluster),
	)
	if err != nil {
		api.logger.Error(err, "Failed to create router")
		return
	}
	api.api.SetApp(router)
}
