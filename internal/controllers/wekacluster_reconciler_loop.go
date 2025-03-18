package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"github.com/weka/weka-k8s-api/util"
	"go.opentelemetry.io/otel/codes"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	util2 "github.com/weka/weka-operator/pkg/util"
	"github.com/weka/weka-operator/pkg/workers"
)

type ReadyForClusterizationContainers struct {
	Drive   []*wekav1alpha1.WekaContainer
	Compute []*wekav1alpha1.WekaContainer
	// containers that are not ready for clusterization (e.g. unhealthy)
	Ignored []*wekav1alpha1.WekaContainer
}

func (c *ReadyForClusterizationContainers) GetAll() []*wekav1alpha1.WekaContainer {
	var all []*wekav1alpha1.WekaContainer
	all = append(all, c.Drive...)
	all = append(all, c.Compute...)
	return all
}

func NewWekaClusterReconcileLoop(r *WekaClusterReconciler) *wekaClusterReconcilerLoop {
	mgr := r.Manager
	config := mgr.GetConfig()
	restClient := r.RestClient
	execService := exec.NewExecService(restClient, config)
	scheme := mgr.GetScheme()
	return &wekaClusterReconcilerLoop{
		Manager:         mgr,
		ExecService:     execService,
		Recorder:        mgr.GetEventRecorderFor("wekaCluster-controller"),
		SecretsService:  services.NewSecretsService(mgr.GetClient(), scheme, execService),
		RestClient:      restClient,
		GlobalThrottler: r.ThrottlingMap,
	}
}

type wekaClusterReconcilerLoop struct {
	Manager         ctrl.Manager
	ExecService     exec.ExecService
	Recorder        record.EventRecorder
	cluster         *wekav1alpha1.WekaCluster
	clusterService  services.WekaClusterService
	containers      []*wekav1alpha1.WekaContainer
	SecretsService  services.SecretsService
	RestClient      rest.Interface
	GlobalThrottler *util2.ThrottlingSyncMap
	Throttler       util2.Throttler
	// internal field used to store data in-memory between steps
	readyContainers *ReadyForClusterizationContainers
}

func (r *wekaClusterReconcilerLoop) FetchCluster(ctx context.Context, req ctrl.Request) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "FetchCluster")
	defer end()

	wekaCluster := &wekav1alpha1.WekaCluster{}
	err := r.getClient().Get(ctx, req.NamespacedName, wekaCluster)
	if err != nil {
		return err
	}

	r.cluster = wekaCluster
	r.clusterService = services.NewWekaClusterService(r.Manager, r.RestClient, wekaCluster)
	r.Throttler = r.GlobalThrottler.WithPartition("cluster/" + string(wekaCluster.GetUID()))

	return err
}

func (r *wekaClusterReconcilerLoop) EnsureWekaContainers(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster

	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		keys := make([]string, 0, len(allocator.WekaClusterTemplates))
		for k := range allocator.WekaClusterTemplates {
			keys = append(keys, k)
		}
		err := errors.New("template not found")
		logger.Error(err, "", "template", cluster.Spec.Template, "keys", keys)
		return err
	}

	//newContainersLimit := config.Consts.NewContainersLimit
	missingContainers, err := BuildMissingContainers(ctx, cluster, template, r.containers)
	if err != nil {
		logger.Error(err, "Failed to create missing containers")
		return err
	}
	for _, container := range missingContainers {
		if err := ctrl.SetControllerReference(cluster, container, r.Manager.GetScheme()); err != nil {
			logger.Error(err, "Failed to set controller reference")
			return err
		}
	}

	if len(missingContainers) == 0 {
		return nil
	}

	resourcesAllocator, err := allocator.NewResourcesAllocator(ctx, r.getClient())
	if err != nil {
		logger.Error(err, "Failed to create resources allocator")
		return err
	}

	k8sClient := r.Manager.GetClient()
	if len(r.containers) == 0 {
		logger.InfoWithStatus(codes.Unset, "Ensuring cluster-level allocation")
		//TODO: should've be just own step function
		err = resourcesAllocator.AllocateClusterRange(ctx, cluster)
		var allocateRangeErr *allocator.AllocateClusterRangeError
		if errors.As(err, &allocateRangeErr) {
			_ = r.RecordEvent(v1.EventTypeWarning, "AllocateClusterRangeError", allocateRangeErr.Error())
			return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
		}
		if err != nil {
			logger.Error(err, "Failed to allocate cluster range")
			return err
		}
		err := k8sClient.Status().Update(ctx, cluster)
		if err != nil {
			logger.Error(err, "Failed to update cluster status")
			return err
		}
		// update weka cluster status
	}

	var joinIps []string
	if meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondClusterCreated) || cluster.IsExpand() {
		//TODO: Update-By-Expansion, cluster-side join-ips until there are own containers
		allowExpansion := false
		err := services.ClustersJoinIps.RefreshJoinIps(ctx, r.containers, cluster)
		if err != nil {
			allowExpansion = true
		}
		joinIps, err = services.ClustersJoinIps.GetJoinIps(ctx, cluster.Name, cluster.Namespace)
		// at this point we should have join ips, if not, we should allow expansion
		if len(joinIps) == 0 {
			allowExpansion = true
		}
		if err != nil && len(cluster.Spec.ExpandEndpoints) != 0 && allowExpansion { //TO
			joinIps = cluster.Spec.ExpandEndpoints
		} else {
			if err != nil {
				logger.Error(err, "Failed to get join ips")
				return err
			}
		}
	}

	for _, container := range missingContainers {
		if len(joinIps) != 0 {
			container.Spec.JoinIps = joinIps
		}
	}

	results := workers.ProcessConcurrently(ctx, missingContainers, 32, func(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
		err := r.getClient().Create(ctx, container)
		return err
	})

	for _, result := range results.Items {
		if result.Err == nil {
			r.containers = append(r.containers, result.Object)
		}
	}

	return results.AsError()
}

func (r *wekaClusterReconcilerLoop) getCurrentContainers(ctx context.Context) error {
	currentContainers := discovery.GetClusterContainers(ctx, r.getClient(), r.cluster, "")
	r.containers = currentContainers
	return nil
}

func (r *wekaClusterReconcilerLoop) getClient() client.Client {
	return r.Manager.GetClient()
}

func (r *wekaClusterReconcilerLoop) ClusterIsInGracefulDeletion() bool {
	if !r.cluster.IsMarkedForDeletion() {
		return false
	}

	deletionTime := r.cluster.GetDeletionTimestamp().Time
	gracefulDestroyDuration := r.cluster.GetGracefulDestroyDuration()
	hitTimeout := deletionTime.Add(gracefulDestroyDuration)
	return hitTimeout.After(time.Now())
}

func (r *wekaClusterReconcilerLoop) InitState(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	wekaCluster := r.cluster
	if !controllerutil.ContainsFinalizer(wekaCluster, WekaFinalizer) {

		wekaCluster.Status.InitStatus()
		wekaCluster.Status.LastAppliedImage = wekaCluster.Spec.Image

		err := r.getClient().Status().Update(ctx, wekaCluster)
		if err != nil {
			logger.Error(err, "failed to init states")
		}

		if updated := controllerutil.AddFinalizer(wekaCluster, WekaFinalizer); updated {
			logger.Info("Adding Finalizer for weka cluster")
			if err := r.getClient().Update(ctx, wekaCluster); err != nil {
				logger.Error(err, "Failed to update custom resource to add finalizer")
				return err
			}

			if err := r.getClient().Get(ctx, client.ObjectKey{Namespace: wekaCluster.Namespace, Name: wekaCluster.Name}, r.cluster); err != nil {
				logger.Error(err, "Failed to re-fetch data")
				return err
			}
			logger.Info("Finalizer added for wekaCluster", "conditions", len(wekaCluster.Status.Conditions))
		}
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) HandleGracefulDeletion(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster

	err := r.updateClusterStatusIfNotEquals(ctx, wekav1alpha1.WekaClusterStatusGracePeriod)
	if err != nil {
		return err
	}

	err = r.ensureContainersPaused(ctx, wekav1alpha1.WekaContainerModeS3)
	if err != nil {
		return err
	}

	err = r.ensureContainersPaused(ctx, wekav1alpha1.WekaContainerModeNfs)
	if err != nil {
		return err
	}

	err = r.ensureContainersPaused(ctx, "")
	if err != nil {
		return err
	}

	gracefulDestroyDuration := r.cluster.GetGracefulDestroyDuration()
	deletionTime := cluster.GetDeletionTimestamp().Time.Add(gracefulDestroyDuration)

	logger.Info("Cluster is in graceful deletion", "deletionTime", deletionTime)
	return nil
}

func (r *wekaClusterReconcilerLoop) ensureContainersPaused(ctx context.Context, mode string) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureContainersPaused", "mode", mode)
	defer end()

	return workers.ProcessConcurrently(ctx, r.containers, 32, func(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
		ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureContainerPaused", "container", container.Name, "mode", mode, "container_mode", container.Spec.Mode)
		defer end()
		if mode != "" && container.Spec.Mode != mode {
			return nil
		}

		ctx, _, end2 := instrumentation.GetLogSpan(ctx, "ensureMatchingContainerPaused", "container", container.Name, "mode", mode, "container_mode", container.Spec.Mode)
		defer end2()

		if !container.IsPaused() {
			patch := map[string]interface{}{
				"spec": map[string]interface{}{
					"state": wekav1alpha1.ContainerStatePaused,
				},
			}

			patchBytes, err := json.Marshal(patch)
			if err != nil {
				return fmt.Errorf("failed to marshal patch for container %s: %w", container.Name, err)
			}

			err = errors.Wrap(
				r.getClient().Patch(ctx, container, client.RawPatch(types.MergePatchType, patchBytes)),
				fmt.Sprintf("failed to update container state %s: %v", container.Name, err),
			)
			if err != nil {
				return err
			}
		}

		if container.Status.Status != wekav1alpha1.Paused {
			return fmt.Errorf("container %s is not paused yet", container.Name)
		}

		return nil
	}).AsError()
}

