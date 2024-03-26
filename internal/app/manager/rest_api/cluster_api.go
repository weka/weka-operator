package restapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"

	"github.com/go-logr/logr"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func NewClusterAPI(mgr ctrl.Manager) *ClusterAPI {
	ctx, cancel := context.WithCancel(context.Background())
	mux := http.NewServeMux()
	api := &ClusterAPI{
		logger: mgr.GetLogger().WithName("ClusterAPI"),
		cancel: cancel,
		mux:    mux,
		server: &http.Server{
			Addr:    ":8082",
			Handler: mux,
			BaseContext: func(listener net.Listener) context.Context {
				return context.WithValue(ctx, "serverAddr", listener.Addr().String())
			},
		},
		client: mgr.GetClient(),
	}
	api.registerRoutes()
	return api
}

type ClusterAPI struct {
	logger logr.Logger
	ctx    context.Context
	cancel context.CancelFunc
	mux    *http.ServeMux
	server *http.Server
	client client.Client
}

func (api *ClusterAPI) StartServer(mgr ctrl.Manager) {
	logger := api.logger.WithName("StartServer")
	logger.Info("Starting Cluster API server", "port", 8082)
	err := api.server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		logger.Error(err, "Server closed")
	} else if err != nil {
		logger.Error(err, "Failed to start server")
	}
	api.cancel()
}

func (api *ClusterAPI) index(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("Hello, World!"))
}

func (api *ClusterAPI) getCluster(w http.ResponseWriter, r *http.Request) {
	logger := api.logger.WithName("getCluster")

	// Get the cluster object
	ctx := r.Context()
	name := r.URL.Query().Get("name")
	namespace := r.URL.Query().Get("namespace")

	if name == "" || namespace == "" {
		logger.Error(errors.New("name and namespace are required"), "Name and namespace are required")
		http.Error(w, "Name and namespace are required", http.StatusBadRequest)
		return
	}

	cluster := &wekav1alpha1.DummyCluster{}
	key := client.ObjectKey{Name: name, Namespace: namespace}
	err := api.client.Get(ctx, key, cluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Error(err, "Cluster not found")
			http.Error(w, "Cluster not found", http.StatusNotFound)
			return
		}
		logger.Error(err, "Failed to get cluster")
		http.Error(w, "Failed to get cluster", http.StatusInternalServerError)
		return
	}

	// Marshal the cluster object to JSON
	data, err := json.Marshal(cluster)
	if err != nil {
		logger.Error(err, "Failed to marshal cluster")
		http.Error(w, "Failed to marshal cluster", http.StatusInternalServerError)
		return
	}

	// Write the JSON response
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
	return
}

func (api *ClusterAPI) registerRoutes() {
	api.mux.HandleFunc("/", api.index)
	api.mux.HandleFunc("/cluster", api.getCluster)
}
