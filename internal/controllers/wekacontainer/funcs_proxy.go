package wekacontainer

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	apiutil "github.com/weka/weka-k8s-api/util"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/controllers/resources"
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
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ensureProxyContainer")
	defer logger.End()

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
	err = r.Get(ctx, client.ObjectKey{
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
	if err := r.Create(ctx, proxyContainer); err != nil {
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

// waitForProxyReady blocks the drive container flow until the SSD proxy container
// on the same node is fully up (Status == Running and InternalStatus == READY).
// This ensures the proxy is serving before drive pods are scheduled.
func (r *containerReconcilerLoop) waitForProxyReady(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "waitForProxyReady")
	defer logger.End()

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return errors.New("container has no node affinity, cannot wait for proxy")
	}

	proxyName := getProxyContainerName(nodeName)
	logger.SetValues("proxyName", proxyName, "node", nodeName)

	operatorNamespace, err := util.GetPodNamespace()
	if err != nil {
		return errors.Wrap(err, "failed to get operator namespace")
	}

	proxy := &weka.WekaContainer{}
	err = r.Get(ctx, client.ObjectKey{
		Name:      proxyName,
		Namespace: operatorNamespace,
	}, proxy)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return lifecycle.NewWaitErrorWithDuration(
				errors.Errorf("proxy container %s not found yet", proxyName), 10*time.Second)
		}
		return errors.Wrap(err, "failed to get proxy container")
	}

	if proxy.Status.Status != weka.Running || proxy.Status.InternalStatus != "READY" {
		logger.SetValues("proxyStatus", proxy.Status.Status, "proxyInternalStatus", proxy.Status.InternalStatus)
		logger.Info("Waiting for proxy container to be ready")
		return lifecycle.NewWaitErrorWithDuration(
			errors.Errorf("proxy container %s not ready yet (status=%s, internalStatus=%s)",
				proxyName, proxy.Status.Status, proxy.Status.InternalStatus), 10*time.Second)
	}

	logger.Debug("Proxy container is ready")
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
	hugepagesTotal, hugepagesOffset, err := r.desiredProxyHugepages(ctx, nodeName)
	if err != nil {
		return nil, errors.Wrap(err, "failed to calculate hugepages for proxy container")
	}

	image := cluster.Spec.Image
	if override := config.Config.DriveSharing.SsdProxyImageOverride; override != "" {
		image = override
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
			Image:              image,
			ImagePullSecret:    cluster.Spec.ImagePullSecret,
			ServiceAccountName: cluster.Spec.ServiceAccountName,
			DriversDistService: cluster.Spec.DriversDistService,
			DriversLoaderImage: cluster.Spec.GetOverrides().DriversLoaderImage,
			DriversBuildId:     cluster.Spec.GetOverrides().DriversBuildId,
			Tolerations:        apiutil.ExpandTolerations([]v1.Toleration{}, cluster.Spec.Tolerations, cluster.Spec.RawTolerations),
			Hugepages:          hugepagesTotal,
			HugepagesOffset:    hugepagesOffset,
			HugepagesSize:      "2Mi",
		},
	}

	return proxyContainer, nil
}

// calculateProxyHugepages calculates the required hugepages for ssd_proxy
// based on the shared drives available on the node
func (r *containerReconcilerLoop) calculateProxyHugepages(ctx context.Context, nodeName weka.NodeName) (int, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "calculateProxyHugepages", "node", nodeName)
	defer logger.End()

	// Get the node to read annotations
	node := &v1.Node{}
	if err := r.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node); err != nil {
		return 0, errors.Wrap(err, "failed to get node")
	}

	// Parse shared drives via the canonical allocator reader, which excludes blocked drives
	// (weka.io/blocked-drives serials and weka.io/blocked-drives-physical-uuids UUIDs).
	info, err := allocator.ParseAllocatorNodeInfo(node)
	if err != nil {
		return 0, errors.Wrap(err, "failed to parse allocator node info")
	}
	sharedDrives := info.SharedDrives
	if len(sharedDrives) == 0 {
		return 0, errors.New("node has no shared drives")
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

	// Calculate hugepages in MiB using the formula
	hugepagesMiB := int(resources.GetSsdProxyHugepagesMiB(maxDrives, expectedMaxDriveTiB))

	logger.Info("Calculated hugepages for ssd_proxy",
		"maxDrives", maxDrives,
		"expectedMaxDriveTiB", expectedMaxDriveTiB,
		"hugepagesMiB", hugepagesMiB,
	)

	return hugepagesMiB, nil
}

