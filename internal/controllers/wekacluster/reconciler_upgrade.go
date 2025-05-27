package wekacluster

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-steps-engine/workers"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	k8sutil "github.com/weka/weka-k8s-api/util"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/controllers/upgrade"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/pkg/util"
)

type UpdatableClusterSpec struct {
	AdditionalMemory          weka.AdditionalMemory
	Tolerations               []string
	RawTolerations            []v1.Toleration
	DriversDistService        string
	ImagePullSecret           string
	Labels                    *util.HashableMap
	NodeSelector              *util.HashableMap
	UpgradeForceReplace       bool
	UpgradeForceReplaceDrives bool
	NetworkSelector           weka.NetworkSelector
	PvcConfig                 *weka.PVCConfig
	TracesConfiguration       *weka.TracesConfiguration
}

func NewUpdatableClusterSpec(spec *weka.WekaClusterSpec, meta *metav1.ObjectMeta) *UpdatableClusterSpec {
	return &UpdatableClusterSpec{
		AdditionalMemory:          spec.AdditionalMemory,
		Tolerations:               spec.Tolerations,
		RawTolerations:            spec.RawTolerations,
		DriversDistService:        spec.DriversDistService,
		ImagePullSecret:           spec.ImagePullSecret,
		Labels:                    util.NewHashableMap(meta.Labels),
		NodeSelector:              util.NewHashableMap(spec.NodeSelector),
		UpgradeForceReplace:       spec.GetOverrides().UpgradeForceReplace,
		UpgradeForceReplaceDrives: spec.GetOverrides().UpgradeForceReplaceDrives,
		NetworkSelector:           spec.NetworkSelector,
		PvcConfig:                 resources.GetPvcConfig(spec.GlobalPVC),
		TracesConfiguration:       spec.TracesConfiguration,
	}
}

type UpgradedCount struct {
	TotalCompute    int
	TotalDrive      int
	TotalS3         int
	UpgradedCompute int
	UpgradedDrive   int
	UpgradedS3      int
}

func (r *wekaClusterReconcilerLoop) GetUpgradedCount(containers []*weka.WekaContainer) (upgradedCount UpgradedCount) {
	for _, container := range containers {
		switch container.Spec.Mode {
		case weka.WekaContainerModeCompute:
			upgradedCount.TotalCompute++
		case weka.WekaContainerModeDrive:
			upgradedCount.TotalDrive++
		case weka.WekaContainerModeS3:
			upgradedCount.TotalS3++
		}

		if container.Status.LastAppliedImage == r.cluster.Spec.Image && container.Status.LastAppliedImage == container.Spec.Image {
			switch container.Spec.Mode {
			case weka.WekaContainerModeCompute:
				upgradedCount.UpgradedCompute++
			case weka.WekaContainerModeDrive:
				upgradedCount.UpgradedDrive++
			case weka.WekaContainerModeS3:
				upgradedCount.UpgradedS3++
			}
		}
	}
	return
}

