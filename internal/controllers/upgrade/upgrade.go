package upgrade

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
)

type UpgradeController struct {
	Containers  []*v1alpha1.WekaContainer
	TargetImage string
	Client      client.Client
}

func NewUpgradeController(k8sClient client.Client, containers []*v1alpha1.WekaContainer, targetImage string) *UpgradeController {
	return &UpgradeController{
		Containers:  containers,
		TargetImage: targetImage,
		Client:      k8sClient,
	}
}

func (u *UpgradeController) isContainerAligned(container *v1alpha1.WekaContainer) bool {
	return container.Spec.Image == u.TargetImage
}

func (u *UpgradeController) isContainerApplied(container *v1alpha1.WekaContainer) bool {
	return container.Status.LastAppliedImage == u.TargetImage
}

func (u *UpgradeController) UpdateContainer(ctx context.Context, container *v1alpha1.WekaContainer) error {
	if u.isContainerAligned(container) {
		return nil // already patched
	}

	specPatch := map[string]interface{}{"image": u.TargetImage}
	patch := map[string]interface{}{
		"spec": specPatch,
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch for %s: %w", container.Name, err)
	}

	if err := u.Client.Patch(ctx, container, client.RawPatch(types.MergePatchType, patchBytes)); err != nil {
		return fmt.Errorf("failed to patch container %s: %w", container.Name, err)
	}
	return nil
}

func (u *UpgradeController) AreUpgraded() bool {
	for _, container := range u.Containers {
		if !u.isContainerApplied(container) && container.Status.ClusterContainerID == nil && u.isContainerAligned(container) {
			continue // if pod is not schedulable, ignore it from "Upgrading" status calc
		}

		if !u.isContainerApplied(container) {
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
	ctx, logger := instrumentation.CreateLogSpan(ctx, "RollingUpgrade")

	maxSkipPercent := config.Config.Upgrade.MaxDeactivatingContainersPercent
	skipped := 0

	defer logger.End()
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
		if u.isContainerAligned(container) && !u.isContainerApplied(container) {
			if container.GetNodeAffinity() == "" {
				logger.Debug("container does not have node affinity, skipping", "container", container.Name)
				continue
			}
			logger.Info("container upgrade did not finish yet", "container_name", container.Name)
			return lifecycle.NewWaitError(errors.New("container upgrade not finished yet"))
		}
	}

	for _, container := range u.Containers {
		if !u.isContainerAligned(container) {
			err := u.UpdateContainer(ctx, container)
			if err != nil {
				return err
			}
			return lifecycle.NewWaitError(errors.New(fmt.Sprintf("starting upgrade of container %s", container.Name)))
		}
	}
	return nil
}
