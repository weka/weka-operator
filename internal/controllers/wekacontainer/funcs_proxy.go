package wekacontainer

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/factory"
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

	// Check if proxy container already exists
	existingProxy := &weka.WekaContainer{}
	err := r.Client.Get(ctx, client.ObjectKey{
		Name:      proxyName,
		Namespace: r.container.Namespace,
	}, existingProxy)

	if err == nil {
		// Proxy already exists
		logger.Info("Proxy container already exists")
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
	proxyContainer, err := r.buildProxyContainerSpec(cluster, nodeName, proxyName, operatorDeployment.Namespace)
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

// countDriveContainersOnNode counts how many drive containers with drive sharing
// exist on the same node as the current container
func (r *containerReconcilerLoop) countDriveContainersOnNode(ctx context.Context) (int, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "countDriveContainersOnNode")
	defer end()

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return 0, errors.New("container has no node affinity")
	}

	// List all WekaContainers in the same namespace
	containerList := &weka.WekaContainerList{}
	if err := r.Client.List(ctx, containerList, client.InNamespace(r.container.Namespace)); err != nil {
		return 0, errors.Wrap(err, "failed to list containers")
	}

	count := 0
	for _, c := range containerList.Items {
		// Count drive containers with drive sharing on the same node
		if c.IsDriveContainer() && c.UsesDriveSharing() && c.GetNodeAffinity() == nodeName {
			// Don't count containers that are being deleted
			if c.DeletionTimestamp == nil {
				count++
			}
		}
	}

	logger.Info("Counted drive containers on node", "count", count, "node", nodeName)
	return count, nil
}

// shouldDeleteProxyContainer checks if the proxy container should be deleted
// This is called during container deletion to determine if the proxy is still needed
func (r *containerReconcilerLoop) shouldDeleteProxyContainer(ctx context.Context) (bool, error) {
	// Only relevant for drive containers with drive sharing
	if !r.container.IsDriveContainer() || !r.container.UsesDriveSharing() {
		return false, nil
	}

	count, err := r.countDriveContainersOnNode(ctx)
	if err != nil {
		return false, err
	}

	// Delete proxy if this is the last drive container on the node
	// count includes the current container, so delete if count <= 1
	return count <= 1, nil
}

// cleanupProxyIfNeeded checks if proxy should be deleted and deletes it if this is the last drive container
func (r *containerReconcilerLoop) cleanupProxyIfNeeded(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "cleanupProxyIfNeeded")
	defer end()

	shouldDelete, err := r.shouldDeleteProxyContainer(ctx)
	if err != nil {
		logger.Error(err, "Failed to determine if proxy should be deleted")
		return err
	}

	if !shouldDelete {
		logger.Info("Other drive containers still exist on node, keeping proxy")
		return nil
	}

	logger.Info("This is the last drive container on node, deleting proxy")
	return r.deleteProxyContainer(ctx)
}

// deleteProxyContainer deletes the proxy container for the current node
func (r *containerReconcilerLoop) deleteProxyContainer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "deleteProxyContainer")
	defer end()

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		// No node affinity, nothing to clean up
		return nil
	}

	proxyName := getProxyContainerName(nodeName)
	logger.SetValues("proxyName", proxyName, "node", nodeName)

	operatorNamespace, err := util.GetPodNamespace()
	if err != nil {
		return errors.Wrap(err, "failed to get operator namespace")
	}

	// Get the proxy container
	proxyContainer := &weka.WekaContainer{}
	err = r.Client.Get(ctx, client.ObjectKey{
		Name:      proxyName,
		Namespace: operatorNamespace,
	}, proxyContainer)

	if apierrors.IsNotFound(err) {
		// Proxy doesn't exist, nothing to do
		logger.Info("Proxy container already deleted")
		return nil
	}

	if err != nil {
		return errors.Wrap(err, "failed to get proxy container")
	}

	// Delete the proxy container
	logger.Info("Deleting proxy container")
	if err := r.Client.Delete(ctx, proxyContainer); err != nil {
		if apierrors.IsNotFound(err) {
			// Already deleted
			return nil
		}
		return errors.Wrap(err, "failed to delete proxy container")
	}

	logger.Info("Proxy container deleted successfully")
	_ = r.RecordEvent(v1.EventTypeNormal, "ProxyDeleted", fmt.Sprintf("Deleted SSD proxy container %s from node %s", proxyName, nodeName))

	return nil
}