func (r *wekaClusterReconcilerLoop) HandleSpecUpdates(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleSpecUpdates")
	defer end()

	cluster := r.cluster
	containers := r.containers

	updatableSpec := NewUpdatableClusterSpec(&cluster.Spec, &cluster.ObjectMeta)
	specHash, err := util.HashStruct(updatableSpec)
	if err != nil {
		return errors.Wrap(err, "failed to hash struct")
	}
	// Preserving whole Spec for more generic approach on status, while being able to update only specific fields on containers
	return workers.ProcessConcurrently(ctx, containers, 32, func(ctx context.Context, container *weka.WekaContainer) error {
		if container.Status.LastAppliedSpec == specHash {
			return nil
		}
		err := r.getClient().Get(ctx, client.ObjectKey{Namespace: container.Namespace, Name: container.Name}, container)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}

		logger.Info("Cluster<>Container spec has changed, updating containers")
		patch := client.MergeFrom(container.DeepCopy())

		additionalMemory := updatableSpec.AdditionalMemory.GetForMode(container.Spec.Mode)
		if container.Spec.AdditionalMemory != additionalMemory {
			container.Spec.AdditionalMemory = additionalMemory
		}

		newTolerations := k8sutil.ExpandTolerations([]v1.Toleration{}, updatableSpec.Tolerations, updatableSpec.RawTolerations)
		oldTolerations := k8sutil.NormalizeTolerations(container.Spec.Tolerations)
		if !reflect.DeepEqual(oldTolerations, newTolerations) {
			container.Spec.Tolerations = newTolerations
		}

		if container.Spec.DriversDistService != updatableSpec.DriversDistService {
			container.Spec.DriversDistService = updatableSpec.DriversDistService
		}

		if container.Spec.ImagePullSecret != updatableSpec.ImagePullSecret {
			container.Spec.ImagePullSecret = updatableSpec.ImagePullSecret
		}

		if container.Spec.GetOverrides().UpgradeForceReplace != updatableSpec.UpgradeForceReplace {
			currentOverrides := container.Spec.GetOverrides()
			currentOverrides.UpgradeForceReplace = updatableSpec.UpgradeForceReplace
			container.Spec.Overrides = currentOverrides
		}

		// Propagate PVC config only if the container doesn't have one set yet
		if container.Spec.PVC == nil && updatableSpec.PvcConfig != nil {
			container.Spec.PVC = updatableSpec.PvcConfig
		}

		if container.Spec.TracesConfiguration != updatableSpec.TracesConfiguration {
			container.Spec.TracesConfiguration = updatableSpec.TracesConfiguration
		}

		if container.IsDriveContainer() {
			if updatableSpec.UpgradeForceReplaceDrives { // above check will reset to common flag, so we dont need to put reversal direction here
				if container.Spec.GetOverrides().UpgradeForceReplace != updatableSpec.UpgradeForceReplaceDrives {
					currentOverrides := container.Spec.GetOverrides()
					currentOverrides.UpgradeForceReplace = updatableSpec.UpgradeForceReplaceDrives
					container.Spec.Overrides = currentOverrides
				}
			}
		}

		if container.IsComputeContainer() {
			if updatableSpec.UpgradeForceReplaceDrives {
				// if we are in this mode, we also want NOT to force replace computes, otherwise we would use the common flag
				// and most surely we are with "evict container on pod deletion" mode, so we want to disable it so computes will weka local stop
				if config.Config.EvictContainerOnDeletion {
					if container.Spec.GetOverrides().UpgradePreventEviction != true {
						currentOverrides := container.Spec.GetOverrides()
						currentOverrides.UpgradePreventEviction = true
						container.Spec.Overrides = currentOverrides
					}

				}
			} else {
				if container.Spec.GetOverrides().UpgradePreventEviction != false {
					currentOverrides := container.Spec.GetOverrides()
					currentOverrides.UpgradePreventEviction = false
					container.Spec.Overrides = currentOverrides
				}
			}
		}

		targetNetworkSpec, err := resources.GetContainerNetwork(updatableSpec.NetworkSelector)
		if err != nil {
			return err
		}
		oldNetworkHash, err := util.HashStruct(container.Spec.Network)
		if err != nil {
			return err
		}
		targetNetworkHash, err := util.HashStruct(targetNetworkSpec)
		if err != nil {
			return err
		}
		if oldNetworkHash != targetNetworkHash {
			container.Spec.Network = targetNetworkSpec
		}

		// desired labels = cluster labels + required labels
		// priority-wise, required labels have the highest priority
		requiredLables := factory.RequiredWekaContainerLabels(cluster.UID, cluster.Name, container.Spec.Mode)
		newLabels := util.MergeMaps(cluster.ObjectMeta.GetLabels(), requiredLables)
		if !util.NewHashableMap(newLabels).Equals(util.NewHashableMap(container.Labels)) {
			container.Labels = newLabels
		}

		oldNodeSelector := util.NewHashableMap(container.Spec.NodeSelector)
		role := container.Spec.Mode
		newNodeSelector := map[string]string{}
		if role != weka.WekaContainerModeEnvoy { // envoy sticks to s3, so does not need explicit node selector
			newNodeSelector = cluster.Spec.NodeSelector
			if len(cluster.Spec.RoleNodeSelector.ForRole(role)) != 0 {
				newNodeSelector = cluster.Spec.RoleNodeSelector.ForRole(role)
			}
		}
		if !util.NewHashableMap(newNodeSelector).Equals(oldNodeSelector) {
			container.Spec.NodeSelector = cluster.Spec.NodeSelector
		}

		err = r.getClient().Patch(ctx, container, patch)
		if err != nil {
			return err
		}
		err = r.getClient().Get(ctx, client.ObjectKey{Namespace: container.Namespace, Name: container.Name}, container)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		container.Status.LastAppliedSpec = specHash
		return r.getClient().Status().Patch(ctx, container, patch)
	}).AsError()
}

