package wekacluster

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-lib/pkg/workers"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	k8sutil "github.com/weka/weka-k8s-api/util"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	AdditionalMemory            weka.AdditionalMemory
	Tolerations                 []string
	RawTolerations              []v1.Toleration
	DriversDistService          string
	ImagePullSecret             string
	Labels                      *util.HashableMap
	Annotations                 *util.HashableMap
	NodeSelector                *util.HashableMap
	S3NodeSelector              *util.HashableMap
	NfsNodeSelector             *util.HashableMap
	ComputeNodeSelector         *util.HashableMap
	DriveNodeSelector           *util.HashableMap
	DataServicesNodeSelector    *util.HashableMap
	S3Annotations               *util.HashableMap
	NfsAnnotations              *util.HashableMap
	ComputeAnnotations          *util.HashableMap
	DriveAnnotations            *util.HashableMap
	DataServicesAnnotations     *util.HashableMap
	UpgradeForceReplace         bool
	UpgradeForceReplaceDrives   bool
	Network                     weka.Network
	RoleNetworkSelector         weka.RoleNetworkSelector
	PvcConfig                   *weka.PVCConfig
	TracesConfiguration         *weka.TracesConfiguration
	RoleCoreIds                 weka.RoleCoreIds
	CpuPolicy                   weka.CpuPolicy
	ComputeExtraCores           int
	DriveExtraCores             int
	S3ExtraCores                int
	NfsExtraCores               int
	DataServicesExtraCores      int
	DriversLoaderImage          string
	DriversBuildId              *string
	ComputeHugepages            int
	ComputeHugepagesOffset      int
	DriveHugepages              int
	DriveHugepagesOffset        int
	S3Hugepages                 int
	S3HugepagesOffset           int
	NfsHugepages                int
	NfsHugepagesOffset          int
	DataServicesHugepages       int
	DataServicesHugepagesOffset int
}

