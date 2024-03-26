package restapi

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
)

func NewClusterAPI(mgr ctrl.Manager) *ClusterAPI {
	ctx, cancel := context.WithCancel(context.Background())
	mux := http.NewServeMux()
	api := &ClusterAPI{
		logger: mgr.GetLogger().WithName("ClusterAPI"),
		ctx:    ctx,
		cancel: cancel,
		mux:    mux,
		server: &http.Server{
			Addr:    ":8082",
			Handler: mux,
			BaseContext: func(listener net.Listener) context.Context {
				return context.WithValue(ctx, "serverAddr", listener.Addr().String())
			},
		},
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
	w.Write([]byte("Hello, Cluster!"))
}

func (api *ClusterAPI) registerRoutes() {
	api.mux.HandleFunc("/", api.index)
	api.mux.HandleFunc("/cluster", api.getCluster)
}