func (r *wekaClusterReconcilerLoop) emitClusterUpgradeCustomEvent(ctx context.Context) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "emitClusterUpgradeCustomEvent")
	defer end()

	activeContainer := discovery.SelectActiveContainer(r.containers)
	if activeContainer == nil {
		logger.Debug("Active container not found, skipping Weka cluster event emit")
		return
	}

	count := r.GetUpgradedCount(r.containers)
	key := fmt.Sprintf("upgrade-%s-%d/%d/%d", r.cluster.Spec.Image, count.UpgradedCompute, count.UpgradedDrive, count.UpgradedS3)
	if !r.Throttler.ShouldRun(key, &throttling.ThrottlingSettings{
		DisableRandomPreSetInterval: true,
		Interval:                    10 * time.Minute,
	}) {
		return
	}

	msg := "Upgrading cluster progress: drive:%d:%d compute:%d:%d"
	msg = fmt.Sprintf(msg, count.UpgradedDrive, count.TotalDrive, count.UpgradedCompute, count.TotalCompute)
	logger.SetValues("image", r.cluster.Spec.Image, "compute", count.UpgradedCompute, "drive", count.UpgradedDrive)

	if count.TotalS3 > 0 {
		msg += fmt.Sprintf(" s3: %d:%d", count.UpgradedS3, count.TotalS3)
		logger.SetValues("s3", count.UpgradedS3)
	}

	execService := r.ExecService
	wekaService := services.NewWekaService(execService, activeContainer)
	err := wekaService.EmitCustomEvent(ctx, msg)
	if err != nil {
		logger.Warn("Failed to emit custom event", "event", msg)
	}
}

