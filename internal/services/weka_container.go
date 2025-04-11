package services

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func SetContainerStateDeleting(ctx context.Context, container *weka.WekaContainer, client client.Client) error {
	return UpdateContainerState(ctx, container, client, weka.ContainerStateDeleting, "SetContainerStateDeleting")
}

func SetContainerStateDestroying(ctx context.Context, container *weka.WekaContainer, client client.Client) error {
	return UpdateContainerState(ctx, container, client, weka.ContainerStateDestroying, "SetContainerStateDestroying")
}

func UpdateContainerState(ctx context.Context, container *weka.WekaContainer, c client.Client, state weka.ContainerState, spanName string) error {
	if container.Spec.State == state {
		return nil
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, spanName)
	defer end()

	logger.Info("Updating container state", "container", container.Name, "state", state)

	patchData := map[string]interface{}{
		"metadata": map[string]interface{}{
			"resourceVersion": container.ResourceVersion,
		},
		"spec": map[string]interface{}{
			"state": state,
		},
	}
	patchBytes, err := json.Marshal(patchData)
	if err != nil {
		return fmt.Errorf("failed to marshal patch data: %w", err)
	}
	err = c.Patch(ctx, container, client.RawPatch(types.MergePatchType, patchBytes))
	if err != nil {
		logger.SetError(err, "Failed to patch container state")
		return fmt.Errorf("failed to patch resource: %w", err)
	}

	return nil
}

func FilterContainersForDeletion(containers []*weka.WekaContainer, shouldDelete func(container *weka.WekaContainer) bool) []*weka.WekaContainer {
	var toDelete []*weka.WekaContainer
	for _, container := range containers {
		if container.IsMarkedForDeletion() || container.IsDeletingState() || container.IsDestroyingState() {
			continue
		}

		if shouldDelete(container) {
			toDelete = append(toDelete, container)
		}
	}
	return toDelete
}