func (r *wekaClusterReconcilerLoop) HandleDeletion(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	gracefulDestroyDuration := r.cluster.GetGracefulDestroyDuration()
	deletionTime := r.cluster.GetDeletionTimestamp().Time.Add(gracefulDestroyDuration)
	logger.Debug("Not graceful deletion", "deletionTime", deletionTime, "now", time.Now(), "gracefulDestroyDuration", gracefulDestroyDuration)

	err := r.updateClusterStatusIfNotEquals(ctx, wekav1alpha1.WekaClusterStatusDestroying)
	if err != nil {
		return err
	}

	if controllerutil.ContainsFinalizer(r.cluster, WekaFinalizer) {
		logger.Info("Performing Finalizer Operations for wekaCluster before delete CR")

		// Perform all operations required before remove the finalizer and allow
		// the Kubernetes API to remove the custom resource.
		err := r.finalizeWekaCluster(ctx)
		if err != nil {
			return err
		}

		logger.Info("Removing Finalizer for wekaCluster after successfully perform the operations")
		if ok := controllerutil.RemoveFinalizer(r.cluster, WekaFinalizer); !ok {
			err := errors.New("Failed to remove finalizer for wekaCluster")
			return err
		}

		if err := r.getClient().Update(ctx, r.cluster); err != nil {
			logger.Error(err, "Failed to remove finalizer for wekaCluster")
			return err
		}

	}
	return nil
}

func (r *wekaClusterReconcilerLoop) finalizeWekaCluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster
	clusterService := r.clusterService

	err := clusterService.EnsureNoContainers(ctx, wekav1alpha1.WekaContainerModeS3)
	if err != nil {
		return err
	}

	err = clusterService.EnsureNoContainers(ctx, wekav1alpha1.WekaContainerModeNfs)
	if err != nil {
		return err
	}

	err = clusterService.EnsureNoContainers(ctx, "")
	if err != nil {
		return err
	}

	logger.Debug("All containers are removed, deallocating cluster resources")

	err = r.updateClusterStatusIfNotEquals(ctx, wekav1alpha1.WekaClusterStatusDeallocating)
	if err != nil {
		return err
	}

	resourcesAllocator, err := allocator.NewResourcesAllocator(ctx, r.getClient())
	if err != nil {
		return err
	}

	err = resourcesAllocator.DeallocateCluster(ctx, cluster)
	if err != nil {
		return err
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) EnsureLoginCredentials(ctx context.Context) error {
	return r.SecretsService.EnsureLoginCredentials(ctx, r.cluster)
}

