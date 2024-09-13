package restapi

import (
	"context"
	"net/http"
	"time"

	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ant0ine/go-json-rest/rest"
	"github.com/kr/pretty"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
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

	cluster := &wekav1alpha1.WekaCluster{}
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

	cluster := &wekav1alpha1.WekaCluster{}
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
	clusters := &wekav1alpha1.WekaClusterList{}
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

type CreateClusterApiResponse struct {
	Message  string `json:"message"`
	Password string `json:"password"`
	Username string `json:"username"`
}

func (api *ClusterAPI) createCluster(w rest.ResponseWriter, r *rest.Request) {
	logger := api.logger.WithName("createCluster")

	// Parse the request body
	clusterSpec := wekav1alpha1.WekaClusterSpec{}
	if err := r.DecodeJsonPayload(&clusterSpec); err != nil {
		logger.Error(err, "Failed to decode request body")
		rest.Error(w, "Failed to decode request body", http.StatusBadRequest)
		return
	}

	name := r.PathParam("name")
	namespace := r.PathParam("namespace")

	cluster := wekav1alpha1.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: clusterSpec,
	}

	// FormCluster the cluster object
	logger.Info("Creating cluster", "name", cluster.Name, "namespace", cluster.Namespace)
	ctx := r.Context()
	key := client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}
	if err := api.client.Get(ctx, key, &wekav1alpha1.WekaCluster{}); err == nil {
		logger.Error(err, "Cluster already exists")
		rest.Error(w, "Cluster already exists", http.StatusConflict)
		return
	}

	if err := api.client.Create(ctx, &cluster); err != nil {
		logger.Error(err, "Failed to create cluster")
		rest.Error(w, "Failed to create cluster", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(180*time.Second))
	defer cancel()

	for {
		cluster := &wekav1alpha1.WekaCluster{}
		key := client.ObjectKey{Name: name, Namespace: namespace}
		err := api.client.Get(ctx, key, cluster)
		if err != nil {
			if ctx.Err() != nil {
				rest.Error(w, "cluster was not created within 30 seconds, aborting", http.StatusInternalServerError)
			}
			if apierrors.IsNotFound(err) {
				time.Sleep(1 * time.Second)
				continue
			}
			logger.Error(err, "Unexpected error")
			rest.Error(w, "Unexpected error", http.StatusInternalServerError)
		}

		if meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondClusterSecretsCreated) {
			secret := &corev1.Secret{}
			key := client.ObjectKey{Name: cluster.GetUserSecretName(), Namespace: cluster.Namespace}
			err := api.client.Get(ctx, key, secret)
			if err != nil {
				if apierrors.IsNotFound(err) {
					logger.Error(err, "debug-msg-to-delete")
					continue
				}
				logger.Error(err, "Failed to get secret")
				rest.Error(w, "Failed to get secret", http.StatusInternalServerError)
				return
			}

			w.WriteJson(&CreateClusterApiResponse{
				Message:  "Cluster created successfully, password is not available on any other API, other than this specific response, unless directly using k8s API for secrets",
				Username: string(secret.Data["username"]),
				Password: string(secret.Data["password"]),
			})
			return
		}
		time.Sleep(1 * time.Second)
	}
	// w.WriteHeader(http.StatusCreated)
}
