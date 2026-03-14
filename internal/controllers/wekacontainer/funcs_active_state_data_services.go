package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

// ensureSiblingFECreated ensures a data-services-fe container exists on the same node
// as this data-services container. The FE container is created by the data-services
// container to ensure they are co-located on the same node.
func (r *containerReconcilerLoop) ensureSiblingFECreated(ctx context.Context) error {
	if !r.container.IsDataServicesContainer() {
		return nil
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureSiblingFECreated")
	defer end()

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		// Container not yet scheduled to a node
		return nil
	}

	ownerRefs := r.container.GetOwnerReferences()
	if len(ownerRefs) == 0 {
		return errors.New("no owner references found")
	}

	ownerUid := string(ownerRefs[0].UID)

	// Check if sibling FE container already exists on the same node
	// Note: We don't filter by node in the query because newly created containers
	// don't have status.nodeAffinity set yet. Instead, we filter the results manually.
	feContainers, err := r.KubeService.GetWekaContainersSimple(ctx, r.container.Namespace, "", map[string]string{
		domain.WekaLabelClusterId: ownerUid,
		domain.WekaLabelMode:      weka.WekaContainerModeDataServicesFe,
	})
	if err != nil {
		return err
	}

	// Filter by node affinity (check both spec and status)
	for _, fe := range feContainers {
		feNodeAffinity := fe.Spec.NodeAffinity
		if feNodeAffinity == "" {
			feNodeAffinity = fe.Status.NodeAffinity
		}
		if feNodeAffinity == nodeName {
			logger.Debug("Sibling data-services-fe container already exists on node", "node", nodeName, "fe_container", fe.Name)
			return nil
		}
	}

	// Get cluster to create FE container
	cluster, err := r.getCluster(ctx)
	if err != nil {
		return fmt.Errorf("failed to get cluster: %w", err)
	}

	// Get template for container configuration
	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		return errors.New("failed to get cluster template")
	}

	// Create the FE container
	feName := allocator.NewContainerName(weka.WekaContainerModeDataServicesFe)
	feContainer, err := factory.NewWekaContainerForWekaCluster(cluster, template, weka.WekaContainerModeDataServicesFe, feName)
	if err != nil {
		return fmt.Errorf("failed to build FE container: %w", err)
	}

	// Set node affinity to the same node as data-services
	feContainer.Spec.NodeAffinity = nodeName

	// Set owner reference to the cluster
	if err := controllerutil.SetControllerReference(cluster, feContainer, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference: %w", err)
	}

	logger.Info("Creating sibling data-services-fe container",
		"fe_container", feContainer.Name,
		"node", nodeName,
	)

	if err := r.Client.Create(ctx, feContainer); err != nil {
		return fmt.Errorf("failed to create FE container: %w", err)
	}

	return lifecycle.NewWaitError(errors.New("waiting for sibling data-services-fe container to be created"))
}