func (r *wekaClusterReconcilerLoop) HandleSpecUpdates(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleSpecUpdates")
	defer end()

	cluster := r.cluster
	containers := r.containers

	updatableSpec := NewUpdatableClusterSpec(&cluster.Spec, &cluster.ObjectMeta)
	// Preserving whole Spec for more generic approach on status, while being able to update only specific fields on containers
	specHash, err := util2.HashStruct(updatableSpec)
	if err != nil {
		return err
	}
	if specHash != cluster.Status.LastAppliedSpec {
		logger.Info("Cluster spec has changed, updating containers")
		err := workers.ProcessConcurrently(ctx, containers, 32, func(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
			patch := client.MergeFrom(container.DeepCopy())
			changed := false

			additionalMemory := updatableSpec.AdditionalMemory.GetForMode(container.Spec.Mode)
			if container.Spec.AdditionalMemory != additionalMemory {
				container.Spec.AdditionalMemory = additionalMemory
				changed = true
			}

			newTolerations := util.ExpandTolerations([]v1.Toleration{}, updatableSpec.Tolerations, updatableSpec.RawTolerations)
			oldTolerations := util.NormalizeTolerations(container.Spec.Tolerations)
			if !reflect.DeepEqual(oldTolerations, newTolerations) {
				container.Spec.Tolerations = newTolerations
				changed = true
			}

			if container.Spec.DriversDistService != updatableSpec.DriversDistService {
				container.Spec.DriversDistService = updatableSpec.DriversDistService
				changed = true
			}

			if container.Spec.ImagePullSecret != updatableSpec.ImagePullSecret {
				container.Spec.ImagePullSecret = updatableSpec.ImagePullSecret
				changed = true
			}

			if container.Spec.GetOverrides().UpgradeForceReplace != updatableSpec.UpgradeForceReplace {
				currentOverrides := container.Spec.GetOverrides()
				currentOverrides.UpgradeForceReplace = updatableSpec.UpgradeForceReplace
				container.Spec.Overrides = currentOverrides
				changed = true
			}

			targetNetworkSpec, err := resources.GetContainerNetwork(updatableSpec.NetworkSelector)
			if err != nil {
				return err
			}
			oldNetworkHash, err := util2.HashStruct(container.Spec.Network)
			if err != nil {
				return err
			}
			targetNetworkHash, err := util2.HashStruct(targetNetworkSpec)
			if err != nil {
				return err
			}
			if oldNetworkHash != targetNetworkHash {
				container.Spec.Network = targetNetworkSpec
				changed = true
			}

			// desired labels = cluster labels + required labels
			// priority-wise, required labels have the highest priority
			requiredLables := factory.RequiredWekaContainerLabels(cluster.UID, cluster.Name, container.Spec.Mode)
			newLabels := util2.MergeMaps(cluster.ObjectMeta.GetLabels(), requiredLables)
			if !util2.NewHashableMap(newLabels).Equals(util2.NewHashableMap(container.Labels)) {
				container.Labels = newLabels
				changed = true
			}

			oldNodeSelector := util2.NewHashableMap(container.Spec.NodeSelector)
			role := container.Spec.Mode
			newNodeSelector := map[string]string{}
			if role != wekav1alpha1.WekaContainerModeEnvoy { // envoy sticks to s3, so does not need explicit node selector
				newNodeSelector = cluster.Spec.NodeSelector
				if len(cluster.Spec.RoleNodeSelector.ForRole(role)) != 0 {
					newNodeSelector = cluster.Spec.RoleNodeSelector.ForRole(role)
				}
			}
			if !util2.NewHashableMap(newNodeSelector).Equals(oldNodeSelector) {
				container.Spec.NodeSelector = cluster.Spec.NodeSelector
				changed = true
			}

			if changed {
				return r.getClient().Patch(ctx, container, patch)
			}
			return nil
		}).AsError()
		if err != nil {
			return err
		}
		logger.Info("Updating last applied spec", "lastAppliedSpec", cluster.Status.LastAppliedSpec, "newSpec", specHash)
		cluster.Status.LastAppliedSpec = specHash
		return r.getClient().Status().Update(ctx, cluster)
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) getReadyForClusterCreateContainers(ctx context.Context) *ReadyForClusterizationContainers {
	if r.readyContainers != nil {
		return r.readyContainers
	}

	cluster := r.cluster
	containers := r.containers

	findSameNetworkConfig := len(cluster.Spec.NetworkSelector.DeviceSubnets) > 0
	readyContainers := &ReadyForClusterizationContainers{}

	for _, container := range containers {
		if container.GetDeletionTimestamp() != nil {
			continue
		}
		if container.Status.Status == wekav1alpha1.Unhealthy {
			readyContainers.Ignored = append(readyContainers.Ignored, container)
			continue
		}
		if container.Status.Status != wekav1alpha1.Running {
			continue
		}
		// if deviceSubnets are provided, we should only consider containers that have devices in the provided subnets
		if findSameNetworkConfig {
			if !resources.ContainerHasDevicesInSubnets(container, cluster.Spec.NetworkSelector.DeviceSubnets) {
				readyContainers.Ignored = append(readyContainers.Ignored, container)
			}
		}
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeDrive {
			readyContainers.Drive = append(readyContainers.Drive, container)
		}
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeCompute {
			readyContainers.Compute = append(readyContainers.Compute, container)
		}
	}

	r.readyContainers = readyContainers
	return readyContainers
}

// InitialContainersReady Logic:
// there are 4 consts defined in config:
//   - FormClusterMinDriveContainers   (5 by default)
//   - FormClusterMinComputeContainers (5 by default)
//   - FormClusterMaxDriveContainers   (10 by default)
//   - FormClusterMaxComputeContainers (10 by default)
//
// Uses getReadyForClusterCreateContainers to get the containers that are UP and ready for cluster creation
// and containers that should be ignored (e.g. unhealthy).
//
// Expected containers number is derived from the template:
//   - expectedComputeContainersNum = min(template.ComputeContainers, FormClusterMaxComputeContainers)
//   - expectedDriveContainersNum = min(template.DriveContainers, FormClusterMaxDriveContainers)
//
// The following checks are performed:
//   - number of "ready" drive containers < FormClusterMinDriveContainers --> error
//   - number of "ready" compute containers < FormClusterMinComputeContainers --> error
//   - number of "ready" containers + number of "ignored" containers < expectedComputeContainersNum + expectedDriveContainersNum --> error
func (r *wekaClusterReconcilerLoop) InitialContainersReady(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster

	minDriveContainers := config.Consts.FormClusterMinDriveContainers
	minComputeContainers := config.Consts.FormClusterMinComputeContainers

	findSameNetworkConfig := len(cluster.Spec.NetworkSelector.DeviceSubnets) > 0
	if findSameNetworkConfig {
		msg := fmt.Sprintf("Looking for %d compute and %d drive containers with device subnets %v", minComputeContainers, minDriveContainers, cluster.Spec.NetworkSelector.DeviceSubnets)
		logger.Debug(msg)
	} else {
		msg := fmt.Sprintf("Looking for %d compute and %d drive containers", minComputeContainers, minDriveContainers)
		logger.Debug(msg)
	}

	// containers that UP and ready for cluster creation
	readyContainers := r.getReadyForClusterCreateContainers(ctx)
	driveContainersCount := len(readyContainers.Drive)
	computeContainersCount := len(readyContainers.Compute)
	ignoredContainersCount := len(readyContainers.Ignored)

	if driveContainersCount < minDriveContainers {
		err := fmt.Errorf("not enough drive containers ready, expected %d, got %d", minDriveContainers, driveContainersCount)
		r.RecordEvent("", "MinContainersNotReady", err.Error())
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}
	if computeContainersCount < minComputeContainers {
		err := fmt.Errorf("not enough compute containers ready, expected %d, got %d", minComputeContainers, computeContainersCount)
		r.RecordEvent("", "MinContainersNotReady", err.Error())
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	// check expected containers number
	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		return errors.New("Failed to get template")
	}

	expectedComputeContainersNum := util2.GetMinValue(template.ComputeContainers, config.Consts.FormClusterMaxComputeContainers)
	expectedDriveContainersNum := util2.GetMinValue(template.DriveContainers, config.Consts.FormClusterMaxDriveContainers)

	if driveContainersCount+computeContainersCount+ignoredContainersCount < expectedComputeContainersNum+expectedDriveContainersNum {
		err := fmt.Errorf("waiting for all containers to be either ready or ignored before forming the cluster, expected %d, got %d", expectedComputeContainersNum+expectedDriveContainersNum, driveContainersCount+computeContainersCount+ignoredContainersCount)
		r.RecordEvent("", "ContainersNotReady", err.Error())
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	msg := fmt.Sprintf("Initial containers are ready for cluster creation, drive containers: %d, compute containers: %d", driveContainersCount, computeContainersCount)
	r.RecordEvent("", "InitialContainersReady", msg)
	logger.InfoWithStatus(codes.Ok, msg)
	return nil
}

func (r *wekaClusterReconcilerLoop) FormCluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	wekaCluster := r.cluster
	wekaClusterService := r.clusterService

	if wekaCluster.Spec.ExpandEndpoints == nil {
		readyContainers := r.getReadyForClusterCreateContainers(ctx)

		err := wekaClusterService.FormCluster(ctx, readyContainers.GetAll())
		if err != nil {
			logger.Error(err, "Failed to form cluster")
			return lifecycle.NewWaitError(err)
		}
		return nil
		// TODO: We might want to capture specific errors, and return "unknown"/bigger errors
	}
	return nil
}

// Waits for initial containers to join the cluster right after the cluster is formed
func (r *wekaClusterReconcilerLoop) WaitForContainersJoin(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	readyContainers := r.getReadyForClusterCreateContainers(ctx)

	logger.Debug("Ensuring all ready containers are up in the cluster")
	joinedContainers := 0
	for _, container := range readyContainers.GetAll() {
		if !container.ShouldJoinCluster() {
			continue
		}
		if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondJoinedCluster) {
			logger.Info("Container has not joined the cluster yet", "container", container.Name)
			return lifecycle.NewWaitError(errors.New("container did not join cluster yet"))
		} else {
			if r.cluster.Status.ClusterID == "" {
				r.cluster.Status.ClusterID = container.Status.ClusterID
				err := r.getClient().Status().Update(ctx, r.cluster)
				if err != nil {
					return err
				}
				logger.Info("Container joined cluster successfully", "container_name", container.Name)
			}
			joinedContainers++
		}
		if container.Status.ClusterContainerID == nil {
			err := fmt.Errorf("container %s does not have a cluster container id", container.Name)
			r.RecordEvent(v1.EventTypeWarning, "ContainerJoinError", err.Error())
			return err
		}
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) WaitForDrivesAdd(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	err := r.updateClusterStatusIfNotEquals(ctx, wekav1alpha1.WekaClusterStatusWaitDrives)
	if err != nil {
		return err
	}
	// minimum required number of drives to be added to the cluster
	var minDrivesNum int

	if r.cluster.Spec.GetStartIoConditions().MinNumDrives > 0 {
		minDrivesNum = r.cluster.Spec.StartIoConditions.MinNumDrives
	}

	// if not provided, derive it from the template
	if minDrivesNum == 0 {
		template, ok := allocator.GetTemplateByName(r.cluster.Spec.Template, *r.cluster)
		if !ok {
			return errors.New("Failed to get template")
		}
		minDrivesNum = template.NumDrives * template.DriveContainers
	}

	// get the number of drives added to the cluster from weka status
	container := discovery.SelectActiveContainer(r.containers)

	wekaService := services.NewWekaService(r.ExecService, container)
	status, err := wekaService.GetWekaStatus(ctx)
	if err != nil {
		return err
	}

	totalDrivesNum := status.Drives.Total
	if totalDrivesNum >= minDrivesNum {
		logger.Info("Min drives num added to the cluster", "addedDrivesNum", totalDrivesNum, "minDrivesNum", minDrivesNum)
		return nil
	}

	msg := fmt.Sprintf("Min drives num not added to the cluster yet, added drives: %d, min drives: %d", totalDrivesNum, minDrivesNum)
	r.RecordEvent("", "WaitingForDrives", msg)

	return lifecycle.NewWaitErrorWithDuration(errors.New("containers did not add drives yet"), 10*time.Second)
}

func (r *wekaClusterReconcilerLoop) StartIo(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	err := r.updateClusterStatusIfNotEquals(ctx, wekav1alpha1.WekaClusterStatusStartingIO)
	if err != nil {
		return err
	}

	containers := r.containers

	if len(containers) == 0 {
		err := errors.New("containers list is empty")
		logger.Error(err, "containers list is empty")
		return err
	}

	kubeExecTimeout := config.Config.Timeouts.KubeExecTimeout
	if kubeExecTimeout < 10*time.Minute {
		kubeExecTimeout = 10 * time.Minute
	}

	execInContainer := discovery.SelectActiveContainer(containers)
	executor, err := r.ExecService.GetExecutorWithTimeout(ctx, execInContainer, &kubeExecTimeout)
	if err != nil {
		return errors.Wrap(err, "Error creating executor")
	}

	cmd := "wekaauthcli cluster start-io"
	_, stderr, err := executor.ExecNamed(ctx, "StartIO", []string{"bash", "-ce", cmd})
	if err != nil {
		logger.WithValues("stderr", stderr.String()).Error(err, "Failed to start-io")
		return errors.Wrapf(err, "Failed to start-io: %s", stderr.String())
	}
	logger.InfoWithStatus(codes.Ok, "IO started", "time_taken", time.Since(r.cluster.CreationTimestamp.Time).String())
	return nil
}

func (r *wekaClusterReconcilerLoop) ApplyCredentials(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()
	cluster := r.cluster
	containers := r.containers

	container := discovery.SelectActiveContainer(containers)
	wekaService := services.NewWekaService(r.ExecService, container)

	ensureUser := func(secretName string) error {
		// fetch secret from k8s
		username, password, err := r.getUsernameAndPassword(ctx, cluster.Namespace, secretName)
		if username == "admin" {
			// force switch back to operator user
			username = cluster.GetOperatorClusterUsername()
		}
		if err != nil {
			return err
		}
		err = wekaService.EnsureUser(ctx, username, password, "clusteradmin")
		if err != nil {
			return err
		}
		return nil
	}

	logger.WithValues("user_name", cluster.GetOperatorClusterUsername()).Info("Ensuring operator user")
	if err := ensureUser(cluster.GetOperatorSecretName()); err != nil {
		logger.Error(err, "Failed to apply operator user credentials")
		return err
	}

	logger.WithValues("user_name", cluster.GetUserClusterUsername()).Info("Ensuring admin user")
	if err := ensureUser(cluster.GetUserSecretName()); err != nil {
		logger.Error(err, "Failed to apply admin user credentials")
		return err
	}

	// change k8s secret to use proper account
	err := r.SecretsService.UpdateOperatorLoginSecret(ctx, cluster)
	if err != nil {
		return err
	}

	// Cannot delete admin at is used until we apply credential here, i.e creating new users
	// TODO: Delete later? How can we know that all pod updated their secrets? Or just, delay by 5 minutes? Docs says it's 1 minute

	//err := wekaService.EnsureNoUser(ctx, "admin")
	//if err != nil {
	//	return err
	//}
	logger.Info("Cluster credentials applied")
	return nil

}

func (r *wekaClusterReconcilerLoop) getUsernameAndPassword(ctx context.Context, namespace string, secretName string) (string, string, error) {
	secret := &v1.Secret{}
	err := r.getClient().Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret)
	if err != nil {
		return "", "", err
	}
	username := secret.Data["username"]
	password := secret.Data["password"]
	return string(username), string(password), nil
}