func (r *wekaClusterReconcilerLoop) handleUpgrade(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster
	clusterService := r.clusterService
	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		return errors.New("Failed to get template")
	}

	if cluster.Spec.Image != cluster.Status.LastAppliedImage {
		logger.Info("Image upgrade sequence")
		targetVersion := utils.GetSoftwareVersion(cluster.Spec.Image)

		if cluster.Spec.GetOverrides().UpgradePaused {
			return lifecycle.NewWaitError(errors.New("Upgrade is paused"))
		}

		if cluster.Spec.GetOverrides().UpgradeAllAtOnce {
			// containers will self-upgrade
			return workers.ProcessConcurrently(ctx, r.containers, 32, func(ctx context.Context, container *weka.WekaContainer) error {
				if container.Spec.Image != cluster.Spec.Image {
					patch := map[string]interface{}{
						"spec": map[string]interface{}{
							"image": cluster.Spec.Image,
						},
					}

					patchBytes, err := json.Marshal(patch)
					if err != nil {
						err = fmt.Errorf("failed to marshal patch for container %s: %w", container.Name, err)
						return err
					}

					return errors.Wrap(
						r.getClient().Patch(ctx, container, client.RawPatch(types.MergePatchType, patchBytes)),
						fmt.Sprintf("failed to update container image %s: %v", container.Name, err),
					)
				}
				return nil
			}).AsError()
		}

		driveContainers, err := clusterService.GetOwnedContainers(ctx, weka.WekaContainerModeDrive)
		if err != nil {
			return err
		}
		// before upgrade, if if all drive nodes are still in old version - invoke upgrade prepare commands
		prepareForUpgrade := true
		for _, container := range driveContainers {
			//i.e if any container already on new target version - we should not prepare for drive phase
			if container.Status.LastAppliedImage == cluster.Spec.Image && container.Status.ClusterContainerID != nil {
				prepareForUpgrade = false
			}
		}
		if prepareForUpgrade {
			err := r.prepareForUpgradeDrives(ctx, driveContainers, targetVersion)
			if err != nil {
				return err
			}
		}

		execInContainer := discovery.SelectActiveContainer(r.containers)
		if execInContainer == nil {
			return errors.New("No active container found")
		}

		wekaService := services.NewWekaService(r.ExecService, execInContainer)
		status, err := wekaService.GetWekaStatus(ctx)
		if err != nil {
			return err
		}

		if !status.Rebuild.IsFullyProtected() {
			_ = r.RecordEvent("", "WaitingForStabilize", "Weka is not fully protected, waiting to stabilize")
			return lifecycle.NewWaitError(errors.Errorf("Weka is not fully protected, waiting to stabilize, %v", status.Rebuild))
		}

		if !slices.Contains([]string{
			"OK",
			"REDISTRIBUTING",
		}, status.Status) {
			return lifecycle.NewWaitError(errors.New("Weka status is not OK/REDISTRIBUTING, waiting to stabilize. status:" + status.Status))
		}

		activeDrivesThreshold := float64(template.DriveContainers) * (float64(config.Config.Upgrade.DriveThresholdPercent) / 100)
		activeComputesThreshold := float64(template.ComputeContainers) * (float64(config.Config.Upgrade.ComputeThresholdPercent) / 100)

		if float64(status.Containers.Drives.Active) < activeDrivesThreshold {
			msg := fmt.Sprintf("Not enough drives containers are active, waiting to stabilize, %d/%d", status.Containers.Drives.Active, template.DriveContainers)
			_ = r.RecordEvent("", "ClusterSizeThreshold", msg)
			return lifecycle.NewWaitError(errors.New(msg))
		}

		if float64(status.Containers.Computes.Active) < activeComputesThreshold {
			msg := fmt.Sprintf("Not enough computes containers are active, waiting to stabilize, %d/%d", status.Containers.Computes.Active, template.ComputeContainers)
			_ = r.RecordEvent("", "ClusterSizeThreshold", msg)
			return lifecycle.NewWaitError(errors.New(msg))
		}

		r.emitClusterUpgradeCustomEvent(ctx)

		uController := upgrade.NewUpgradeController(r.getClient(), driveContainers, cluster.Spec.Image)
		err = uController.RollingUpgrade(ctx)
		if err != nil {
			return err
		}

		computeContainers, err := clusterService.GetOwnedContainers(ctx, weka.WekaContainerModeCompute)
		if err != nil {
			return err
		}

		prepareForUpgrade = true
		if r.cluster.Spec.GetOverrides().UpgradePausePreCompute {
			return lifecycle.NewWaitError(errors.New("Upgrade paused before compute phase"))
		}
		for _, container := range computeContainers {
			if container.Status.LastAppliedImage == cluster.Spec.Image && container.Status.ClusterContainerID != nil {
				prepareForUpgrade = false
			}
		}
		//
		if prepareForUpgrade {
			err := r.prepareForUpgradeCompute(ctx, computeContainers, targetVersion)
			if err != nil {
				return err
			}
		}

		uController = upgrade.NewUpgradeController(r.getClient(), computeContainers, cluster.Spec.Image)
		err = uController.RollingUpgrade(ctx)
		if err != nil {
			return err
		}

		s3Containers, err := clusterService.GetOwnedContainers(ctx, weka.WekaContainerModeS3)
		if err != nil {
			return err
		}
		prepareForUpgrade = true
		// if any s3 container changed version - do not prepare for s3
		for _, container := range s3Containers {
			if container.Status.LastAppliedImage == cluster.Spec.Image && container.Status.ClusterContainerID != nil {
				prepareForUpgrade = false
			}
		}
		if prepareForUpgrade {
			err := r.prepareForUpgradeS3(ctx, s3Containers, targetVersion)
			if err != nil {
				return err
			}
		}

		uController = upgrade.NewUpgradeController(r.getClient(), s3Containers, cluster.Spec.Image)
		err = uController.RollingUpgrade(ctx)
		if err != nil {
			return err
		}

		err = r.finalizeUpgrade(ctx, driveContainers)
		if err != nil {
			return err
		}

		cluster.Status.LastAppliedImage = cluster.Spec.Image
		if err := r.getClient().Status().Update(ctx, cluster); err != nil {
			return err
		}
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) prepareForUpgradeDrives(ctx context.Context, containers []*weka.WekaContainer, targetVersion string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "prepareForUpgradeDrives")
	defer end()

	executor, err := r.ExecService.GetExecutor(ctx, containers[0])
	if err != nil {
		logger.Error(err, "Failed to create executor")
		return nil
	}

	cmd := `
wekaauthcli status --json | grep upgrade_phase | grep -i drive || wekaauthcli debug jrpc prepare_leader_for_upgrade
wekaauthcli status --json | grep upgrade_phase | grep -i drive ||  wekaauthcli debug jrpc upgrade_phase_start target_phase_type=DrivePhase target_version_name=` + targetVersion + `
`

	_, stderr, err := executor.ExecNamed(ctx, "PrepareForUpgradeDrives", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to prepare for upgrade: %s", stderr.String())
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) prepareForUpgradeCompute(ctx context.Context, containers []*weka.WekaContainer, targetVersion string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "prepareForUpgradeCompute")
	defer end()

	executor, err := r.ExecService.GetExecutor(ctx, containers[0])
	if err != nil {
		logger.Error(err, "Failed to create executor")
		return nil
	}

	cmd := `
wekaauthcli status --json | grep upgrade_phase | grep -i compute || wekaauthcli debug jrpc upgrade_phase_finish
wekaauthcli status --json | grep upgrade_phase | grep -i compute || wekaauthcli debug jrpc upgrade_phase_start target_phase_type=ComputeRollingPhase target_version_name=` + targetVersion + `
`

	_, stderr, err := executor.ExecNamed(ctx, "PrepareForUpgradeCompute", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to prepare for upgrade: %s", stderr.String())
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) prepareForUpgradeS3(ctx context.Context, containers []*weka.WekaContainer, targetVersion string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "prepareForUpgradeS3")
	defer end()

	if len(containers) == 0 {
		logger.Info("No S3 containers found to ugprade")
		return nil
	}

	executor, err := r.ExecService.GetExecutor(ctx, containers[0])
	if err != nil {
		logger.Error(err, "Failed to create executor")
		return nil
	}

	cmd := `
wekaauthcli status --json | grep upgrade_phase | grep -i frontend || wekaauthcli debug jrpc upgrade_phase_finish
wekaauthcli status --json | grep upgrade_phase | grep -i frontend || wekaauthcli debug jrpc upgrade_phase_start target_phase_type=FrontendPhase target_version_name=` + targetVersion + `
`
	_, stderr, err := executor.ExecNamed(ctx, "PrepareForUpgradeS3", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to prepare for upgrade: %s", stderr.String())
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) finalizeUpgrade(ctx context.Context, containers []*weka.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "finalizeUpgrade")
	defer end()

	executor, err := r.ExecService.GetExecutor(ctx, containers[0])
	if err != nil {
		logger.Error(err, "Failed to create executor")
		return nil
	}

	cmd := `
wekaauthcli debug jrpc upgrade_phase_finish
wekaauthcli debug jrpc unprepare_leader_for_upgrade
`
	stdout, stderr, err := executor.ExecNamed(ctx, "FinalizeUpgrade", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to finalize upgrade: STDERR: %s \n STDOUT:%s ", stderr.String(), stdout.String())
	}
	return nil
}
