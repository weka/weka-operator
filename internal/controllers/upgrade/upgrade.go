package upgrade

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type UpgradeController struct {
	Containers       []*v1alpha1.WekaContainer
	TargetImage      string
	TargetConfigHash string
	PatchFunc        func(container *v1alpha1.WekaContainer) // applies tracked spec fields per container
	Client           client.Client
}

func NewUpgradeController(client client.Client, containers []*v1alpha1.WekaContainer, targetImage string, targetConfigHash string, patchFunc func(container *v1alpha1.WekaContainer)) *UpgradeController {
	return &UpgradeController{
		Containers:       containers,
		TargetImage:      targetImage,
		TargetConfigHash: targetConfigHash,
		PatchFunc:        patchFunc,
		Client:           client,
	}
}

func (u *UpgradeController) UpdateContainer(ctx context.Context, container *v1alpha1.WekaContainer) error {
	patch := client.MergeFrom(container.DeepCopy())
	container.Spec.Image = u.TargetImage
	if u.PatchFunc != nil {
		u.PatchFunc(container)
	}
	if err := u.Client.Patch(ctx, container, patch); err != nil {
		return fmt.Errorf("failed to patch container %s with new image %s: %w", container.Name, u.TargetImage, err)
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
		if u.TargetConfigHash != "" && container.Status.LastAppliedSpec != u.TargetConfigHash {
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
		if container.Spec.Image == u.TargetImage && container.Status.LastAppliedImage == "" {
			logger.Info("container is a new container and does not need upgrade", "container_name", container.Name)
			continue
		}
		if container.Spec.Image == u.TargetImage &&
			(container.Status.LastAppliedImage != container.Spec.Image ||
				(u.TargetConfigHash != "" && container.Status.LastAppliedSpec != u.TargetConfigHash)) {
			if container.GetNodeAffinity() == "" {
				logger.Debug("container does not have node affinity, skipping", "container", container.Name)
				continue
			}
			logger.Info("container upgrade did not finish yet", "container_name", container.Name)
			return lifecycle.NewWaitError(errors.New("container upgrade not finished yet"))
		}
	}

	for _, container := range u.Containers {
		needsUpdate := container.Spec.Image != u.TargetImage
		if u.TargetConfigHash != "" && container.Status.LastAppliedSpec != u.TargetConfigHash {
			needsUpdate = true
		}
		if needsUpdate {
			err := u.UpdateContainer(ctx, container)
			if err != nil {
				return err
			}
			return lifecycle.NewWaitError(errors.New(fmt.Sprintf("starting upgrade of container %s", container.Name)))
		}
	}
	return nil
}