func NewUpdatableClusterSpec(ctx context.Context, spec *weka.WekaClusterSpec, meta *metav1.ObjectMeta, containers []*weka.WekaContainer) *UpdatableClusterSpec {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "NewUpdatableClusterSpec")
	defer end()

	// Helper function to safely convert pointer-to-map to HashableMap
	safeHashableMap := func(ptr *map[string]string) *util.HashableMap {
		if ptr == nil {
			return nil
		}
		return util.NewHashableMap(*ptr)
	}

	// Get extra cores values from dynamic config if available
	computeExtraCores := 0
	driveExtraCores := 0
	s3ExtraCores := 0
	nfsExtraCores := 0
	dataServicesExtraCores := 0
	if spec.Dynamic != nil {
		computeExtraCores = spec.Dynamic.ComputeExtraCores
		driveExtraCores = spec.Dynamic.DriveExtraCores
		s3ExtraCores = spec.Dynamic.S3ExtraCores
		nfsExtraCores = spec.Dynamic.NfsExtraCores
		dataServicesExtraCores = spec.Dynamic.DataServicesExtraCores
	}

	// Compute deterministic hugepages from template
	tmpl := allocator.GetWekaClusterTemplate(spec.Dynamic)
	dynamicTemplate := spec.Dynamic
	if dynamicTemplate == nil {
		dynamicTemplate = &weka.WekaClusterTemplate{}
	}

	// Compute desired compute/drive hugepages (0 = skip propagation).
	// User-set values are always propagated; auto-calculated values only when
	// hugepagesUpdate.compute/drive is enabled in config.
	// Offsets are only set when the corresponding hugepages value is non-zero.
	var computeHp, computeHpOffset int
	var driveHp, driveHpOffset int

	if dynamicTemplate.ComputeHugepages > 0 {
		// User override
		computeHp = dynamicTemplate.ComputeHugepages

		maxMiB := config.Config.ComputeMaxHugepagesMiB
		if maxMiB > 0 && computeHp > maxMiB {
			computeHp = maxMiB
		}
	} else if config.Config.HugepagesUpdate.Compute {
		// Auto-calculate from capacity
		totalRawCapacityGiB, err := getClusterTotalCapacityGiB(containers, tmpl)
		if err != nil {
			logger.Info("Cannot compute capacity for hugepages, skipping compute hugepages propagation", "error", err)
		} else if totalRawCapacityGiB > 0 {
			computeHp = allocator.ComputeCapacityBasedHugepages(
				ctx, totalRawCapacityGiB, tmpl.Containers.Compute, tmpl.Cores.Compute, tmpl.DriveTypesRatio)
		}
	}
	if computeHp > 0 {
		computeHpOffset = util.GetNonZeroOrDefault(dynamicTemplate.ComputeHugepagesOffset, 200)
	}

	if dynamicTemplate.DriveHugepages > 0 {
		// User override
		driveHp = dynamicTemplate.DriveHugepages
	} else if config.Config.HugepagesUpdate.Drive {
		// Auto-calculate
		driveHp = allocator.CalculateDriveHugepages(tmpl)
	}
	if driveHp > 0 {
		driveHpOffset = util.GetNonZeroOrDefault(dynamicTemplate.DriveHugepagesOffset, allocator.CalculateDriveHugepagesOffset(tmpl))
	}

	return &UpdatableClusterSpec{
		AdditionalMemory:            spec.AdditionalMemory,
		Tolerations:                 spec.Tolerations,
		RawTolerations:              spec.RawTolerations,
		DriversDistService:          spec.DriversDistService,
		ImagePullSecret:             spec.ImagePullSecret,
		Labels:                      util.NewHashableMap(meta.Labels),
		Annotations:                 util.NewHashableMap(util.RemoveKeysStartingWithPrefix(meta.Annotations, "weka.io/prepull-")),
		NodeSelector:                util.NewHashableMap(spec.NodeSelector),
		S3NodeSelector:              safeHashableMap(spec.RoleNodeSelector.S3),
		NfsNodeSelector:             safeHashableMap(spec.RoleNodeSelector.Nfs),
		ComputeNodeSelector:         safeHashableMap(spec.RoleNodeSelector.Compute),
		DriveNodeSelector:           safeHashableMap(spec.RoleNodeSelector.Drive),
		DataServicesNodeSelector:    safeHashableMap(spec.RoleNodeSelector.DataServices),
		S3Annotations:               safeHashableMap(spec.RoleAnnotations.S3),
		NfsAnnotations:              safeHashableMap(spec.RoleAnnotations.Nfs),
		ComputeAnnotations:          safeHashableMap(spec.RoleAnnotations.Compute),
		DriveAnnotations:            safeHashableMap(spec.RoleAnnotations.Drive),
		DataServicesAnnotations:     safeHashableMap(spec.RoleAnnotations.DataServices),
		UpgradeForceReplace:         spec.GetOverrides().UpgradeForceReplace,
		UpgradeForceReplaceDrives:   spec.GetOverrides().UpgradeForceReplaceDrives,
		Network:                     spec.Network,
		RoleNetworkSelector:         spec.RoleNetworkSelector,
		PvcConfig:                   resources.GetPvcConfig(spec.GlobalPVC),
		TracesConfiguration:         spec.TracesConfiguration,
		RoleCoreIds:                 spec.RoleCoreIds,
		CpuPolicy:                   spec.CpuPolicy,
		ComputeExtraCores:           computeExtraCores,
		DriveExtraCores:             driveExtraCores,
		S3ExtraCores:                s3ExtraCores,
		NfsExtraCores:               nfsExtraCores,
		DataServicesExtraCores:      dataServicesExtraCores,
		DriversLoaderImage:          spec.GetOverrides().DriversLoaderImage,
		DriversBuildId:              spec.GetOverrides().DriversBuildId,
		ComputeHugepages:            computeHp,
		ComputeHugepagesOffset:      computeHpOffset,
		DriveHugepages:              driveHp,
		DriveHugepagesOffset:        driveHpOffset,
		S3Hugepages:                 util.GetNonZeroOrDefault(dynamicTemplate.S3FrontendHugepages, 1400*tmpl.Cores.S3),
		S3HugepagesOffset:           util.GetNonZeroOrDefault(dynamicTemplate.S3FrontendHugepagesOffset, 200),
		NfsHugepages:                util.GetNonZeroOrDefault(dynamicTemplate.NfsFrontendHugepages, 1400*tmpl.Cores.Nfs),
		NfsHugepagesOffset:          util.GetNonZeroOrDefault(dynamicTemplate.NfsFrontendHugepagesOffset, 200),
		DataServicesHugepages:       util.GetNonZeroOrDefault(dynamicTemplate.DataServicesHugepages, 1536),
		DataServicesHugepagesOffset: util.GetNonZeroOrDefault(dynamicTemplate.DataServicesHugepagesOffset, 200),
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

// PropagateSpecToContainer updates a single container's spec fields from the cluster's updatable spec,
// including setting the target image and cluster config hash. This is used by the upgrade controller
// ApplyUpdatableSpecToContainer applies all updatable spec fields from the cluster to a container in memory.
// No K8s API calls — just modifies the container object. Used for both initial creation and rolling updates.
func ApplyUpdatableSpecToContainer(container *weka.WekaContainer, updatableSpec *UpdatableClusterSpec, cluster *weka.WekaCluster) {
	role := container.Spec.Mode

	additionalMemory := updatableSpec.AdditionalMemory.GetForMode(role)
	if container.Spec.AdditionalMemory != additionalMemory {
		container.Spec.AdditionalMemory = additionalMemory
	}

	newTolerations := k8sutil.ExpandTolerations([]v1.Toleration{}, updatableSpec.Tolerations, updatableSpec.RawTolerations)
	container.Spec.Tolerations = newTolerations

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
		if updatableSpec.UpgradeForceReplaceDrives {
			if container.Spec.GetOverrides().UpgradeForceReplace != updatableSpec.UpgradeForceReplaceDrives {
				currentOverrides := container.Spec.GetOverrides()
				currentOverrides.UpgradeForceReplace = updatableSpec.UpgradeForceReplaceDrives
				container.Spec.Overrides = currentOverrides
			}
		}
	}

	if container.IsComputeContainer() {
		if updatableSpec.UpgradeForceReplaceDrives {
			if config.Config.EvictContainerOnDeletion {
				if !container.Spec.GetOverrides().UpgradePreventEviction {
					currentOverrides := container.Spec.GetOverrides()
					currentOverrides.UpgradePreventEviction = true
					container.Spec.Overrides = currentOverrides
				}
			}
		} else {
			if container.Spec.GetOverrides().UpgradePreventEviction {
				currentOverrides := container.Spec.GetOverrides()
				currentOverrides.UpgradePreventEviction = false
				container.Spec.Overrides = currentOverrides
			}
		}
	}

	targetNetwork := cluster.GetNetworkForRole(role)
	container.Spec.Network = targetNetwork

	// desired labels = cluster labels + required labels
	requiredLables := factory.RequiredWekaContainerLabels(cluster.UID, cluster.Name, role)
	newLabels := util.MergeMaps(cluster.ObjectMeta.GetLabels(), requiredLables)
	container.Labels = newLabels

	newAnnotations := cluster.GetAnnotationsForRole(role)
	container.Annotations = newAnnotations

	if role != weka.WekaContainerModeEnvoy {
		newNodeSelector := cluster.GetNodeSelectorForRole(role)
		container.Spec.NodeSelector = newNodeSelector
	}

	roleCoreIds := cluster.GetCoreIdsForRole(role)
	container.Spec.CoreIds = roleCoreIds

	container.Spec.CpuPolicy = updatableSpec.CpuPolicy

	var targetExtraCores int
	switch role {
	case weka.WekaContainerModeCompute:
		targetExtraCores = updatableSpec.ComputeExtraCores
	case weka.WekaContainerModeDrive:
		targetExtraCores = updatableSpec.DriveExtraCores
	case weka.WekaContainerModeS3:
		targetExtraCores = updatableSpec.S3ExtraCores
	case weka.WekaContainerModeNfs:
		targetExtraCores = updatableSpec.NfsExtraCores
	case weka.WekaContainerModeDataServices:
		targetExtraCores = updatableSpec.DataServicesExtraCores
	}
	container.Spec.ExtraCores = targetExtraCores

	var targetHp, targetHpOffset int
	switch role {
	case weka.WekaContainerModeCompute:
		targetHp = updatableSpec.ComputeHugepages
		targetHpOffset = updatableSpec.ComputeHugepagesOffset
	case weka.WekaContainerModeDrive:
		targetHp = updatableSpec.DriveHugepages
		targetHpOffset = updatableSpec.DriveHugepagesOffset
	case weka.WekaContainerModeS3:
		targetHp = updatableSpec.S3Hugepages
		targetHpOffset = updatableSpec.S3HugepagesOffset
	case weka.WekaContainerModeNfs:
		targetHp = updatableSpec.NfsHugepages
		targetHpOffset = updatableSpec.NfsHugepagesOffset
	case weka.WekaContainerModeDataServices:
		targetHp = updatableSpec.DataServicesHugepages
		targetHpOffset = updatableSpec.DataServicesHugepagesOffset
	}

	container.Spec.Hugepages = util.GetNonZeroOrDefault(targetHp, container.Spec.Hugepages)
	container.Spec.HugepagesOffset = util.GetNonZeroOrDefault(targetHpOffset, container.Spec.HugepagesOffset)

	if container.Spec.DriversLoaderImage != updatableSpec.DriversLoaderImage {
		container.Spec.DriversLoaderImage = updatableSpec.DriversLoaderImage
	}

	if (container.Spec.DriversBuildId == nil && updatableSpec.DriversBuildId != nil) ||
		(container.Spec.DriversBuildId != nil && updatableSpec.DriversBuildId == nil) ||
		(container.Spec.DriversBuildId != nil && updatableSpec.DriversBuildId != nil && *container.Spec.DriversBuildId != *updatableSpec.DriversBuildId) {
		container.Spec.DriversBuildId = updatableSpec.DriversBuildId
	}
}

