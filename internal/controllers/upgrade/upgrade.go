package upgrade

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type UpgradeController struct {
	Containers        []*v1alpha1.WekaContainer
	TargetImage       string // non-empty only when the image itself changed
	TargetSpecVersion string
	Client            client.Client
}

func NewUpgradeController(client client.Client, containers []*v1alpha1.WekaContainer, targetImage, targetSpecVersion string) *UpgradeController {
	return &UpgradeController{
		Containers:        containers,
		TargetImage:       targetImage,
		TargetSpecVersion: targetSpecVersion,
		Client:            client,
	}
}

// isContainerAligned returns true if the container already has the target spec applied.
func (u *UpgradeController) isContainerAligned(container *v1alpha1.WekaContainer) bool {
	if u.TargetSpecVersion != "" {
		return container.Spec.SpecVersion == u.TargetSpecVersion
	}
	return container.Spec.Image == u.TargetImage
}

// isContainerApplied returns true if the container's pod has successfully applied the target spec.
func (u *UpgradeController) isContainerApplied(container *v1alpha1.WekaContainer) bool {
	if u.TargetSpecVersion != "" {
		return container.Status.LastAppliedSpecVersion == u.TargetSpecVersion
	}
	return container.Status.LastAppliedImage == u.TargetImage
}

func (u *UpgradeController) UpdateContainer(ctx context.Context, container *v1alpha1.WekaContainer) error {
	if u.isContainerAligned(container) {
		return nil // already patched
	}

	specPatch := map[string]interface{}{}
	if u.TargetSpecVersion != "" {
		specPatch["specVersion"] = u.TargetSpecVersion
	}
	if u.TargetImage != "" && container.Spec.Image != u.TargetImage {
		specPatch["image"] = u.TargetImage
	}
	if len(specPatch) == 0 {
		return nil
	}
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
