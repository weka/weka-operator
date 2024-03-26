package restapi

import (
	"net/http"

	"github.com/ant0ine/go-json-rest/rest"
	"github.com/kr/pretty"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (api *ClusterAPI) getCluster(w rest.ResponseWriter, r *rest.Request) {
	logger := api.logger.WithName("getCluster")

	// Get the cluster object
	ctx := r.Context()
	name := r.PathParam("name")
	namespace := r.PathParam("namespace")
	logger.Info("Getting cluster", "name", name, "namespace", namespace)

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

func (api *ClusterAPI) getClusterStatus(w rest.ResponseWriter, r *rest.Request) {
	logger := api.logger.WithName("getClusterStatus")

	ctx := r.Context()
	name := r.PathParam("name")
	namespace := r.PathParam("namespace")
	logger.Info("Getting cluster status", "name", name, "namespace", namespace)

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

	w.Header().Set("Content-Type", "application/json")
	w.WriteJson(cluster.Status)
}

func (api *ClusterAPI) listClusters(w rest.ResponseWriter, r *rest.Request) {
	logger := api.logger.WithName("listClusters")
	logger.Info("Listing clusters")

	// List all clusters
	ctx := r.Context()
	clusters := &wekav1alpha1.DummyClusterList{}
	err := api.client.List(ctx, clusters)
	if err != nil {
		logger.Error(err, "Failed to list clusters")
		rest.Error(w, "Failed to list clusters", http.StatusInternalServerError)
		return
	}

	// Write the JSON response
	w.Header().Set("Content-Type", "application/json")
	w.WriteJson(clusters)
	return
}