func (r *wekaClusterReconcilerLoop) ensureClientLoginCredentials(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster
	containers := r.containers

	activeContainer, err := discovery.SelectActiveContainerWithRole(ctx, containers, wekav1alpha1.WekaContainerModeDrive)
	if err != nil {
		return err
	}

	err = r.SecretsService.EnsureClientLoginCredentials(ctx, cluster, activeContainer)
	if err != nil {
		logger.Error(err, "Failed to apply client login credentials")
		return err
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) DeleteAdminUser(ctx context.Context) error {
	container := discovery.SelectActiveContainer(r.containers)
	wekaService := services.NewWekaService(r.ExecService, container)
	return wekaService.EnsureNoUser(ctx, "admin")
}

func (r *wekaClusterReconcilerLoop) EnsureDefaultFS(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureDefaultFs")
	defer end()

	container := discovery.SelectActiveContainer(r.containers)

	wekaService := services.NewWekaService(r.ExecService, container)
	status, err := wekaService.GetWekaStatus(ctx)
	if err != nil {
		return err
	}

	err = wekaService.CreateFilesystemGroup(ctx, "default")
	if err != nil {
		var fsGroupExists *services.FilesystemGroupExists
		if !errors.As(err, &fsGroupExists) {
			logger.Error(err, "DID NOT detect FS as already exists")
			return err
		}
	}

	// This defaults are not meant to be configurable, as instead weka should not require them.
	// Until then, user configuratino post cluster create

	var thinProvisionedLimitsConfigFS int64 = 100 * 1024 * 1024 * 1024 // half a total capacity allocated for thin provisioning
	thinProvisionedLimitsDefault := status.Capacity.TotalBytes / 10    // half a total capacity allocated for thin provisioning
	fsReservedCapacity := status.Capacity.TotalBytes / 100
	var configFsSize int64 = 10 * 1024 * 1024 * 1024
	var defaultFsSize int64 = 1 * 1024 * 1024 * 1024

	if defaultFsSize > thinProvisionedLimitsDefault {
		thinProvisionedLimitsDefault = defaultFsSize
	}

	err = wekaService.CreateFilesystem(ctx, ".config_fs", "default", services.FSParams{
		TotalCapacity:             strconv.FormatInt(thinProvisionedLimitsConfigFS, 10),
		ThickProvisioningCapacity: strconv.FormatInt(configFsSize, 10),
		ThinProvisioningEnabled:   true,
	})
	if err != nil {
		var fsExists *services.FilesystemExists
		if !errors.As(err, &fsExists) {
			return err
		}
	}

	err = wekaService.CreateFilesystem(ctx, "default", "default", services.FSParams{
		TotalCapacity:             strconv.FormatInt(thinProvisionedLimitsDefault, 10),
		ThickProvisioningCapacity: strconv.FormatInt(fsReservedCapacity, 10),
		ThinProvisioningEnabled:   true,
	})
	if err != nil {
		var fsExists *services.FilesystemExists
		if !errors.As(err, &fsExists) {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "default filesystem ensured")
	return nil
}

func (r *wekaClusterReconcilerLoop) SelectS3Containers(containers []*wekav1alpha1.WekaContainer) []*wekav1alpha1.WekaContainer {
	var s3Containers []*wekav1alpha1.WekaContainer
	for _, container := range containers {
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeS3 {
			s3Containers = append(s3Containers, container)
		}
	}
	return s3Containers
}

func (r *wekaClusterReconcilerLoop) SelectNfsContainers(containers []*wekav1alpha1.WekaContainer) []*wekav1alpha1.WekaContainer {
	var nfsContainers []*wekav1alpha1.WekaContainer
	for _, container := range containers {
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeNfs {
			nfsContainers = append(nfsContainers, container)
		}
	}
	return nfsContainers
}

func (r *wekaClusterReconcilerLoop) EnsureS3Cluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureS3Cluster")
	defer end()

	cluster := r.cluster

	execInContainer := discovery.SelectActiveContainer(r.containers)
	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	// check if s3 cluster already exists
	s3Cluster, err := wekaService.GetS3Cluster(ctx)
	if err != nil {
		err = errors.Wrap(err, "Failed to get S3 cluster")
		return err
	}

	if s3Cluster.Active || len(s3Cluster.S3Hosts) > 0 {
		logger.Info("S3 cluster already exists")
		return nil
	}

	s3Containers := r.SelectS3Containers(r.containers)
	containerIds := []int{}
	for _, c := range s3Containers {
		if len(containerIds) == config.Consts.FormS3ClusterMaxContainerCount {
			logger.Debug("Max S3 containers reached for initial s3 cluster creation", "maxContainers", config.Consts.FormS3ClusterMaxContainerCount)
			break
		}

		if c.Status.ClusterContainerID == nil {
			msg := fmt.Sprintf("s3 container %s does not have a cluster container id", c.Name)
			logger.Debug(msg)
			continue
		}
		containerIds = append(containerIds, *c.Status.ClusterContainerID)
	}

	if len(containerIds) == 0 {
		err := errors.New("Ready S3 containers not found")
		logger.Error(err, "Cannot create S3 cluster")
		return err
	}

	logger.Debug("Creating S3 cluster", "containers", containerIds)

	err = wekaService.CreateS3Cluster(ctx, services.S3Params{
		EnvoyPort:      cluster.Status.Ports.LbPort,
		EnvoyAdminPort: cluster.Status.Ports.LbAdminPort,
		S3Port:         cluster.Status.Ports.S3Port,
		ContainerIds:   containerIds,
	})
	if err != nil {
		if !errors.As(err, &services.S3ClusterExists{}) {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "S3 cluster ensured")
	return nil
}

func (r *wekaClusterReconcilerLoop) ShouldDestroyS3Cluster() bool {
	if !r.cluster.Spec.GetOverrides().AllowS3ClusterDestroy {
		return false
	}

	// if spec contains desired S3 containers, do not destroy the cluster
	template, ok := allocator.GetTemplateByName(r.cluster.Spec.Template, *r.cluster)
	if !ok {
		return false
	}
	if template.S3Containers > 0 {
		return false
	}

	containers := r.SelectS3Containers(r.containers)

	// if there are more that 1 S3 container, we should not destroy the cluster
	if len(containers) > 1 {
		return false
	}

	// if S3 cluster was not created, we should not destroy it
	if !meta.IsStatusConditionTrue(r.cluster.Status.Conditions, condition.CondS3ClusterCreated) {
		return false
	}

	return true
}

func (r *wekaClusterReconcilerLoop) DestroyS3Cluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := discovery.SelectActiveContainer(r.containers)

	wekaService := services.NewWekaService(r.ExecService, container)

	s3ContainerIds, err := wekaService.ListS3ClusterContainers(ctx)
	if err != nil {
		return err
	}
	logger.Info("S3 cluster containers", "containers", s3ContainerIds)

	if len(s3ContainerIds) > 1 {
		err := fmt.Errorf("more than one container in S3 cluster: %v", s3ContainerIds)
		return lifecycle.NewWaitError(err)
	}

	logger.Info("Destroying S3 cluster")
	err = wekaService.DeleteS3Cluster(ctx)
	if err != nil {
		err = errors.Wrap(err, "Failed to delete S3 cluster")
		return err
	}

	// invalidate S3 cluster created condition
	changed := meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondS3ClusterCreated,
		Status: metav1.ConditionFalse,
		Reason: "DestroyS3Cluster",
	})
	if changed {
		err := r.getClient().Status().Update(ctx, r.cluster)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) EnsureNfs(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureNfs")
	defer end()

	nfsContainers := r.SelectNfsContainers(r.containers)

	execInContainer := discovery.SelectActiveContainer(r.containers)
	wekaService := services.NewWekaService(r.ExecService, execInContainer)
	containerIds := []int{}
	for _, c := range nfsContainers {
		containerIds = append(containerIds, *c.Status.ClusterContainerID)
	}

	err := wekaService.ConfigureNfs(ctx, services.NFSParams{
		ConfigFilesystem: ".config_fs",
	})

	if err != nil {
		if !errors.As(err, &services.NfsInterfaceGroupExists{}) {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "NFS ensured")
	return nil
}

func (r *wekaClusterReconcilerLoop) applyClientLoginCredentials(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "applyClientLoginCredentials")
	defer end()

	cluster := r.cluster
	containers := r.containers

	container := discovery.SelectActiveContainer(containers)
	username, password, err := r.getUsernameAndPassword(ctx, cluster.Namespace, cluster.GetClientSecretName())
	if err != nil {
		err = fmt.Errorf("failed to get client login credentials: %w", err)
		return err
	}

	wekaService := services.NewWekaService(r.ExecService, container)
	err = wekaService.EnsureUser(ctx, username, password, "regular")
	if err != nil {
		logger.Error(err, "Failed to ensure user")
		return err
	}
	logger.SetStatus(codes.Ok, "Client login credentials applied")
	return nil
}

