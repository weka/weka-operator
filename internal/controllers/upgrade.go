package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/weka/weka-operator/internal/config"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type UpgradeController struct {
	Containers  []*v1alpha1.WekaContainer
	TargetImage string
	Client      client.Client
}

func NewUpgradeController(client client.Client, containers []*v1alpha1.WekaContainer, targetImage string) *UpgradeController {
	return &UpgradeController{
		Containers:  containers,
		TargetImage: targetImage,
		Client:      client,
	}
}

func (u *UpgradeController) UpdateContainer(ctx context.Context, container *v1alpha1.WekaContainer) error {
	if container.Status.LastAppliedImage != u.TargetImage {
		if container.Spec.Image != u.TargetImage {
			patch := map[string]interface{}{
				"spec": map[string]interface{}{
					"image": u.TargetImage,
				},
			}

			patchBytes, err := json.Marshal(patch)
			if err != nil {
				err = fmt.Errorf("failed to marshal patch for %s: %w", container.Name, err)
				return err
			}

			if err := u.Client.Patch(ctx, container, client.RawPatch(types.MergePatchType, patchBytes)); err != nil {
				err = fmt.Errorf("failed to patch container %s with new image %s: %w", container.Name, u.TargetImage, err)
				return err
			}
		}
	}
	return nil
}

func (u *UpgradeController) AreUpgraded() bool {
	for _, container := range u.Containers {
		if container.Status.LastAppliedImage == "" && container.Status.ClusterContainerID == nil && container.Spec.Image == u.TargetImage {
			continue // if pod is not schedulable, ignore it from "Upgrading" status calc
		}

		if container.Status.LastAppliedImage != u.TargetImage {
			return false
		}
	}
	return true
}

func (u *UpgradeController) AllAtOnceUpgrade(ctx context.Context) error {
	for _, container := range u.Containers {
		if err := u.UpdateContainer(ctx, container); err != nil {
			return err
		}
	}
	if !u.AreUpgraded() {
		return lifecycle.NewExpectedError(errors.New("container upgrade not finished yet"))
	}
	return nil
}

// Upgrades one container at a time
func (u *UpgradeController) RollingUpgrade(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "RollingUpgrade")

	maxSkipPercent := config.Config.Upgrade.MaxDeactivatingContainersPercent
	skipped := 0

	defer end()
	for _, container := range u.Containers {
		if container.IsMarkedForDeletion() {
			skipped += 1
			if skipped > (len(u.Containers)*maxSkipPercent)/100 {
				logger.Info("too many containers marked for deletion, aborting", "container", container.Name)
				return lifecycle.NewWaitError(errors.New("too many containers marked for deletion"))
			}
			logger.Info("container marked for deletion, skipping", "container", container.Name)
			continue
		}
		if container.Spec.Image == u.TargetImage && container.Status.LastAppliedImage != container.Spec.Image {
			if container.GetNodeAffinity() == "" {
				logger.Debug("container does not have node affinity, skipping", "container", container.Name)
				continue
			}
			logger.Info("container upgrade did not finish yet", "container_name", container.Name)
			return lifecycle.NewWaitError(errors.New("container upgrade not finished yet"))
		}
	}

	for _, container := range u.Containers {
		if container.Spec.Image != u.TargetImage {
			err := u.UpdateContainer(ctx, container)
			if err != nil {
				return err
			}
			return lifecycle.NewWaitError(errors.New(fmt.Sprintf("starting upgrade of container %s", container.Name)))
		}
	}
	return nil
}
