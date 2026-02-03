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

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

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