func (r *wekaClusterReconcilerLoop) applyCSILoginCredentials(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "applyCSILoginCredentials")
	defer end()

	cluster := r.cluster
	containers := r.containers

	container := discovery.SelectActiveContainer(containers)
	username, password, err := r.getUsernameAndPassword(ctx, cluster.Namespace, cluster.GetCSISecretName())
	if err != nil {
		err = fmt.Errorf("failed to get client login credentials: %w", err)
		return err
	}

	wekaService := services.NewWekaService(r.ExecService, container)
	err = wekaService.EnsureUser(ctx, username, password, "clusteradmin")
	if err != nil {
		logger.Error(err, "Failed to ensure user")
		return err
	}
	logger.SetStatus(codes.Ok, "CSI login credentials applied")
	return nil
}

func (r *wekaClusterReconcilerLoop) configureWekaHome(ctx context.Context) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "configureWekaHome")
	defer end()

	wekaCluster := r.cluster
	containers := r.containers

	config, err := domain.GetWekahomeConfig(wekaCluster)
	if err != nil {
		return err
	}

	if config.Endpoint == "" {
		return nil // explicitly asked not to configure
	}

	driveContainer, err := discovery.SelectActiveContainerWithRole(ctx, containers, wekav1alpha1.WekaContainerModeDrive)
	if err != nil {
		return err
	}

	wekaService := services.NewWekaService(r.ExecService, driveContainer)
	err = wekaService.SetWekaHome(ctx, config)
	if err != nil {
		return err
	}
	return err
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

		if cluster.Spec.GetOverrides().UpgradeAllAtOnce {
			// containers will self-upgrade
			return workers.ProcessConcurrently(ctx, r.containers, 32, func(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
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

		driveContainers, err := clusterService.GetOwnedContainers(ctx, wekav1alpha1.WekaContainerModeDrive)
		if err != nil {
			return err
		}
		// before upgrade, if if all drive nodes are still in old version - invoke upgrade prepare commands
		prepareForUpgrade := true
		for _, container := range driveContainers {
			//i.e if any container already on new target version - we should not prepare for drive phase
			if container.Spec.Image == cluster.Spec.Image {
				prepareForUpgrade = false
			}
		}
		if prepareForUpgrade {
			err := r.prepareForUpgradeDrives(ctx, driveContainers, cluster.Spec.Image)
			if err != nil {
				return err
			}
		}

		wekaService := services.NewWekaService(r.ExecService, discovery.SelectActiveContainer(r.containers))
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

		uController := NewUpgradeController(r.getClient(), driveContainers, cluster.Spec.Image)
		err = uController.RollingUpgrade(ctx)
		if err != nil {
			return err
		}

		computeContainers, err := clusterService.GetOwnedContainers(ctx, wekav1alpha1.WekaContainerModeCompute)
		if err != nil {
			return err
		}

		prepareForUpgrade = true
		// if any compute container changed version - do not prepare for compute
		for _, container := range computeContainers {
			if container.Spec.Image == cluster.Spec.Image {
				prepareForUpgrade = false
			}
		}
		if prepareForUpgrade {
			err := r.prepareForUpgradeCompute(ctx, computeContainers, cluster.Spec.Image)
			if err != nil {
				return err
			}
		}

		uController = NewUpgradeController(r.getClient(), computeContainers, cluster.Spec.Image)
		err = uController.RollingUpgrade(ctx)
		if err != nil {
			return err
		}

		s3Containers, err := clusterService.GetOwnedContainers(ctx, wekav1alpha1.WekaContainerModeS3)
		if err != nil {
			return err
		}
		prepareForUpgrade = true
		// if any s3 container changed version - do not prepare for s3
		for _, container := range s3Containers {
			if container.Spec.Image == cluster.Spec.Image {
				prepareForUpgrade = false
			}
		}
		if prepareForUpgrade {
			err := r.prepareForUpgradeS3(ctx, s3Containers, cluster.Spec.Image)
			if err != nil {
				return err
			}
		}

		uController = NewUpgradeController(r.getClient(), s3Containers, cluster.Spec.Image)
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

// getSoftwareVersion extracts the software version of weka
func getSoftwareVersion(image string) string {
	idx := strings.LastIndex(image, ":")
	if idx == -1 {
		return ""
	}
	tag := image[idx+1:]
	parts := strings.Split(tag, "-")
	return parts[0]
}

func (r *wekaClusterReconcilerLoop) prepareForUpgradeDrives(ctx context.Context, containers []*wekav1alpha1.WekaContainer, image string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "prepareForUpgradeDrives")
	defer end()

	executor, err := r.ExecService.GetExecutor(ctx, containers[0])
	if err != nil {
		logger.Error(err, "Failed to create executor")
		return nil
	}

	targetVersion := getSoftwareVersion(image)

	cmd := `
wekaauthcli debug jrpc prepare_leader_for_upgrade
wekaauthcli debug jrpc upgrade_phase_start target_phase_type=DrivePhase target_version_name=` + targetVersion + `
`

	_, stderr, err := executor.ExecNamed(ctx, "PrepareForUpgradeDrives", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to prepare for upgrade: %s", stderr.String())
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) prepareForUpgradeCompute(ctx context.Context, containers []*wekav1alpha1.WekaContainer, targetVersion string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "prepareForUpgradeCompute")
	defer end()

	executor, err := r.ExecService.GetExecutor(ctx, containers[0])
	if err != nil {
		logger.Error(err, "Failed to create executor")
		return nil
	}

	cmd := `
wekaauthcli status --json | grep upgrade_phase | grep -i compute ||  wekaauthcli debug jrpc upgrade_phase_finish
wekaauthcli debug jrpc upgrade_phase_start target_phase_type=ComputeRollingPhase target_version_name=` + targetVersion + `
`

	_, stderr, err := executor.ExecNamed(ctx, "PrepareForUpgradeCompute", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to prepare for upgrade: %s", stderr.String())
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) prepareForUpgradeS3(ctx context.Context, containers []*wekav1alpha1.WekaContainer, targetVersion string) error {
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
wekaauthcli debug jrpc upgrade_phase_finish
wekaauthcli debug jrpc upgrade_phase_start target_phase_type=FrontendPhase target_version_name=` + targetVersion + `
`
	_, stderr, err := executor.ExecNamed(ctx, "PrepareForUpgradeS3", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to prepare for upgrade: %s", stderr.String())
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) finalizeUpgrade(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error {
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

func (r *wekaClusterReconcilerLoop) AllocateResources(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "AllocateResources")
	defer end()

	// Fetch all own containers
	// Filter by .Allocated == nil
	// TODO: Figure out if this filtering can be done by indexing, if not - rely on labels filtering, updating spec
	// Allocate resources for all containers at once, log-report for failed
	toAllocate := []*wekav1alpha1.WekaContainer{}
	for _, container := range r.containers {
		if unhealthy, _, _ := IsUnhealthy(ctx, container); unhealthy {
			continue
		}
		if container.Status.NodeAffinity == "" {
			continue
		}
		if container.Status.Allocations == nil {
			// Allocate resources
			toAllocate = append(toAllocate, container)
		}
	}

	if len(toAllocate) == 0 {
		// No containers to allocate resources for
		return nil
	}

	resourceAllocator, err := allocator.NewResourcesAllocator(ctx, r.getClient())
	if err != nil {
		return err
	}

	err = resourceAllocator.AllocateContainers(ctx, r.cluster, toAllocate)
	if err != nil {
		if failedAllocs, ok := err.(*allocator.FailedAllocations); ok {
			err = fmt.Errorf("failed to allocate resources for %d containers", len(*failedAllocs))
			logger.Error(err, "", "failedAllocs", failedAllocs)
			_ = r.RecordEvent(v1.EventTypeWarning, "FailedAllocations", err.Error())
			for _, alloc := range *failedAllocs {
				// we landed in some conflicting place, evicting for rescheduling
				_ = r.RecordEvent(v1.EventTypeWarning, "RemoveUnschedulable", fmt.Sprintf("Evicting container %s for rescheduling", alloc.Container.Name))
				patch := client.MergeFrom(alloc.Container.DeepCopy())
				alloc.Container.Spec.State = wekav1alpha1.ContainerStateDeleting
				if err := r.getClient().Patch(ctx, alloc.Container, patch); err != nil {
					logger.Error(err, "Failed to patch container state to deleting", "container", alloc.Container.Name)
				}
			}
		} else {
			_ = r.RecordEvent(v1.EventTypeWarning, "ResourcesAllocationError", err.Error())
			return err
		}
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) HasS3Containers() bool {
	cluster := r.cluster

	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		return false
	}
	if template.S3Containers == 0 {
		return false
	}

	for _, container := range r.containers {
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeS3 {
			return true
		}
	}
	return false
}

func (r *wekaClusterReconcilerLoop) HasNfsContainers() bool {
	return len(r.SelectNfsContainers(r.containers)) > 0
}

func (r *wekaClusterReconcilerLoop) MarkAsReady(ctx context.Context) error {
	wekaCluster := r.cluster

	if wekaCluster.Status.Status != wekav1alpha1.WekaClusterStatusReady {
		wekaCluster.Status.Status = wekav1alpha1.WekaClusterStatusReady
		wekaCluster.Status.TraceId = ""
		wekaCluster.Status.SpanID = ""
		return r.getClient().Status().Update(ctx, wekaCluster)
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) refreshContainersJoinIps(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containers := r.containers
	cluster := r.cluster

	_, err := services.ClustersJoinIps.JoinIpsAreValid(ctx, cluster.Name, cluster.Namespace)
	if err != nil {
		logger.Debug("Cannot get join ips", "msg", err.Error())
		err := services.ClustersJoinIps.RefreshJoinIps(ctx, containers, cluster)
		if err != nil {
			logger.Error(err, "Failed to refresh join ips")
			return err
		}
	}
	return nil
}

func tickCounter(counters *sync.Map, key string, value int64) {
	val, ok := counters.Load(key)
	if ok {
		ptr := val.(int64)
		ptr += value
		counters.Store(key, ptr)
	} else {
		counters.Store(key, value)
	}
}

func getCounter(counters *sync.Map, key string) int64 {
	val, ok := counters.Load(key)
	if ok {
		ptr := val.(int64)
		return ptr
	}
	return 0
}

func (r *wekaClusterReconcilerLoop) UpdateContainersCounters(ctx context.Context) error {
	cluster := r.cluster
	containers := r.containers
	roleCreatedCounts := &sync.Map{}
	roleActiveCounts := &sync.Map{}
	driveCreatedCounts := &sync.Map{}
	maxCpu := map[string]float64{}

	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		return errors.New("Failed to get template")
	}

	if cluster.Status.Stats == nil {
		cluster.Status.Stats = &wekav1alpha1.ClusterMetrics{}
	}

	// Idea to bubble up from containers, TODO: Actually use metrics and not accumulated status
	for _, container := range containers {
		// count Active containers
		if container.Status.Status == wekav1alpha1.Running && container.Status.ClusterContainerID != nil {
			tickCounter(roleActiveCounts, container.Spec.Mode, 1)
		}
		// count Created containers
		if container.Status.Status != wekav1alpha1.PodNotRunning {
			tickCounter(roleCreatedCounts, container.Spec.Mode, 1)
			if container.Spec.Mode == wekav1alpha1.WekaContainerModeDrive {
				tickCounter(driveCreatedCounts, container.Spec.Mode, int64(max(container.Spec.NumDrives, 1)))
			}
		}

		if container.Status.Stats == nil {
			continue
		}

		if container.Status.Stats.CpuUsage.GetValue() > 0.0 {
			if container.Status.Stats.CpuUsage.GetValue() > maxCpu[container.Spec.Mode] {
				maxCpu[container.Spec.Mode] = container.Status.Stats.CpuUsage.GetValue()
			}
		}
	}

	// calculate desired counts
	cluster.Status.Stats.Containers.Compute.Containers.Desired = wekav1alpha1.IntMetric(template.ComputeContainers)
	cluster.Status.Stats.Containers.Drive.Containers.Desired = wekav1alpha1.IntMetric(template.DriveContainers)
	if template.S3Containers != 0 {
		if cluster.Status.Stats.Containers.S3 == nil {
			cluster.Status.Stats.Containers.S3 = &wekav1alpha1.ContainerMetrics{}
		}
		cluster.Status.Stats.Containers.S3.Containers.Desired = wekav1alpha1.IntMetric(template.S3Containers)
	}
	if template.NfsContainers != 0 {
		if cluster.Status.Stats.Containers.Nfs == nil {
			cluster.Status.Stats.Containers.Nfs = &wekav1alpha1.ContainerMetrics{}
		}
		cluster.Status.Stats.Containers.Nfs.Containers.Desired = wekav1alpha1.IntMetric(template.NfsContainers)
	}

	cluster.Status.Stats.Drives.DriveCounters.Desired = wekav1alpha1.IntMetric(template.DriveContainers * template.NumDrives)

	// convert to new metrics accessor
	cluster.Status.Stats.Containers.Compute.Processes.Desired = wekav1alpha1.IntMetric(max(int64(template.ComputeCores), 1) * int64(template.ComputeContainers))
	cluster.Status.Stats.Containers.Drive.Processes.Desired = wekav1alpha1.IntMetric(max(int64(template.DriveCores), 1) * int64(template.DriveContainers))

	// propagate "created" counters
	cluster.Status.Stats.Containers.Compute.Containers.Created = wekav1alpha1.IntMetric(getCounter(roleCreatedCounts, wekav1alpha1.WekaContainerModeCompute))
	cluster.Status.Stats.Containers.Drive.Containers.Created = wekav1alpha1.IntMetric(getCounter(roleCreatedCounts, wekav1alpha1.WekaContainerModeDrive))

	// propagate "active" counters for these that not exposed explicitly in weka status --json
	if template.S3Containers != 0 {
		cluster.Status.Stats.Containers.S3.Containers.Active = wekav1alpha1.IntMetric(getCounter(roleActiveCounts, wekav1alpha1.WekaContainerModeS3))
	}
	if template.NfsContainers != 0 {
		cluster.Status.Stats.Containers.Nfs.Containers.Active = wekav1alpha1.IntMetric(getCounter(roleActiveCounts, wekav1alpha1.WekaContainerModeNfs))
	}

	// fill in utilization
	if maxCpu[wekav1alpha1.WekaContainerModeCompute] > 0 {
		cluster.Status.Stats.Containers.Compute.CpuUtilization = wekav1alpha1.NewFloatMetric(maxCpu[wekav1alpha1.WekaContainerModeCompute])
	}

	if maxCpu[wekav1alpha1.WekaContainerModeDrive] > 0 {
		cluster.Status.Stats.Containers.Drive.CpuUtilization = wekav1alpha1.NewFloatMetric(maxCpu[wekav1alpha1.WekaContainerModeDrive])
	}

	if maxCpu[wekav1alpha1.WekaContainerModeS3] > 0 {
		cluster.Status.Stats.Containers.S3.CpuUtilization = wekav1alpha1.NewFloatMetric(maxCpu[wekav1alpha1.WekaContainerModeS3])
	}

	// prepare printerColumns
	cluster.Status.PrinterColumns.ComputeContainers = wekav1alpha1.StringMetric(cluster.Status.Stats.Containers.Compute.Containers.String())
	cluster.Status.PrinterColumns.DriveContainers = wekav1alpha1.StringMetric(cluster.Status.Stats.Containers.Drive.Containers.String())
	cluster.Status.PrinterColumns.Drives = wekav1alpha1.StringMetric(cluster.Status.Stats.Drives.DriveCounters.String())

	return r.getClient().Status().Update(ctx, cluster)
}

func (r *wekaClusterReconcilerLoop) UpdateWekaStatusMetrics(ctx context.Context) error {
	cluster := r.cluster

	if cluster.IsTerminating() || r.cluster.Status.Status != wekav1alpha1.WekaClusterStatusReady {
		return nil
	}

	activeContainer := discovery.SelectActiveContainer(r.containers)

	wekaService := services.NewWekaService(r.ExecService, activeContainer)
	wekaStatus, err := wekaService.GetWekaStatus(ctx)
	if err != nil {
		return errors.Wrap(err, "Failed to get Weka status")
	}

	if cluster.Status.Stats == nil {
		cluster.Status.Stats = &wekav1alpha1.ClusterMetrics{}
	}

	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		return errors.New("Failed to get template")
	}

	cluster.Status.Stats.Containers.Compute.Processes.Active = wekav1alpha1.IntMetric(int64(wekaStatus.Containers.Computes.Active * template.ComputeCores))
	cluster.Status.Stats.Containers.Compute.Containers.Active = wekav1alpha1.IntMetric(int64(wekaStatus.Containers.Computes.Active))
	cluster.Status.Stats.Containers.Drive.Processes.Active = wekav1alpha1.IntMetric(int64(wekaStatus.Containers.Drives.Active * template.DriveCores))
	cluster.Status.Stats.Containers.Drive.Containers.Active = wekav1alpha1.IntMetric(int64(wekaStatus.Containers.Drives.Active))
	cluster.Status.Stats.Drives.DriveCounters.Active = wekav1alpha1.IntMetric(int64(wekaStatus.Drives.Active))

	cluster.Status.Stats.Containers.Compute.Processes.Created = wekav1alpha1.IntMetric(int64(wekaStatus.Containers.Computes.Total * template.ComputeCores))
	cluster.Status.Stats.Containers.Drive.Processes.Created = wekav1alpha1.IntMetric(int64(wekaStatus.Containers.Drives.Total * template.DriveCores))
	// TODO: might be incorrect with bad drives, and better to buble up from containers
	cluster.Status.Stats.Drives.DriveCounters.Created = wekav1alpha1.IntMetric(int64(wekaStatus.Drives.Total))
	//TODO: this should go via template builder and not direct dynamic access
	//if cluster.Spec.Dynamic.S3Containers != 0 {
	//TODO: S3 cant be implemented this way, should buble up from containers instead(for all)
	//}

	cluster.Status.Stats.IoStats.Throughput.Read = wekav1alpha1.IntMetric(int64(wekaStatus.Activity.SumBytesRead))
	cluster.Status.Stats.IoStats.Throughput.Write = wekav1alpha1.IntMetric(int64(wekaStatus.Activity.SumBytesWritten))
	cluster.Status.Stats.IoStats.Iops.Read = wekav1alpha1.IntMetric(int64(wekaStatus.Activity.NumReads))
	cluster.Status.Stats.IoStats.Iops.Write = wekav1alpha1.IntMetric(int64(wekaStatus.Activity.NumWrites))
	cluster.Status.Stats.IoStats.Iops.Metadata = wekav1alpha1.IntMetric(int64(wekaStatus.Activity.NumOps - wekaStatus.Activity.NumReads - wekaStatus.Activity.NumWrites))
	cluster.Status.Stats.IoStats.Iops.Total = wekav1alpha1.IntMetric(int64(wekaStatus.Activity.NumOps))
	cluster.Status.Stats.AlertsCount = wekav1alpha1.IntMetric(wekaStatus.ActiveAlertsCount)
	cluster.Status.Stats.ClusterStatus = wekav1alpha1.StringMetric(wekaStatus.Status)
	cluster.Status.Stats.ClusterStatus = wekav1alpha1.StringMetric(wekaStatus.Status)
	cluster.Status.Stats.NumFailures = map[string]wekav1alpha1.FloatMetric{} // resetting every time as some keys might go away
	for _, rebuildDetails := range wekaStatus.Rebuild.ProtectionState {
		cluster.Status.Stats.NumFailures[strconv.Itoa(rebuildDetails.NumFailures)] = wekav1alpha1.NewFloatMetric(rebuildDetails.Percent)
	}

	cluster.Status.Stats.Capacity.TotalBytes = wekav1alpha1.IntMetric(wekaStatus.Capacity.TotalBytes)
	cluster.Status.Stats.Capacity.UnavailableBytes = wekav1alpha1.IntMetric(wekaStatus.Capacity.UnavailableBytes)
	cluster.Status.Stats.Capacity.UnprovisionedBytes = wekav1alpha1.IntMetric(wekaStatus.Capacity.UnprovisionedBytes)
	cluster.Status.Stats.Capacity.HotSpareBytes = wekav1alpha1.IntMetric(wekaStatus.Capacity.HotSpareBytes)

	tpsRead := util2.HumanReadableThroughput(wekaStatus.Activity.SumBytesRead)
	tpsWrite := util2.HumanReadableThroughput(wekaStatus.Activity.SumBytesWritten)
	cluster.Status.PrinterColumns.Throughput = wekav1alpha1.StringMetric(fmt.Sprintf("%s/%s", tpsRead, tpsWrite))

	iopsRead := util2.HumanReadableIops(wekaStatus.Activity.NumReads)
	iopsWrite := util2.HumanReadableIops(wekaStatus.Activity.NumWrites)
	iopsOther := util2.HumanReadableIops(wekaStatus.Activity.NumOps - wekaStatus.Activity.NumReads - wekaStatus.Activity.NumWrites)
	cluster.Status.PrinterColumns.Iops = wekav1alpha1.StringMetric(fmt.Sprintf("%s/%s/%s", iopsRead, iopsWrite, iopsOther))
	cluster.Status.Stats.LastUpdate = metav1.NewTime(time.Now())

	if err := r.getClient().Status().Update(ctx, cluster); err != nil {
		return err
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) EnsureClusterMonitoringService(ctx context.Context) error {
	// TODO: Re-wrap as operation
	labels := map[string]string{
		"app":                "weka-cluster-monitoring",
		"weka.io/cluster-id": string(r.cluster.GetUID()),
	}

	annototations := map[string]string{
		// prometheus annotations
		"prometheus.io/scrape": "true",
		"prometheus.io/port":   "80",
		"prometheus.io/path":   "/metrics",
	}

	deployment := apps.Deployment{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "monitoring-" + r.cluster.Name,
			Namespace: r.cluster.Namespace,
			Labels:    labels,
		},
		Spec: apps.DeploymentSpec{
			Replicas: util2.Int32Ref(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":                "weka-cluster-monitoring",
					"weka.io/cluster-id": string(r.cluster.GetUID()),
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: ctrl.ObjectMeta{
					Labels:      labels,
					Annotations: annototations,
				},
				Spec: v1.PodSpec{
					ImagePullSecrets: []v1.LocalObjectReference{
						{Name: r.cluster.Spec.ImagePullSecret},
					},
					Tolerations:  util.ExpandTolerations([]v1.Toleration{}, r.cluster.Spec.Tolerations, r.cluster.Spec.RawTolerations),
					NodeSelector: r.cluster.Spec.NodeSelector, //TODO: Monitoring-specific node-selector
					Containers: []v1.Container{
						{
							Name:  "weka-cluster-metrics",
							Image: config.Config.Metrics.Clusters.Image,
							Ports: []v1.ContainerPort{
								{
									ContainerPort: 80,
								},
							},
							Command: []string{
								"/bin/sh",
								"-c",
								`echo 'server {
								listen 80;
								location / {
									root /data;
									autoindex on;
									default_type text/plain;
								}
							}' > /etc/nginx/conf.d/default.conf &&
							nginx -g 'daemon off;'`,
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "data",
									MountPath: "/data",
								},
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "data",
							VolumeSource: v1.VolumeSource{
								EmptyDir: &v1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}
	err := ctrl.SetControllerReference(r.cluster, &deployment, r.Manager.GetScheme())
	if err != nil {
		return err
	}

	upsert := func() error {
		err = r.getClient().Get(ctx, client.ObjectKey{Name: deployment.Name, Namespace: deployment.Namespace}, &deployment)
		if err == nil {
			// already exists, no need to stress api with creates
			return nil
		}

		err = r.getClient().Create(ctx, &deployment)
		if err != nil {
			if alreadyExists := client.IgnoreAlreadyExists(err); alreadyExists == nil {
				//fetch current deployment
				err = r.getClient().Get(ctx, client.ObjectKey{Name: deployment.Name, Namespace: deployment.Namespace}, &deployment)
				if err != nil {
					return err
				}
				return nil
			}
			return err
		}
		return nil
	}

	if err := upsert(); err != nil {
		return err
	}

	// we should have deployment at hand now
	kubeService := kubernetes.NewKubeService(r.getClient())
	// searching for own pods
	pods, err := kubeService.GetPodsSimple(ctx, r.cluster.Namespace, "", labels)
	if err != nil {
		return err
	}
	// find running pod
	var pod *v1.Pod
	for _, p := range pods {
		if p.Status.Phase == v1.PodRunning {
			pod = &p
			break
		}
	}
	if pod == nil {
		return lifecycle.NewWaitError(errors.New("No running monitoring pod found"))
	}

	exec, err := util2.NewExecInPodByName(r.RestClient, r.Manager.GetConfig(), pod, "weka-cluster-metrics")
	if err != nil {
		return err
	}
	data, err := resources.BuildClusterPrometheusMetrics(ctx, r.cluster)
	if err != nil {
		return err
	}
	// finally write a data
	// better data operation/labeling to come
	cmd := []string{
		"/bin/sh",
		"-ec",
		`cat <<EOF > /data/metrics.tmp
` + data + `
EOF
mv /data/metrics.tmp /data/metrics
`,
	}
	stdout, stderr, err := exec.ExecNamed(ctx, "WriteMetrics", cmd)
	if err != nil {
		return errors.Wrapf(err, "Failed to write metrics: %s\n%s", stderr.String(), stdout.String())
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) HasPostFormClusterScript() bool {
	return r.cluster.Spec.GetOverrides().PostFormClusterScript != ""
}

func (r *wekaClusterReconcilerLoop) RunPostFormClusterScript(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "RunPostFormClusterScript")
	defer end()

	activeContainer, err := discovery.SelectActiveContainerWithRole(ctx, r.containers, wekav1alpha1.WekaContainerModeDrive)
	if err != nil {
		return err
	}
	executor, err := r.ExecService.GetExecutor(ctx, activeContainer)
	if err != nil {
		return err
	}

	script := r.cluster.Spec.GetOverrides().PostFormClusterScript
	cmd := []string{
		"/bin/sh",
		"-ec",
		script, // TODO: Might need additional escaping
	}
	stdout, stderr, err := executor.ExecNamed(ctx, "PostFormClusterScript", cmd)
	if err != nil {
		return errors.Wrapf(err, "Failed to run post-form cluster script: %s\n%s", stderr.String(), stdout.String())
	}

	logger.Info("Post-form cluster script executed")
	return nil

}

func (r *wekaClusterReconcilerLoop) RecordEvent(eventtype string, reason string, message string) error {
	if r.cluster == nil {
		return fmt.Errorf("cluster is not set")
	}
	if eventtype == "" {
		normal := v1.EventTypeNormal
		eventtype = normal
	}

	r.Recorder.Event(r.cluster, eventtype, reason, message)
	return nil
}

func BuildMissingContainers(ctx context.Context, cluster *wekav1alpha1.WekaCluster, template allocator.ClusterTemplate, existingContainers []*wekav1alpha1.WekaContainer) ([]*wekav1alpha1.WekaContainer, error) {
	_, logger, end := instrumentation.GetLogSpan(ctx, "BuildMissingContainers")
	defer end()

	containers := make([]*wekav1alpha1.WekaContainer, 0)

	clusterReady := meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondClusterCreated)

	existingByRole := map[string]int{}
	totalByrole := map[string]int{}

	for _, container := range existingContainers {
		existingByRole[container.Spec.Mode]++
	}

	for _, role := range []string{"drive", "compute", "s3", "envoy", "nfs"} {
		var numContainers int

		if clusterReady {
			switch role {
			case "compute":
				numContainers = template.ComputeContainers
			case "drive":
				numContainers = template.DriveContainers
			case "s3":
				numContainers = template.S3Containers
			case "envoy":
				numContainers = template.S3Containers
			case "nfs":
				numContainers = template.NfsContainers
			}
		} else {
			switch role {
			case "compute":
				numContainers = util2.GetMinValue(template.ComputeContainers, config.Consts.FormClusterMaxComputeContainers)
			case "drive":
				numContainers = util2.GetMinValue(template.DriveContainers, config.Consts.FormClusterMaxDriveContainers)
			default:
				continue
			}
		}

		currentCount := 0
		for _, container := range existingContainers {
			if unhealthy, _, _ := IsUnhealthy(ctx, container); unhealthy {
				continue // we don't care why it's unhealthy, but if it is - we do not account for it and replacement will be scheduled
			}
			if container.Spec.Mode == role {
				currentCount++
			}
		}

		toCreateNum := numContainers - currentCount
		totalByrole[role] = existingByRole[role] + toCreateNum
		if role == "envoy" {
			numContainers = totalByrole["s3"]
		}

		for i := currentCount; i < numContainers; i++ {

			name := allocator.NewContainerName(role)
			logger.Info("Building missing container", "role", role, "name", name)

			container, err := factory.NewWekaContainerForWekaCluster(cluster, template, role, name)
			if err != nil {
				logger.Error(err, "Failed to build container", "role", role, "name", name)
				return nil, err
			}
			containers = append(containers, container)
		}
	}
	return containers, nil
}

func (r *wekaClusterReconcilerLoop) updateClusterStatusIfNotEquals(ctx context.Context, newStatus wekav1alpha1.WekaClusterStatusEnum) error {
	if r.cluster.Status.Status != newStatus {
		r.cluster.Status.Status = newStatus
		err := r.getClient().Status().Update(ctx, r.cluster)
		if err != nil {
			err := fmt.Errorf("failed to update cluster status: %w", err)
			return err
		}
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) EnsureCSILoginCredentials(ctx context.Context) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster
	secret := &v1.Secret{}
	err := r.getClient().Get(ctx, client.ObjectKey{
		Name:      cluster.GetCSISecretName(),
		Namespace: cluster.Namespace,
	}, secret)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
	}

	containers := discovery.SelectOperationalContainers(r.containers, 30, nil)
	endpoints := discovery.GetClusterEndpoints(ctx, containers, 30, cluster.Spec.CsiConfig)

	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		return errors.New("Failed to get template")
	}

	var nfsContainers []*wekav1alpha1.WekaContainer
	var nfsTargetIps []string
	nfsTargetIpsBytes := []byte{}
	endpointsBytes := []byte{}

	if template.NfsContainers != 0 {
		nfsContainers = discovery.SelectOperationalContainers(r.containers, 30, []string{wekav1alpha1.WekaContainerModeNfs})
		nfsTargetIps = discovery.GetClusterNfsTargetIps(ctx, nfsContainers)
	}

	if nfsTargetIps != nil && len(nfsTargetIps) > 0 && nfsTargetIps[0] != "" {
		nfsTargetIpsBytes = []byte(strings.Join(nfsTargetIps, ","))
	}

	endpointsBytes = []byte(strings.Join(endpoints, ","))

	if secret.Data == nil {
		clientSecret := services.NewCsiSecret(ctx, cluster, endpoints, nfsTargetIps)

		kubeService := kubernetes.NewKubeService(r.getClient())
		return kubeService.EnsureSecret(ctx, clientSecret, &kubernetes.K8sOwnerRef{
			Scheme: r.getClient().Scheme(),
			Obj:    cluster,
		})
	} else {
		if len(nfsTargetIpsBytes) != 0 {
			secret.Data["nfsTargetIps"] = nfsTargetIpsBytes
		}
		secret.Data["endpoints"] = endpointsBytes
		return r.getClient().Update(ctx, secret)
	}
}

type UpdatableClusterSpec struct {
	AdditionalMemory    wekav1alpha1.AdditionalMemory
	Tolerations         []string
	RawTolerations      []v1.Toleration
	DriversDistService  string
	ImagePullSecret     string
	Labels              *util2.HashableMap
	NodeSelector        *util2.HashableMap
	UpgradeForceReplace bool
	NetworkSelector     wekav1alpha1.NetworkSelector
}

func NewUpdatableClusterSpec(spec *wekav1alpha1.WekaClusterSpec, meta *metav1.ObjectMeta) *UpdatableClusterSpec {
	labels := util2.NewHashableMap(meta.Labels)
	nodeSelector := util2.NewHashableMap(spec.NodeSelector)

	return &UpdatableClusterSpec{
		AdditionalMemory:    spec.AdditionalMemory,
		Tolerations:         spec.Tolerations,
		RawTolerations:      spec.RawTolerations,
		DriversDistService:  spec.DriversDistService,
		ImagePullSecret:     spec.ImagePullSecret,
		Labels:              labels,
		NodeSelector:        nodeSelector,
		UpgradeForceReplace: spec.GetOverrides().UpgradeForceReplace,
		NetworkSelector:     spec.NetworkSelector,
	}
}
