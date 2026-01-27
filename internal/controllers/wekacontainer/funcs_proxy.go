package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/discovery"
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

	// Create the proxy container spec
	proxyContainer, err := r.buildProxyContainerSpec(ctx, cluster, nodeName, proxyName, operatorNamespace)
	if err != nil {
		return errors.Wrap(err, "failed to build proxy container spec")
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
func (r *containerReconcilerLoop) buildProxyContainerSpec(ctx context.Context, cluster *weka.WekaCluster, nodeName weka.NodeName, proxyName, namespace string) (*weka.WekaContainer, error) {
	// Build labels for the proxy container
	labels := util.MergeMaps(
		cluster.GetLabels(),
		factory.RequiredAnyWekaContainerLabels(weka.WekaContainerModeSSDProxy),
	)

	// Calculate hugepages based on shared drives on the node
	hugepagesMiB, err := r.calculateProxyHugepages(ctx, nodeName)
	if err != nil {
		return nil, errors.Wrap(err, "failed to calculate hugepages for proxy container")
	}

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
			Hugepages:          hugepagesMiB + config.Config.DriveSharing.SsdProxyHugepagesOffsetMiB,
			HugepagesSize:      "2Mi",
		},
	}

	return proxyContainer, nil
}

// calculateProxyHugepages calculates the required hugepages for ssd_proxy
// based on the shared drives available on the node
func (r *containerReconcilerLoop) calculateProxyHugepages(ctx context.Context, nodeName weka.NodeName) (int, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "calculateProxyHugepages", "node", nodeName)
	defer end()

	// Get the node to read annotations
	node := &corev1.Node{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node); err != nil {
		return 0, errors.Wrap(err, "failed to get node")
	}

	// Parse shared drives annotation
	sharedDrivesStr, ok := node.Annotations[consts.AnnotationSharedDrives]
	if !ok || sharedDrivesStr == "" {
		return 0, errors.New("node has no shared drives annotation")
	}

	var sharedDrives []domain.SharedDriveInfo
	if err := json.Unmarshal([]byte(sharedDrivesStr), &sharedDrives); err != nil {
		return 0, errors.Wrap(err, "failed to parse shared drives annotation")
	}

	if len(sharedDrives) == 0 {
		return 0, errors.New("no shared drives found in annotation")
	}

	// Calculate maxDrives and expectedMaxDriveTiB
	maxDrives := len(sharedDrives)
	maxCapacityGiB := 0
	for _, drive := range sharedDrives {
		if drive.CapacityGiB > maxCapacityGiB {
			maxCapacityGiB = drive.CapacityGiB
		}
	}

	// Convert GiB to TiB (round up to be safe)
	expectedMaxDriveTiB := (maxCapacityGiB + 1023) / 1024

	// Calculate hugepages in KiB using the formula
	hugepagesKiB := resources.GetSsdProxyHugeTLBKiB(maxDrives, expectedMaxDriveTiB)

	// Convert KiB to MiB (round up)
	hugepagesMiB := int((hugepagesKiB + 1023) / 1024)

	logger.Info("Calculated hugepages for ssd_proxy",
		"maxDrives", maxDrives,
		"expectedMaxDriveTiB", expectedMaxDriveTiB,
		"hugepagesMiB", hugepagesMiB,
	)

	return hugepagesMiB, nil
}

// findSSDProxyOnNode finds the ssdproxy container on the same node as the current drive container
func (r *containerReconcilerLoop) findSSDProxyOnNode(ctx context.Context) (*weka.WekaContainer, error) {
	container := r.container
	nodeName := container.GetNodeAffinity()
	if nodeName == "" {
		return nil, errors.New("container has no node affinity")
	}

	return discovery.GetSsdProxyOnNode(ctx, r.Client, nodeName)
}