func (r *containerReconcilerLoop) deleteDataServicesFEIfNoDataServicesNeighbor(ctx context.Context) error {
	if !r.container.IsDataServicesFEContainer() {
		return nil // only data-services-fe containers should be checked
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	nodeName := r.container.GetNodeAffinity()

	if nodeName != "" {
		ownerRefs := r.container.GetOwnerReferences()
		if len(ownerRefs) == 0 {
			return errors.New("no owner references found")
		} else if len(ownerRefs) > 1 {
			return errors.New("more than one owner reference found")
		}

		ownerUid := string(ownerRefs[0].UID)
		// Check if there are any data-services wekacontainers on the same node
		dataServicesContainers, err := r.KubeService.GetWekaContainersSimple(ctx, r.container.Namespace, string(nodeName), map[string]string{
			domain.WekaLabelClusterId: ownerUid,
			domain.WekaLabelMode:      weka.WekaContainerModeDataServices,
		})
		if err != nil {
			return err
		}
		if len(dataServicesContainers) > 0 {
			logger.Debug("Found data-services neighbor, not deleting data-services-fe container")
			return nil
		}
	}

	noDataServicesNeighborKey := "NoDataServicesNeighbor"

	if r.container.Status.Timestamps == nil {
		r.container.Status.Timestamps = make(map[string]metav1.Time)
	}
	if since, ok := r.container.Status.Timestamps[noDataServicesNeighborKey]; !ok {
		r.container.Status.Timestamps[noDataServicesNeighborKey] = metav1.Time{Time: time.Now()}
		if err := r.Status().Update(ctx, r.container); err != nil {
			return err
		}

		return lifecycle.NewWaitErrorWithDuration(
			errors.New("Data-services-fe container has no data-services neighbor, waiting before deleting it"),
			time.Second*15,
		)
	} else if time.Since(since.Time) < config.Config.DeleteDataServicesFEWithoutDataServicesNeighborTimeout {
		logger.Info("Data-services-fe container has no data-services neighbor, but waiting before deleting it",
			"waited", time.Since(since.Time).String(),
			"node", nodeName,
		)
		return nil
	}

	_ = r.RecordEvent(
		v1.EventTypeNormal,
		"DataServicesFEContainerWithoutDataServicesNeighbor",
		"Data-services-fe container has no data-services neighbor, deleting it",
	)

	if err := r.Client.Delete(ctx, r.container); err != nil {
		return errors.Wrap(err, "failed to delete data-services-fe container")
	}

	// Clear the timestamp to avoid re-deleting the container on next reconcile
	delete(r.container.Status.Timestamps, noDataServicesNeighborKey)
	if err := r.Status().Update(ctx, r.container); err != nil {
		return errors.Wrap(err, "failed to update container status after deleting data-services-fe")
	}

	logger.Info("Data-services-fe container deleted as it has no data-services neighbor")

	return nil
}

// ensureSiblingFEDeleted ensures the sibling data-services-fe container is deleted before
// the data-services container deletion proceeds. This function:
// 1. Finds the sibling FE container on the same node
// 2. Triggers deletion of the FE container
// 3. Waits for the FE container to be fully deleted
func (r *containerReconcilerLoop) ensureSiblingFEDeleted(ctx context.Context) error {
	if !r.container.IsDataServicesContainer() {
		return nil
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureSiblingFEDeleted")
	defer end()

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return nil
	}

	ownerRefs := r.container.GetOwnerReferences()
	if len(ownerRefs) == 0 {
		return errors.New("no owner references found")
	}

	ownerUid := string(ownerRefs[0].UID)

	// Find sibling data-services-fe container on the same node
	feContainers, err := r.KubeService.GetWekaContainersSimple(ctx, r.container.Namespace, string(nodeName), map[string]string{
		domain.WekaLabelClusterId: ownerUid,
		domain.WekaLabelMode:      weka.WekaContainerModeDataServicesFe,
	})
	if err != nil {
		return err
	}

	if len(feContainers) == 0 {
		logger.Debug("No sibling data-services-fe container found on node, proceeding with deletion", "node", nodeName)
		return nil
	}

	feContainer := &feContainers[0]

	// Trigger deletion if not already marked for deletion
	if !feContainer.IsMarkedForDeletion() {
		logger.Info("Triggering deletion of sibling data-services-fe container",
			"fe_container", feContainer.Name,
			"node", nodeName,
		)

		if err := r.Client.Delete(ctx, feContainer); err != nil {
			return fmt.Errorf("failed to delete sibling FE container %s: %w", feContainer.Name, err)
		}

		return lifecycle.NewWaitError(errors.New("waiting for sibling data-services-fe container to be deleted"))
	}

	// FE container is being deleted, wait for it to complete
	logger.Info("Waiting for sibling data-services-fe container deletion to complete",
		"fe_container", feContainer.Name,
		"node", nodeName,
	)
	return lifecycle.NewWaitError(errors.New("waiting for sibling data-services-fe container deletion to complete"))
}

// ensureSiblingFEUpgraded ensures the sibling data-services-fe container is upgraded before
// the data-services container pod is deleted for upgrade. This function:
// 1. Finds the sibling FE container on the same node
// 2. Updates its image to match the target image if needed
// 3. Waits for the FE to be running with the new version
func (r *containerReconcilerLoop) ensureSiblingFEUpgraded(ctx context.Context) error {
	if !r.container.IsDataServicesContainer() {
		return nil
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureSiblingFEUpgraded")
	defer end()

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return nil
	}

	ownerRefs := r.container.GetOwnerReferences()
	if len(ownerRefs) == 0 {
		return errors.New("no owner references found")
	}

	ownerUid := string(ownerRefs[0].UID)
	targetImage := r.container.Spec.Image

	// Find sibling data-services-fe container on the same node
	feContainers, err := r.KubeService.GetWekaContainersSimple(ctx, r.container.Namespace, string(nodeName), map[string]string{
		domain.WekaLabelClusterId: ownerUid,
		domain.WekaLabelMode:      weka.WekaContainerModeDataServicesFe,
	})
	if err != nil {
		return err
	}

	if len(feContainers) == 0 {
		logger.Debug("No sibling data-services-fe container found on node", "node", nodeName)
		return nil
	}

	feContainer := &feContainers[0]

	// Check if FE container needs image update
	if feContainer.Spec.Image != targetImage {
		logger.Info("Updating sibling data-services-fe container image",
			"fe_container", feContainer.Name,
			"current_image", feContainer.Spec.Image,
			"target_image", targetImage,
		)

		patch := map[string]any{
			"spec": map[string]any{
				"image": targetImage,
			},
		}

		patchBytes, err := json.Marshal(patch)
		if err != nil {
			return fmt.Errorf("failed to marshal patch for FE container %s: %w", feContainer.Name, err)
		}

		if err := r.Client.Patch(ctx, feContainer, client.RawPatch(types.MergePatchType, patchBytes)); err != nil {
			return fmt.Errorf("failed to patch FE container %s with new image: %w", feContainer.Name, err)
		}

		return lifecycle.NewWaitError(errors.New("waiting for sibling data-services-fe container to update image"))
	}

	// Check if FE container is running with the new version
	if feContainer.Status.LastAppliedImage != targetImage {
		logger.Info("Waiting for sibling data-services-fe container to be running with new image",
			"fe_container", feContainer.Name,
			"target_image", targetImage,
			"last_applied_image", feContainer.Status.LastAppliedImage,
		)
		return lifecycle.NewWaitError(errors.New("waiting for sibling data-services-fe container to be running with new version"))
	}

	logger.Info("Sibling data-services-fe container is running with target image",
		"fe_container", feContainer.Name,
		"target_image", targetImage,
	)
	return nil
}