// PropagateSpecToContainer fetches a container from K8s, applies all spec updates, and patches it.
// Used by the upgrade controller for rolling spec propagation.
func PropagateSpecToContainer(ctx context.Context, k8sClient client.Client, container *weka.WekaContainer, updatableSpec *UpdatableClusterSpec, cluster *weka.WekaCluster) error {
	targetHash := clusterConfigHash(&cluster.Spec)

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "PropagateSpecToContainer", "container", container.Name)
	defer end()

	err := k8sClient.Get(ctx, client.ObjectKey{Namespace: container.Namespace, Name: container.Name}, container)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	logger.Debug("Propagating spec to container", "container", container.Name, "mode", container.Spec.Mode, "targetHash", targetHash)
	patch := client.MergeFrom(container.DeepCopy())

	ApplyUpdatableSpecToContainer(container, updatableSpec, cluster)

	// Also set target image and config hash so upgrade controller can track progress
	container.Spec.Image = cluster.Spec.Image
	container.Spec.TargetClusterSpecHash = targetHash

	return k8sClient.Patch(ctx, container, patch)
}

func (r *wekaClusterReconcilerLoop) HandleSpecUpdates(ctx context.Context) error {
	cluster := r.cluster
	containers := r.containers

	updatableSpec := NewUpdatableClusterSpec(ctx, &cluster.Spec, &cluster.ObjectMeta, containers)
	targetHash := clusterConfigHash(&cluster.Spec)

	// Preserving whole Spec for more generic approach on status, while being able to update only specific fields on containers
	return workers.ProcessConcurrently(ctx, containers, 32, func(ctx context.Context, container *weka.WekaContainer) error {
		if container.Status.LastAppliedSpec == targetHash {
			return nil
		}

		ctx, logger, end := instrumentation.GetLogSpan(ctx, "handleContainerSpecUpdate", "container", container.Name)
		defer end()

		logger.Debug("Cluster<>Container spec hash has changed", "container", container.Name, "mode", container.Spec.Mode, "lastAppliedSpec", container.Status.LastAppliedSpec, "newSpecHash", targetHash)

		err := PropagateSpecToContainer(ctx, r.getClient(), container, updatableSpec, cluster)
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
		statusPatch := client.MergeFrom(container.DeepCopy())
		container.Status.LastAppliedSpec = targetHash
		return r.getClient().Status().Patch(ctx, container, statusPatch)
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
	err := wekaService.EmitCustomEvent(ctx, msg, utils.GetKubernetesVersion(r.Manager))
	if err != nil {
		logger.Warn("Failed to emit custom event", "event", msg)
	}
}

func (r *wekaClusterReconcilerLoop) handleUpgrade(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster
	clusterService := r.clusterService

	nums := allocator.GetWekaContainerNumbers(cluster.Spec.Dynamic)

	if clusterConfigHash(&cluster.Spec) != cluster.Status.LastAppliedConfig {
		logger.Info("Image upgrade sequence")
		targetVersion := utils.GetSoftwareVersion(cluster.Spec.Image)

		if cluster.Spec.GetOverrides().UpgradePaused {
			return lifecycle.NewWaitError(errors.New("Upgrade is paused"))
		}

		if config.Config.Upgrade.ImagePrePullEnabled && r.isImageChanged() {
			err := r.handleImagePrePull(ctx)
			if err != nil {
				return err
			}
		}

		if cluster.Spec.GetOverrides().UpgradeAllAtOnce {
			// containers will self-upgrade
			targetHash := clusterConfigHash(&cluster.Spec)
			updatableSpec := NewUpdatableClusterSpec(ctx, &cluster.Spec, &cluster.ObjectMeta, r.containers)
			return workers.ProcessConcurrently(ctx, r.containers, 32, func(ctx context.Context, container *weka.WekaContainer) error {
				if container.Spec.Image != cluster.Spec.Image || container.Spec.TargetClusterSpecHash != targetHash {
					return PropagateSpecToContainer(ctx, r.getClient(), container, updatableSpec, cluster)
				}
				return nil
			}).AsError()
		}

		driveContainers, err := clusterService.GetOwnedContainers(ctx, weka.WekaContainerModeDrive)
		if err != nil {
			return err
		}
		if r.isImageChanged() && !anyContainerConfigApplied(driveContainers, clusterConfigHash(&cluster.Spec)) {
			err := r.prepareForUpgradeDrives(ctx, driveContainers, targetVersion)
			if err != nil {
				return err
			}
		}

		execInContainer := discovery.SelectActiveContainer(r.containers)
		if execInContainer == nil {
			return errors.New("No active container found")
		}

		timeout := time.Second * 30
		wekaService := services.NewWekaServiceWithTimeout(r.ExecService, execInContainer, &timeout)
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

		activeDrivesThreshold := float64(nums.Drive) * (float64(config.Config.Upgrade.DriveThresholdPercent) / 100)
		activeComputesThreshold := float64(nums.Compute) * (float64(config.Config.Upgrade.ComputeThresholdPercent) / 100)

		if float64(status.Containers.Drives.Active) < activeDrivesThreshold {
			msg := fmt.Sprintf("Not enough drives containers are active, waiting to stabilize, %d/%d", status.Containers.Drives.Active, nums.Drive)
			_ = r.RecordEvent("", "ClusterSizeThreshold", msg)
			return lifecycle.NewWaitError(errors.New(msg))
		}

		if float64(status.Containers.Computes.Active) < activeComputesThreshold {
			msg := fmt.Sprintf("Not enough computes containers are active, waiting to stabilize, %d/%d", status.Containers.Computes.Active, nums.Compute)
			_ = r.RecordEvent("", "ClusterSizeThreshold", msg)
			return lifecycle.NewWaitError(errors.New(msg))
		}

		r.emitClusterUpgradeCustomEvent(ctx)

		targetHash := clusterConfigHash(&cluster.Spec)
		updatableSpec := NewUpdatableClusterSpec(ctx, &cluster.Spec, &cluster.ObjectMeta, r.containers)
		propagator := func(ctx context.Context, container *weka.WekaContainer) error {
			return PropagateSpecToContainer(ctx, r.getClient(), container, updatableSpec, cluster)
		}

		uController := upgrade.NewUpgradeController(r.getClient(), driveContainers, cluster.Spec.Image, targetHash)
		uController.SpecPropagator = propagator
		err = uController.RollingUpgrade(ctx)
		if err != nil {
			return err
		}

		computeContainers, err := clusterService.GetOwnedContainers(ctx, weka.WekaContainerModeCompute)
		if err != nil {
			return err
		}

		if r.isImageChanged() {
			if r.cluster.Spec.GetOverrides().UpgradePausePreCompute {
				return lifecycle.NewWaitError(errors.New("Upgrade paused before compute phase"))
			}
			if !anyContainerConfigApplied(computeContainers, targetHash) {
				err := r.prepareForUpgradeCompute(ctx, computeContainers, targetVersion)
				if err != nil {
					return err
				}
			}
		}

		uController = upgrade.NewUpgradeController(r.getClient(), computeContainers, cluster.Spec.Image, targetHash)
		uController.SpecPropagator = propagator
		err = uController.RollingUpgrade(ctx)
		if err != nil {
			return err
		}

		s3Containers, err := clusterService.GetOwnedContainers(ctx, weka.WekaContainerModeS3)
		if err != nil {
			return err
		}
		nfsContainres, err := clusterService.GetOwnedContainers(ctx, weka.WekaContainerModeNfs)
		if err != nil {
			return err
		}
		feContainers := append(s3Containers, nfsContainres...)

		if r.isImageChanged() && !anyContainerConfigApplied(feContainers, targetHash) {
			err := r.prepareForUpgradeS3(ctx, feContainers, targetVersion)
			if err != nil {
				return err
			}
		}

		uController = upgrade.NewUpgradeController(r.getClient(), feContainers, cluster.Spec.Image, targetHash)
		uController.SpecPropagator = propagator
		err = uController.RollingUpgrade(ctx)
		if err != nil {
			return err
		}

		if r.isImageChanged() {
			err = r.finalizeUpgrade(ctx, driveContainers)
			if err != nil {
				return err
			}
		}

		cluster.Status.LastAppliedImage = cluster.Spec.Image
		cluster.Status.LastAppliedConfig = clusterConfigHash(&cluster.Spec)
		if err := r.getClient().Status().Update(ctx, cluster); err != nil {
			return err
		}

		// Clear pre-pull annotations after successful upgrade
		err = r.clearAllPrePullAnnotations(ctx)
		if err != nil {
			logger.Warn("Failed to clear pre-pull annotations", "error", err)
			// Don't fail upgrade on annotation cleanup failure
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
