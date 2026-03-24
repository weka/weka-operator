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
	Containers       []*v1alpha1.WekaContainer
	TargetImage      string
	TargetConfigHash string
	Client           client.Client
	SpecPropagator   func(ctx context.Context, container *v1alpha1.WekaContainer) error // optional, for spec propagation
}

func NewUpgradeController(client client.Client, containers []*v1alpha1.WekaContainer, targetImage string, targetConfigHash string) *UpgradeController {
	return &UpgradeController{
		Containers:       containers,
		TargetImage:      targetImage,
		TargetConfigHash: targetConfigHash,
		Client:           client,
	}
}

func (u *UpgradeController) isConfigApplied(container *v1alpha1.WekaContainer) bool {
	if u.TargetConfigHash == "" {
		return container.Status.LastAppliedImage == u.TargetImage
	}
	return container.Status.LastAppliedSpec == u.TargetConfigHash
}

// isSpecAligned returns true if the container's spec already targets the upgrade goal.
// For cluster containers this checks TargetClusterSpecHash; for clients (no config hash) it checks image.
func (u *UpgradeController) isSpecAligned(container *v1alpha1.WekaContainer) bool {
	if u.TargetConfigHash == "" {
		return container.Spec.Image == u.TargetImage
	}
	return container.Spec.TargetClusterSpecHash == u.TargetConfigHash
}

func (u *UpgradeController) UpdateContainer(ctx context.Context, container *v1alpha1.WekaContainer) error {
	if !u.isConfigApplied(container) {
		if !u.isSpecAligned(container) {
			// If we have a spec propagator, use it (it handles image + hash + spec fields)
			if u.SpecPropagator != nil {
				return u.SpecPropagator(ctx, container)
			}
			// Fallback: just patch image and hash (for clients without spec propagation)
			patch := map[string]interface{}{
				"spec": map[string]interface{}{
					"image":                 u.TargetImage,
					"targetClusterSpecHash": u.TargetConfigHash,
				},
			}

			patchBytes, err := json.Marshal(patch)
			if err != nil {
				err = fmt.Errorf("failed to marshal patch for %s: %w", container.Name, err)
				return err
			}

			if err := u.Client.Patch(ctx, container, client.RawPatch(types.MergePatchType, patchBytes)); err != nil {
				err = fmt.Errorf("failed to patch container %s: %w", container.Name, err)
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

		if !u.isConfigApplied(container) {
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
		isNewContainer := container.Status.LastAppliedImage == ""
		isTargetAligned := u.isSpecAligned(container)
		isApplied := u.isConfigApplied(container)

		if isNewContainer && isTargetAligned {
			logger.Info("container is a new container and does not need upgrade", "container_name", container.Name)
			continue
		}
		if isTargetAligned && !isApplied {
			if container.GetNodeAffinity() == "" {
				logger.Debug("container does not have node affinity, skipping", "container", container.Name)
				continue
			}
			logger.Info("container upgrade did not finish yet", "container_name", container.Name)
			return lifecycle.NewWaitError(errors.New("container upgrade not finished yet"))
		}
	}

	for _, container := range u.Containers {
		if !u.isSpecAligned(container) {
			err := u.UpdateContainer(ctx, container)
			if err != nil {
				return err
			}
			return lifecycle.NewWaitError(errors.New(fmt.Sprintf("starting upgrade of container %s", container.Name)))
		}
	}
	return nil
}
