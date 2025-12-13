package wekacontainer

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
)

const (
	ProxyContainerNamePrefix = "weka-drives-proxy-"
)

// getProxyContainerName generates the proxy container name for a given node
func getProxyContainerName(nodeName weka.NodeName) string {
	return fmt.Sprintf("%s%s", ProxyContainerNamePrefix, nodeName)
}

// ensureProxyContainer ensures that an SSD proxy container exists on the node
// This function is called for drive containers that use drive sharing
func (r *containerReconcilerLoop) ensureProxyContainer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureProxyContainer")
	defer end()

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return errors.New("container has no node affinity, cannot ensure proxy")
	}

	proxyName := getProxyContainerName(nodeName)
	logger.SetValues("proxyName", proxyName, "node", nodeName)

	// Get operator namespace where proxy containers are deployed
	operatorNamespace, err := util.GetPodNamespace()
	if err != nil {
		return errors.Wrap(err, "failed to get operator namespace")
	}

	// Check if proxy container already exists
	existingProxy := &weka.WekaContainer{}
	err = r.Client.Get(ctx, client.ObjectKey{
		Name:      proxyName,
		Namespace: operatorNamespace,
	}, existingProxy)

	if err == nil {
		// Proxy already exists
		logger.Debug("Proxy container already exists")
		return nil
	}

	if !apierrors.IsNotFound(err) {
		// Unexpected error
		return errors.Wrap(err, "failed to check for existing proxy container")
	}

	// Proxy doesn't exist, create it
	logger.Info("Creating proxy container")

	// Get the owner cluster for reference
	cluster, err := r.getOwnerCluster(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get owner cluster")
	}

	operatorDeployment, err := util.GetOperatorDeployment(ctx, r.Client)
	if err != nil {
		return errors.Wrap(err, "failed to get operator deployment")
	}

	// Create the proxy container spec
	proxyContainer, err := r.buildProxyContainerSpec(cluster, nodeName, proxyName, operatorNamespace)
	if err != nil {
		return errors.Wrap(err, "failed to build proxy container spec")
	}

	// Set owner reference to the operator deployment
	if err := ctrl.SetControllerReference(operatorDeployment, proxyContainer, r.Scheme); err != nil {
		return errors.Wrap(err, "failed to set controller reference for proxy")
	}

	// Create the proxy container
	if err := r.Client.Create(ctx, proxyContainer); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Another drive container created it concurrently, this is fine
			logger.Info("Proxy container already created by another reconciler")
			return nil
		}
		return errors.Wrap(err, "failed to create proxy container")
	}

	logger.Info("Proxy container created successfully")

	return nil
}

// buildProxyContainerSpec creates the specification for a proxy container
func (r *containerReconcilerLoop) buildProxyContainerSpec(cluster *weka.WekaCluster, nodeName weka.NodeName, proxyName, namespace string) (*weka.WekaContainer, error) {
	// Build labels for the proxy container
	labels := util.MergeMaps(
		cluster.GetLabels(),
		factory.RequiredAnyWekaContainerLabels(weka.WekaContainerModeSSDProxy),
	)

	// TODO: Calculate appropriate resources based on total drive capacity
	// For now, use conservative defaults
	// Memory calculation: (totalCapacityTiB * 4 MiB) + 128 MiB base
	// This will be refined in testing

	proxyContainer := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      proxyName,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: weka.WekaContainerSpec{
			Mode:               weka.WekaContainerModeSSDProxy,
			NodeAffinity:       nodeName,
			WekaContainerName:  weka.WekaContainerModeSSDProxy,
			Image:              cluster.Spec.Image,
			ImagePullSecret:    cluster.Spec.ImagePullSecret,
			ServiceAccountName: cluster.Spec.ServiceAccountName,
			Tolerations:        cluster.Spec.RawTolerations,
			HostPID:            true, // Needed for drive access
			Hugepages:          config.Config.SsdProxy.HugepagesMi,
			HugepagesSize:      "2Mi",
			// Resources will be set by the pod factory based on container mode
		},
	}

	return proxyContainer, nil
}

// findSSDProxyOnNode finds the ssdproxy container on the same node as the current drive container
func (r *containerReconcilerLoop) findSSDProxyOnNode(ctx context.Context) (*weka.WekaContainer, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "findSSDProxyOnNode")
	defer end()

	container := r.container
	nodeName := container.GetNodeAffinity()
	if nodeName == "" {
		return nil, errors.New("container has no node affinity")
	}

	// Get the operator namespace where ssdproxy containers are deployed
	operatorNamespace, err := util.GetPodNamespace()
	if err != nil {
		return nil, fmt.Errorf("failed to get operator namespace: %w", err)
	}

	// List all ssdproxy containers in the operator namespace
	// Note: We don't filter by cluster because ssdproxy containers are shared across clusters on the same node
	kubeService := kubernetes.NewKubeService(r.Client)
	containers, err := kubeService.GetWekaContainersSimple(ctx, operatorNamespace, string(nodeName), map[string]string{
		"weka.io/mode": weka.WekaContainerModeSSDProxy,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list ssdpoxy containers on node %s: %w", nodeName, err)
	}

	if len(containers) == 0 {
		return nil, fmt.Errorf("no ssdproxy container found on node %s", nodeName)
	}

	proxy := containers[0]

	logger.Debug("Found ssdproxy container on node",
		"ssdproxy_name", proxy.Name,
		"ssdproxy_uid", proxy.UID,
		"node", nodeName,
	)

	return &proxy, nil
}