// calculateProxyHugepagesTotal returns the total hugepages (MiB) that should be configured
// on the ssd_proxy container: drive-derived hugepages, plus DPDK memory, plus the configured offset.
func (r *containerReconcilerLoop) calculateProxyHugepagesTotal(ctx context.Context, nodeName weka.NodeName) (int, error) {
	hugepagesMiB, err := r.calculateProxyHugepages(ctx, nodeName)
	if err != nil {
		return 0, err
	}
	return hugepagesMiB + config.Consts.SsdProxyDpdkMemoryMiB + config.Config.DriveSharing.SsdProxyHugepagesOffsetMiB, nil
}

// desiredProxyHugepages returns the hugepages total and offset an ssdproxy container should have,
// derived from the node's shared drives. Same values used at container creation.
func (r *containerReconcilerLoop) desiredProxyHugepages(ctx context.Context, nodeName weka.NodeName) (hugepages, offset int, err error) {
	total, err := r.calculateProxyHugepagesTotal(ctx, nodeName)
	if err != nil {
		return 0, 0, err
	}
	return total, config.Config.DriveSharing.SsdProxyHugepagesOffsetMiB, nil
}

// reconcileProxyHugepagesSpec keeps the ssdproxy container's hugepages spec in sync with the node's
// current shared-drive capacity, using the same calculation as container creation. The operator does
// not recreate the pod itself: like compute/drive containers, it surfaces a Warning event and leaves
// pod recreation to the user (the new hugepages/memory take effect only after a manual pod restart).
func (r *containerReconcilerLoop) reconcileProxyHugepagesSpec(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return nil
	}

	desiredHugepages, desiredOffset, err := r.desiredProxyHugepages(ctx, nodeName)
	if err != nil {
		logger.Info("Skipping ssdproxy hugepages spec reconcile, failed to calculate hugepages", "error", err)
		return nil
	}

	// Increase-only policy: never propagate a decrease (would shrink MEMORY and needlessly recreate the pod).
	if desiredHugepages <= r.container.Spec.Hugepages {
		if desiredHugepages < r.container.Spec.Hugepages {
			logger.Info("Skipping ssdproxy hugepages decrease (increase-only policy)",
				"currentHugepages", r.container.Spec.Hugepages, "desiredHugepages", desiredHugepages)
		}
		return nil
	}

	oldHugepages := r.container.Spec.Hugepages
	oldOffset := r.container.Spec.HugepagesOffset
	logger.Info("Updating ssdproxy container hugepages spec",
		"oldHugepages", oldHugepages, "newHugepages", desiredHugepages,
		"oldOffset", oldOffset, "newOffset", desiredOffset)
	r.container.Spec.Hugepages = desiredHugepages
	r.container.Spec.HugepagesOffset = desiredOffset
	if err := r.Update(ctx, r.container); err != nil {
		return errors.Wrap(err, "failed to update ssdproxy hugepages spec")
	}
	_ = r.RecordEvent(v1.EventTypeWarning, "CapacityGrowthApplied", //nolint:errcheck // error return value intentionally not checked
		fmt.Sprintf("ssdproxy hugepages increased from %dMiB (offset %dMiB) to %dMiB (offset %dMiB); the pod must be manually recreated to apply the new hugepages/memory",
			oldHugepages, oldOffset, desiredHugepages, desiredOffset))
	return nil
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
