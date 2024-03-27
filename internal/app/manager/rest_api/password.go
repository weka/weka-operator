package restapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ant0ine/go-json-rest/rest"
	"github.com/kr/pretty"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/util"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type UsernamePassword struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (api *ClusterAPI) updateClusterPassword(w rest.ResponseWriter, r *rest.Request) {
	logger := api.logger.WithName("updateClusterPassword")

	name := r.PathParam("name")
	namespace := r.PathParam("namespace")
	logger.Info("Change password", "name", name, "namespace", namespace)

	ctx := r.Context()
	cluster, err := api.validateClusterExists(ctx, name, namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			rest.Error(w, "Cluster not found", http.StatusNotFound)
			return
		}
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
	username := credentials.Username
	password := credentials.Password
	pod, err := api.getPodForCluster(ctx, cluster)
	if err != nil {
		logger.Error(err, "Failed to get pod for cluster")
		rest.Error(w, "Failed to get pod for cluster", http.StatusInternalServerError)
		return
	}

	exec, err := util.NewExecInPod(pod)
	if err != nil {
		logger.Error(err, "Failed to create exec in pod")
		rest.Error(w, "Failed to create exec in pod", http.StatusInternalServerError)
		return
	}

	command := []string{
		"bash", "-c",
		fmt.Sprintf(
			"WEKA_USERNAME=admin WEKA_PASSWORD=admin weka user add %s %s %s",
			username,
			"clusteradmin",
			password,
		),
	}

	stdout, stderr, err := exec.Exec(ctx, command)
	if err != nil {
		logger.Error(err, "Failed to execute command", "stdout", stdout.String(), "stderr", stderr.String())
		rest.Error(w, "Failed to execute command", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (api *ClusterAPI) validateClusterExists(ctx context.Context, name string, namespace string) (*wekav1alpha1.WekaCluster, error) {
	logger := api.logger.WithName("validateClusterExists")

	if name == "" || namespace == "" {
		err := pretty.Errorf("name and namespace are required")
		logger.Error(err, "Name and namespace are required")
		return nil, err
	}

	cluster := &wekav1alpha1.WekaCluster{}
	key := client.ObjectKey{Name: name, Namespace: namespace}
	err := api.client.Get(ctx, key, cluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Error(err, "Cluster not found")
			return nil, err
		}
		logger.Error(err, "Failed to get cluster")
		return nil, err
	}

	return cluster, nil
}

// getPodForCluster finds a pod belonging to a cluster
func (api *ClusterAPI) getPodForCluster(ctx context.Context, cluster *wekav1alpha1.WekaCluster) (*v1.Pod, error) {
	pods := &v1.PodList{}
	inNamespace := client.InNamespace(cluster.Namespace)
	matchLabels := client.MatchingLabels{"app.kubernetes.io/name": "WekaContainer"}
	if err := api.client.List(ctx, pods, inNamespace, matchLabels); err != nil {
		return nil, err
	}

	if len(pods.Items) == 0 {
		return nil, pretty.Errorf("No pods found for cluster %s", cluster.Name)
	}

	return &pods.Items[0], nil
}
