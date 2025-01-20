package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/node_agent"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
)

const (
	PodStatePodNotRunning   = "PodNotRunning"
	PodStatePodRunning      = "PodRunning"
	WaitForDrivers          = "WaitForDrivers"
	ContainerStatusRunning  = "Running"
	ContainerStatusDegraded = "Degraded"
	Error                   = "Error"
	// for drivers-build and adhoc-op-with-container (sign-dives) container
	Completed = "Completed"
	Building  = "Building"
)

func NewContainerReconcileLoop(mgr ctrl.Manager, restClient rest.Interface) *containerReconcilerLoop {
	//TODO: We creating new client on every loop, we should reuse from reconciler, i.e pass it by reference
	config := mgr.GetConfig()
	kClient := mgr.GetClient()
	execService := exec.NewExecService(restClient, config)
	metricsService, err := kubernetes.NewKubeMetricsServiceFromManager(mgr)
	if err != nil {
		mgr.GetLogger().Error(err, "Failed to create metrics service")
	}
	return &containerReconcilerLoop{
		Client:         kClient,
		Scheme:         mgr.GetScheme(),
		KubeService:    kubernetes.NewKubeService(mgr.GetClient()),
		MetricsService: metricsService,
		ExecService:    execService,
		Manager:        mgr,
		RestClient:     restClient,
	}
}

// LockMap is a structure that holds a map of locks
type LockMap struct {
	locks sync.Map // Concurrent map to store the locks
}

// GetLock retrieves or creates a lock for a given identifier
func (lm *LockMap) GetLock(id string) *sync.Mutex {
	actual, _ := lm.locks.LoadOrStore(id, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

type containerReconcilerLoop struct {
	client.Client
	RestClient       rest.Interface
	Scheme           *runtime.Scheme
	KubeService      kubernetes.KubeService
	ExecService      exec.ExecService
	Manager          ctrl.Manager
	container        *weka.WekaContainer
	pod              *v1.Pod
	nodeAffinityLock LockMap
	node             *v1.Node
	MetricsService   kubernetes.KubeMetricsService
	// field used in cases when we can assume current container
	// is in deletion process or its node is not available
	clusterContainers []*weka.WekaContainer
	// values shared between steps
	activeMounts *int
}

func (r *containerReconcilerLoop) FetchContainer(ctx context.Context, req ctrl.Request) error {
	container := &weka.WekaContainer{}
	err := r.Get(ctx, req.NamespacedName, container)
	if err != nil {
		container = nil
		if !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	r.container = container
	return nil
}

func ContainerReconcileSteps(mgr ctrl.Manager, restClient rest.Interface, container *weka.WekaContainer) lifecycle.ReconciliationSteps {
	loop := NewContainerReconcileLoop(mgr, restClient)
	loop.container = container

	return lifecycle.ReconciliationSteps{
		Client:        loop.Client,
		StatusObject:  loop.container,
		ThrottlingMap: loop.container.Status.Timestamps,
		Conditions:    &loop.container.Status.Conditions,
		Steps: []lifecycle.Step{
			{Run: loop.GetNode},
			{
				Condition:  condition.CondRemovedFromS3Cluster,
				CondReason: "Deletion",
				Run:        loop.RemoveFromS3Cluster,
				Predicates: lifecycle.Predicates{
					container.IsMarkedForDeletion,
					lifecycle.IsNotFunc(loop.CanSkipRemoveFromS3Cluster),
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition:  condition.CondContainerDrivesDeactivated,
				CondReason: "Deletion",
				Run:        loop.DeactivateDrives,
				Predicates: lifecycle.Predicates{
					container.IsMarkedForDeletion,
					lifecycle.IsNotFunc(loop.CanSkipDeactivate),
					container.IsDriveContainer,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition:  condition.CondContainerDeactivated,
				CondReason: "Deletion",
				Run:        loop.DeactivateWekaContainer,
				Predicates: lifecycle.Predicates{
					container.IsMarkedForDeletion,
					lifecycle.IsNotFunc(loop.CanSkipDeactivate),
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition:  condition.CondContainerDrivesRemoved,
				CondReason: "Deletion",
				Run:        loop.RemoveDeactivatedContainersDrives,
				Predicates: lifecycle.Predicates{
					container.IsMarkedForDeletion,
					lifecycle.IsNotFunc(loop.CanSkipDeactivate),
					container.IsDriveContainer,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition:  condition.CondContainerRemoved,
				CondReason: "Deletion",
				Run:        loop.RemoveDeactivatedContainers,
				Predicates: lifecycle.Predicates{
					container.IsMarkedForDeletion,
					lifecycle.IsNotFunc(loop.CanSkipDeactivate),
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition:  condition.CondContainerDrivesResigned,
				CondReason: "Deletion",
				Run:        loop.ResignDrives,
				Predicates: lifecycle.Predicates{
					container.IsMarkedForDeletion,
					lifecycle.IsNotFunc(loop.CanSkipDeactivate),
					lifecycle.IsNotFunc(loop.CanSkipDrivesForceResign),
					container.IsDriveContainer,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.HandleDeletion,
				Predicates: lifecycle.Predicates{
					container.IsMarkedForDeletion,
					loop.CanProceedDeletion,
				},
				ContinueOnPredicatesFalse: true,
				FinishOnSuccess:           true,
			},
			{
				Run: loop.handleStatePaused,
				Predicates: lifecycle.Predicates{
					container.IsPaused,
				},
				ContinueOnPredicatesFalse: true,
				FinishOnSuccess:           true,
			},
			{
				Run: loop.getAndStoreActiveMounts,
				Predicates: lifecycle.Predicates{
					container.HasFrontend,
					loop.NodeIsSet,
				},
				ContinueOnPredicatesFalse: true,
			},
			{Run: loop.initState},
			{Run: loop.deleteIfNoNode},
			{Run: loop.ensureFinalizer},
			{Run: loop.ensureBootConfigMapInTargetNamespace},
			{Run: loop.refreshPod},
			{
				Run: loop.handlePodTermination,
				Predicates: lifecycle.Predicates{
					lifecycle.IsNotFunc(loop.PodNotSet),
					func() bool {
						return loop.pod.DeletionTimestamp != nil
					},
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.cleanupFinishedOneOff,
				Predicates: lifecycle.Predicates{
					loop.container.IsOneOff,
					loop.ResultsAreProcessed,
				},
				ContinueOnPredicatesFalse: true,
				FinishOnSuccess:           true,
			},
			{
				Run: loop.Noop,
				Predicates: lifecycle.Predicates{
					loop.container.IsOneOff,
					loop.ResultsAreProcessed,
				},
				FinishOnSuccess:           true,
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition:  condition.CondContainerImageUpdated,
				CondReason: "ImageUpdate",
				Run:        loop.handleImageUpdate,
				Predicates: lifecycle.Predicates{
					func() bool {
						return container.Status.LastAppliedImage != ""
					},
					loop.IsNotAlignedImage,
					lifecycle.IsNotFunc(loop.PodNotSet),
				},
				ContinueOnPredicatesFalse: true,
				SkipOwnConditionCheck:     true,
			},
			{
				Run: loop.EnsureDrivers,
				Predicates: lifecycle.Predicates{
					container.RequiresDrivers,
					loop.HasNodeAffinity, // if we dont have node set yet we can't load drivers, but we do want to load before creating pod if we have affinity
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.ensurePod,
				Predicates: lifecycle.Predicates{
					loop.PodNotSet,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.ensurePodNotRunningState,
				Predicates: lifecycle.Predicates{
					loop.PodNotRunning,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run:       loop.enforceNodeAffinity,
				Condition: condition.CondContainerAffinitySet,
				Predicates: lifecycle.Predicates{
					loop.container.MustHaveNodeAffinity,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.EnsureDrivers, //drivers might be off at this point if we had to wait for node affinity
				Predicates: lifecycle.Predicates{
					container.RequiresDrivers,
					loop.HasNodeAffinity, // if we dont have node set yet we can't load drivers, but we do want to load before creating pod if we have affinity
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.CleanupUnschedulable,
				Predicates: lifecycle.Predicates{
					func() bool {
						if loop.pod == nil {
							panic("pod is nil, it must be set at this point")
						}
						return loop.pod.Status.Phase == v1.PodPending
					},
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.cleanupFinished,
				Predicates: lifecycle.Predicates{
					func() bool {
						return loop.pod.Status.Phase == v1.PodSucceeded
					},
				},
				ContinueOnPredicatesFalse: true,
			},
			{Run: loop.WaitForRunning},

			{
				Run:       loop.WriteResources,
				Condition: condition.CondContainerResourcesWritten,
				Predicates: lifecycle.Predicates{
					container.IsAllocatable,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.updateDriversBuilderStatus,
				Predicates: lifecycle.Predicates{
					container.IsDriversBuilder,
					lifecycle.IsNotFunc(container.IsDistMode), // TODO: legacy "dist" mode is currently used both for building drivers and for distribution
					lifecycle.IsNotFunc(loop.ResultsAreProcessed),
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.updateAdhocOpStatus,
				Predicates: lifecycle.Predicates{
					container.IsAdhocOpContainer,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition: condition.CondResultsReceived,
				Run:       loop.fetchResults,
				Predicates: lifecycle.Predicates{
					loop.container.IsOneOff,
				},
				ContinueOnPredicatesFalse: true,
				SkipOwnConditionCheck:     false,
			},
			{
				Condition: condition.CondResultsProcessed,
				Run:       loop.processResults,
				Predicates: lifecycle.Predicates{
					loop.container.IsOneOff,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.reconcileManagementIP, // TODO: #shouldRefresh?
				Predicates: lifecycle.Predicates{
					lifecycle.IsEmptyString(container.Status.ManagementIP),
					container.IsBackend,
				},
				ContinueOnPredicatesFalse: true,
				OnFail:                    loop.setErrorStatus,
			},
			{
				Run: loop.reconcileWekaLocalStatus,
				Predicates: lifecycle.Predicates{
					container.IsWekaContainer,
				},
				ContinueOnPredicatesFalse: true,
				OnFail:                    loop.setErrorStatus,
			},
			{
				Run: loop.applyCurrentImage,
				Predicates: lifecycle.Predicates{
					loop.IsNotAlignedImage,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition: condition.CondJoinedCluster,
				Run:       loop.reconcileClusterStatus,
				Predicates: lifecycle.Predicates{
					container.ShouldJoinCluster,
				},
				ContinueOnPredicatesFalse: true,
				CondMessage:               "Container joined cluster",
			},
			{
				Condition:   condition.CondDrivesAdded,
				Run:         loop.EnsureDrives,
				CondMessage: fmt.Sprintf("Added %d drives", container.Spec.NumDrives),
				Predicates: lifecycle.Predicates{
					container.IsDriveContainer,
					func() bool {
						return loop.container.Spec.NumDrives > 0
					},
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition:   condition.CondJoinedS3Cluster,
				Run:         loop.JoinS3Cluster,
				CondMessage: "Joined s3 cluster",
				Predicates: lifecycle.Predicates{
					container.IsS3Container,
					container.HasJoinIps,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition:   condition.CondNfsInterfaceGroupsConfigured,
				Run:         loop.JoinNfsInterfaceGroups,
				CondMessage: "NFS interface groups configured",
				Predicates: lifecycle.Predicates{
					container.IsNfsContainer,
					container.HasJoinIps,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Name: "SetStatusMetrics",
				Run:  lifecycle.ForceNoError(loop.SetStatusMetrics),
				Predicates: lifecycle.Predicates{
					lifecycle.BoolValue(config.Config.Metrics.Containers.Enabled),
					func() bool {
						return slices.Contains(
							[]string{
								weka.WekaContainerModeCompute,
								weka.WekaContainerModeClient,
								weka.WekaContainerModeS3,
								weka.WekaContainerModeNfs,
								weka.WekaContainerModeDrive,
								// TODO: Expand to clients, introduce API-level(or not) HasManagement check
							}, container.Spec.Mode)
					},
				},
				Throttled:                 config.Config.Metrics.Containers.PollingRate,
				ContinueOnPredicatesFalse: true,
			},
			{
				Name: "RegisterContainerOnMetrics",
				Run:  lifecycle.ForceNoError(loop.RegisterContainerOnMetrics),
				Predicates: lifecycle.Predicates{
					lifecycle.BoolValue(config.Config.Metrics.Containers.Enabled),
					func() bool {
						return slices.Contains(
							[]string{
								weka.WekaContainerModeCompute,
								weka.WekaContainerModeClient,
								weka.WekaContainerModeS3,
								weka.WekaContainerModeNfs,
								weka.WekaContainerModeDrive,
								// TODO: Expand to clients, introduce API-level(or not) HasManagement check
							}, container.Spec.Mode)
					},
				},
				Throttled:                 config.Config.Metrics.Containers.PollingRate,
				ContinueOnPredicatesFalse: true,
			},
			{
				Name:                      "ReportOtelMetrics",
				Run:                       lifecycle.ForceNoError(loop.ReportOtelMetrics),
				Throttled:                 time.Minute,
				ContinueOnPredicatesFalse: true,
			},
		},
	}
}

func (r *containerReconcilerLoop) HandleDeletion(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	err := r.finalizeContainer(ctx)
	if err != nil {
		return err
	}
	controllerutil.RemoveFinalizer(r.container, WekaFinalizer)
	err = r.Update(ctx, r.container)
	if err != nil {
		logger.Error(err, "Error removing finalizer")
		return errors.Wrap(err, "Failed to remove finalizer")
	}
	return nil
}

func (r *containerReconcilerLoop) RemoveFromS3Cluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		return errors.New("Container ID is not set")
	}

	executeInContainer := r.container

	if !r.ContainerNodeIsAlive() {
		containers, err := r.getClusterContainers(ctx)
		if err != nil {
			return err
		}
		executeInContainer = discovery.SelectActiveContainer(containers)
	}

	logger.Info("Removing container from S3 cluster", "container_id", *containerId)

	wekaService := services.NewWekaService(r.ExecService, executeInContainer)
	return wekaService.RemoveFromS3Cluster(ctx, *containerId)
}

func (r *containerReconcilerLoop) DeactivateDrives(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		return errors.New("Container ID is not set")
	}

	executeInContainer := r.container

	if !r.ContainerNodeIsAlive() {
		containers, err := r.getClusterContainers(ctx)
		if err != nil {
			return err
		}
		executeInContainer = discovery.SelectActiveContainer(containers)
	}

	wekaService := services.NewWekaService(r.ExecService, executeInContainer)
	statusActive := "ACTIVE"
	statusInactive := "INACTIVE"

	drives, err := wekaService.ListContainerDrives(ctx, *containerId)
	if err != nil {
		return err
	}

	for _, drive := range drives {
		switch drive.Status {
		case statusActive:
			logger.Info("Deactivating drive", "drive_id", drive.Uuid)
			err = wekaService.DeactivateDrive(ctx, drive.Uuid)
			if err != nil {
				return err
			}
		case statusInactive:
			continue
		default:
			err := fmt.Errorf("drive has status '%s', wait for it to become 'INACTIVE'", drive.Status)
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) DeactivateWekaContainer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		return errors.New("Container ID is not set")
	}

	executeInContainer := r.container

	if !r.ContainerNodeIsAlive() {
		// TODO: temporary check caused by weka s3 container remove behavior
		if r.container.IsS3Container() {
			// before deacitvating S3 container, we need to make sure, that local s3 container is removed
			// if node is not available, we can't execute `weka local ps` operation
			err := errors.New("node is not available, can't check if local s3 container is running")
			return err
		}

		containers, err := r.getClusterContainers(ctx)
		if err != nil {
			return err
		}
		executeInContainer = discovery.SelectActiveContainer(containers)
	}

	wekaService := services.NewWekaService(r.ExecService, executeInContainer)

	// TODO: temporary check caused by weka s3 container remove behavior
	if r.container.IsS3Container() {
		// check that local s3 container does not exist anymore
		// if it does, wait for it to be removed
		localContainers, err := wekaService.ListLocalContainers(ctx)
		if err != nil {
			err = errors.Wrap(err, "Failed to list weka local containers")
			return err
		}

		logger.Debug("weka local ps", "containers", localContainers)

		for _, localContainer := range localContainers {
			if localContainer.Type == "s3" {
				err := errors.New("local s3 container still exists")
				return err
			}
		}
	}

	logger.Info("Deactivating container", "container_id", *containerId)

	return wekaService.DeactivateContainer(ctx, *containerId)
}

func (r *containerReconcilerLoop) RemoveDeactivatedContainersDrives(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		err := errors.New("Container ID is not set")
		return err
	}

	containers, err := r.getClusterContainers(ctx)
	if err != nil {
		return err
	}

	execInContainer := discovery.SelectActiveContainer(containers)
	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	drives, err := wekaService.ListContainerDrives(ctx, *containerId)
	if err != nil {
		return err
	}
	logger.Info("Removing drives for container", "container_id", *containerId, "drives", drives)

	var errs []error
	for _, drive := range drives {
		err := wekaService.RemoveDrive(ctx, drive.Uuid)
		if err != nil {
			errs = append(errs, err)
		} else {
			logger.Info("Drive removed", "drive_uuid", drive.Uuid, "container_id", *containerId)
		}
	}
	if len(errs) > 0 {
		err = fmt.Errorf("failed to remove drives for container %d: %v", *containerId, errs)
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) RemoveDeactivatedContainers(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containerId := r.container.Status.ClusterContainerID

	containers, err := r.getClusterContainers(ctx)
	if err != nil {
		return err
	}

	logger.Info("Removing container", "container_id", *containerId)

	execInContainer := discovery.SelectActiveContainer(containers)
	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	err = wekaService.RemoveContainer(ctx, *containerId)
	if err != nil {
		err = errors.Wrap(err, "Failed to remove container")
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) ResignDrives(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	if !r.ContainerNodeIsAlive() {
		err := fmt.Errorf("container node is not ready, cannot perform resign drives operation")
		return err
	}

	deactivatedContainer := r.container

	if deactivatedContainer.Status.Allocations == nil || len(deactivatedContainer.Status.Allocations.Drives) == 0 {
		logger.Info("No drives to force resign for container", "container_name", deactivatedContainer.Name)
		return nil
	}

	payload := weka.ForceResignDrivesPayload{
		NodeName:      deactivatedContainer.GetNodeAffinity(),
		DeviceSerials: deactivatedContainer.Status.Allocations.Drives,
	}
	emptyCallback := func(ctx context.Context) error { return nil }
	op := operations.NewResignDrivesOperation(
		r.Manager,
		&payload,
		deactivatedContainer,
		*deactivatedContainer.ToContainerDetails(),
		nil,
		emptyCallback,
	)

	err := operations.ExecuteOperation(ctx, op)
	return err
}

func (r *containerReconcilerLoop) handlePodTermination(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	node := r.node
	pod := r.pod
	container := r.container

	if container.HasFrontend() {
		// node drain or cordon detected
		if node.Spec.Unschedulable {
			logger.Info("Node is unschedulable, checking active mounts")

			ok, err := r.noActiveMountsRestriction(ctx)
			if err != nil {
				return err
			}
			if !ok {
				err := errors.New("Node is unschedulable and has active mounts")
				return err
			}
		}

		// upgrade detected
		if container.Spec.Image != container.Status.LastAppliedImage && container.Status.LastAppliedImage != "" {
			var wekaPodContainer v1.Container
			wekaPodContainer, err := r.getWekaPodContainer(pod)
			if err != nil {
				return err
			}

			if wekaPodContainer.Image != container.Spec.Image {
				logger.Info("Upgrade detected")
				err = r.runPreUpgradeSteps(ctx)
				if err != nil && errors.Is(err, &NoWekaFsDriverFound{}) {
					logger.Info("No wekafs driver found, skip prepare-upgrade")
				} else if err != nil {
					return err
				}
			}
		}
	}

	err := r.writeAllowStopInstruction(ctx, pod)
	if err != nil {
		logger.Error(err, "Error writing allow stop instruction")
		return err
	}

	// stop weka local if container has agent
	if r.container.HasAgent() {
		// check instruction in pod
		forceStop, err := r.checkAllowForceStopInstruction(ctx, pod)
		if err != nil {
			return err
		}

		logger.Debug("Stopping weka local", "force", forceStop)

		if forceStop {
			logger.Info("Force stop instruction found")
			err = r.runWekaLocalStop(ctx, pod, true)
		} else {
			err = r.runWekaLocalStop(ctx, pod, false)
		}

		if err != nil {
			logger.Error(err, "Error stopping weka local")
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) checkAllowForceStopInstruction(ctx context.Context, pod *v1.Pod) (bool, error) {
	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		return false, err
	}

	_, _, err = executor.ExecNamed(ctx, "CheckAllowForceStop", []string{"bash", "-ce", "test -f /tmp/.allow-force-stop"})
	if err != nil {
		return false, nil
	}
	// if file exists, we can force stop
	return true, nil
}

func (r *containerReconcilerLoop) runWekaLocalStop(ctx context.Context, pod *v1.Pod, force bool) error {
	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		return err
	}

	args := []string{"weka", "local", "stop"}

	// TODO: remove later - temporary workaround for weka clients
	// we need to use --force flag
	if force || r.container.HasFrontend() {
		args = append(args, "--force")
	} else {
		args = append(args, "-g")
	}

	_, stderr, err := executor.ExecNamed(ctx, "WekaLocalStop", args)
	// handle the case when weka-agent isn't running:
	// `error stopping weka local: No container names or types provided; applying action to all Weka containers on the server.\n\x00error: error: weka-agent isn't running. To start the agent, run 'service weka-agent start'\n\x00`
	if err != nil && strings.Contains(stderr.String(), "error: weka-agent isn't running") {
		return nil
	}
	// hanlde the case when there is no weka-container on the pod
	if err != nil && strings.Contains(err.Error(), "container not found") {
		return nil
	}
	if err != nil {
		err = fmt.Errorf("error stopping weka local: %s, %v", stderr.String(), err)
	}
	return err
}

func (r *containerReconcilerLoop) writeAllowForceStopInstruction(ctx context.Context, pod *v1.Pod) error {
	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		return err
	}

	_, _, err = executor.ExecNamed(ctx, "AllowForceStop", []string{"bash", "-ce", "touch /tmp/.allow-force-stop"})
	if err != nil {
		if !strings.Contains(err.Error(), "container not found") {
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) writeAllowStopInstruction(ctx context.Context, pod *v1.Pod) error {
	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		return err
	}

	_, _, err = executor.ExecNamed(ctx, "AllowStop", []string{"bash", "-ce", "touch /tmp/.allow-stop"})
	if err != nil {
		if !strings.Contains(err.Error(), "container not found") {
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) handleStatePaused(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	newStatus := strings.ToUpper(string(weka.ContainerStatePaused))
	if r.container.Status.Status != newStatus {
		err := r.stopForceAndEnsureNoPod(ctx)
		if err != nil {
			return err
		}

		logger.Info("Updating status", "from", r.container.Status.Status, "to", newStatus)
		r.container.Status.Status = newStatus

		if err := r.Status().Update(ctx, r.container); err != nil {
			err = errors.Wrap(err, "Failed to update status")
			return err
		}
	}

	logger.Info("Container is paused, skipping reconciliation")
	return nil
}

func (r *containerReconcilerLoop) initState(ctx context.Context) error {
	if r.container.Status.Conditions == nil {
		r.container.Status.Conditions = []metav1.Condition{}
	}
	return nil
}

// Implement the remaining methods (ensurePod, driversLoaded, reconcileManagementIP, etc.)
// by adapting the logic from the original container_controller.go file.

func (r *containerReconcilerLoop) ensureBootConfigMapInTargetNamespace(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureBootConfigMapInTargetNamespace")
	defer end()

	bundledConfigMap := &v1.ConfigMap{}
	podNamespace, err := util.GetPodNamespace()
	if err != nil {
		logger.Error(err, "Error getting pod namespace")
		return err
	}
	key := client.ObjectKey{Namespace: podNamespace, Name: bootScriptConfigName}
	if err := r.Get(ctx, key, bundledConfigMap); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Error(err, "Bundled config map not found")
			return err
		}
		logger.Error(err, "Error getting bundled config map")
		return err
	}

	bootScripts := &v1.ConfigMap{}
	err = r.Get(ctx, client.ObjectKey{Namespace: r.container.Namespace, Name: bootScriptConfigName}, bootScripts)
	if err != nil {
		if apierrors.IsNotFound(err) {
			bootScripts.Namespace = r.container.Namespace
			bootScripts.Name = bootScriptConfigName
			bootScripts.Data = bundledConfigMap.Data
			if err := r.Create(ctx, bootScripts); err != nil {
				if apierrors.IsAlreadyExists(err) {
					logger.Info("Boot scripts config map already exists in designated namespace")
				} else {
					logger.Error(err, "Error creating boot scripts config map")
				}
			}
			logger.Info("Created boot scripts config map in designated namespace")
		}
	}

	if !util.IsEqualConfigMapData(bootScripts, bundledConfigMap) {
		bootScripts.Data = bundledConfigMap.Data
		if err := r.Update(ctx, bootScripts); err != nil {
			logger.Error(err, "Error updating boot scripts config map")
			return err
		}
		logger.InfoWithStatus(codes.Ok, "Updated and reconciled boot scripts config map in designated namespace")

	}
	return nil
}

func (r *containerReconcilerLoop) refreshPod(ctx context.Context) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "refreshPod")
	defer end()

	pod := &v1.Pod{}
	key := client.ObjectKey{Name: r.container.Name, Namespace: r.container.Namespace}
	if err := r.Get(ctx, key, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	r.pod = pod
	return nil
}

func (r *containerReconcilerLoop) reconcileManagementIP(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container
	pod := r.pod

	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		return err
	}

	var getIpCmd string
	if container.Spec.Network.EthDevice != "" {
		if container.Spec.Ipv6 {
			getIpCmd = fmt.Sprintf("ip -6 addr show dev %s | grep 'inet6 ' | awk '{print $2}' | cut -d/ -f1", container.Spec.Network.EthDevice)
		} else {
			getIpCmd = fmt.Sprintf("ip addr show dev %s | grep 'inet ' | awk '{print $2}' | cut -d/ -f1", container.Spec.Network.EthDevice)
		}
	} else {
		if container.Spec.Ipv6 {
			getIpCmd = fmt.Sprintf("ip -6 addr show $(ip -6 route show default | awk '{print $5}' | head -n1) | grep 'inet6 ' | grep global | awk '{print $2}' | cut -d/ -f1")
		} else {
			getIpCmd = fmt.Sprintf("ip route show default | grep src | awk '/default/ {print $9}' | head -n1")
		}
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "GetManagementIpAddress", []string{"bash", "-ce", getIpCmd})
	if err != nil {
		logger.Error(err, "Error executing command", "stderr", stderr.String())
		return err
	}
	ipAddress := strings.TrimSpace(stdout.String())
	if container.Spec.Network.EthDevice == "" && ipAddress == "" {
		//TODO: support ipv6 in this case
		if container.Spec.Ipv6 {
			return fmt.Errorf("failed to get management IP, no fallback for ipv6")
		}
		// Compatible with Amazon Linux 2
		getIpCmd = "ip -4 addr show dev $(ip route show default | awk '{print $5}') | grep inet | awk '{print $2}' | cut -d/ -f1"
		stdout, stderr, err = executor.ExecNamed(ctx, "GetManagementIpAddress", []string{"bash", "-ce", getIpCmd})
		if err != nil {
			logger.Error(err, "Error executing command", "stderr", stderr.String())
			return err
		}
		ipAddress = strings.TrimSpace(stdout.String())
	}
	if ipAddress == "" {
		return fmt.Errorf("failed to get management IP")
	}
	logger.WithValues("management_ip", ipAddress).Info("Got management IP")
	if container.Status.ManagementIP != ipAddress {
		container.Status.ManagementIP = ipAddress
		if err := r.Status().Update(ctx, container); err != nil {
			logger.Error(err, "Error updating status")
			return err
		}
		return nil
	}
	return nil
}

func (r *containerReconcilerLoop) reconcileClusterStatus(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container
	pod := r.pod

	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return err
	}

	containerName := container.Spec.WekaContainerName

	showAgentPortCmd := `cat /opt/weka/k8s-runtime/vars/agent_port`
	cmd := fmt.Sprintf("weka local run wapi -H localhost:$(%s)/jrpc -W container-get-identity --container-name %s --json", showAgentPortCmd, containerName)
	if container.Spec.JoinIps != nil {
		cmd = fmt.Sprintf("wekaauthcli local run wapi -H 127.0.0.1:$(%s)/jrpc -W container-get-identity --container-name %s --json", showAgentPortCmd, containerName)
	}

	stdout, _, err := executor.ExecNamed(ctx, "WekaLocalContainerGetIdentity", []string{"bash", "-ce", cmd})
	if err != nil {
		return lifecycle.NewWaitError(err)
	}
	logger.Debug("Parsing weka local container-get-identity")
	response := resources.WekaLocalContainerGetIdentityResponse{}
	err = json.Unmarshal(stdout.Bytes(), &response)
	if err != nil {
		logger.Error(err, "Error parsing weka local status")
		return lifecycle.NewWaitError(err)
	}

	if response.Value == nil {
		return lifecycle.NewWaitError(errors.New("no value in response from weka local container-get-identity"))
	}
	if response.Value.ClusterId == "" || response.Value.ClusterId == "00000000-0000-0000-0000-000000000000" {
		return lifecycle.NewWaitError(errors.New("cluster not ready"))
	}

	container.Status.ClusterContainerID = &response.Value.ContainerId
	container.Status.ClusterID = response.Value.ClusterId
	logger.InfoWithStatus(
		codes.Ok,
		"Cluster created and its GUID and container ID are updated in WekaContainer status",
		"cluster_guid", response.Value.ClusterId,
		"container_id", response.Value.ContainerId,
	)
	if err := r.Status().Update(ctx, container); err != nil {
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) ensureFinalizer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	if ok := controllerutil.AddFinalizer(container, WekaFinalizer); !ok {
		return nil
	}

	logger.Info("Adding Finalizer for weka container")
	err := r.Update(ctx, container)
	if err != nil {
		logger.Error(err, "Failed to update wekaCluster with finalizer")
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) finalizeContainer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "finalizeContainer")
	defer end()

	container := r.container

	// first ensure no pod exists
	err := r.stopForceAndEnsureNoPod(ctx)
	if err != nil {
		return err
	}

	// then ensure we deleted container data
	err = r.cleanupPersistentDir(ctx)
	if err != nil {
		err = errors.Wrap(err, "Failed to cleanup persistent dir")
		return err
	}

	err = allocator.DeallocateContainer(ctx, container, r.Client)
	if err != nil {
		logger.Error(err, "Error deallocating container")
		return err
	} else {
		logger.Info("Container deallocated")
	}
	// deallocate from allocmap

	return nil
}

func (r *containerReconcilerLoop) cleanupPersistentDir(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "cleanupPersistentDir")
	defer end()

	container := r.container

	if container.Spec.Overrides != nil && container.Spec.Overrides.SkipCleanupPersistentDir {
		logger.Info("Skip cleanup persistent dir")
		return nil
	}

	if container.GetNodeAffinity() == "" {
		logger.Info("Container has no node affinity, skipping", "container", container.Name)
		return nil
	}

	if !container.HasPersistentStorage() {
		logger.Debug("Container has no persistent storage, skipping", "container", container.Name)
		return nil
	}

	nodeInfo, err := r.GetNodeInfo(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("node is deleted, no need for cleanup")
			return nil
		}
		// better to define specific error type for this, and helper function that would unwrap steps-execution exceptions
		// as an option, we should look into preserving original error without unwrapping. i.e abort+wait are encapsulated control cycles
		// but generic ReconcilationError wrapping error is sort of pointless
		if strings.Contains(err.Error(), "error reconciling object during phase GetNode: Node") && strings.Contains(err.Error(), "not found") {
			logger.Info("node is deleted, no need for cleanup")
			return nil
		}
		logger.Error(err, "Error getting node discovery")
		return err
	}

	if !r.ContainerNodeIsAlive() {
		err := fmt.Errorf("container node is not ready, cannot perform cleanup persistent dir operation")
		return err
	}

	payload := operations.CleanupPersistentDirPayload{
		NodeName:        container.GetNodeAffinity(),
		ContainerId:     string(container.UID),
		PersistencePath: nodeInfo.GetHostsideContainerPersistence(),
	}
	op := operations.NewCleanupPersistentDirOperation(
		r.Manager,
		&payload,
		container,
		*container.ToContainerDetails(),
	)
	err = operations.ExecuteOperation(ctx, op)
	return err
}

func (r *containerReconcilerLoop) GetNodeInfo(ctx context.Context) (*discovery.DiscoveryNodeInfo, error) {
	container := r.container
	nodeAffinity := container.GetNodeAffinity()
	if container.GetNodeAffinity() == "" {
		// HACK: Since we don't really know the node affinity, we will try to discover it by labels
		// Assuming, that supplied labels represent unique type of machines
		// this puts a requirement on user to separate machines by labels, which is common approach
		nodes, err := r.KubeService.GetNodes(ctx, container.Spec.NodeSelector)
		if err != nil {
			return nil, err
		}
		if len(nodes) == 0 {
			return nil, errors.New("no matching nodes found")
		}
		nodeAffinity = ""
		// if container has affinity, try to find node that satisfies it
		if r.container.Spec.Affinity != nil {
			for _, node := range nodes {
				if kubernetes.NodeSatisfiesAffinity(&node, r.container.Spec.Affinity) {
					nodeAffinity = weka.NodeName(node.Name)
					break
				}
			}
		} else {
			nodeAffinity = weka.NodeName(nodes[0].Name)
		}

		if nodeAffinity == "" {
			return nil, errors.New("no matching nodes found")
		}
	}
	discoverNodeOp := operations.NewDiscoverNodeOperation(
		r.Manager,
		r.RestClient,
		nodeAffinity,
		container,
		container.ToContainerDetails(),
	)
	err := operations.ExecuteOperation(ctx, discoverNodeOp)
	if err != nil {
		return nil, err
	}

	return discoverNodeOp.GetResult(), nil
}

func (r *containerReconcilerLoop) GetNode(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return nil
	}

	node, err := r.KubeService.GetNode(ctx, types.NodeName(nodeName))
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Node not found", "node", nodeName)
			return nil
		}
		err = errors.Wrap(err, "failed to get node")
		return err
	}
	r.node = node
	return nil
}

func (r *containerReconcilerLoop) ContainerNodeIsAlive() bool {
	node := r.node
	if node == nil {
		return false
	}

	// check if node is drained
	if node.Spec.Unschedulable {
		return false
	}

	// check if the node has a NodeReady condition set to True
	isNodeReady := false
	for _, condition := range node.Status.Conditions {
		if condition.Type == v1.NodeReady && condition.Status == v1.ConditionTrue {
			isNodeReady = true
			break
		}
	}
	return isNodeReady
}

func (r *containerReconcilerLoop) getClusterContainers(ctx context.Context) ([]*weka.WekaContainer, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getActiveContainers")
	defer end()

	if r.clusterContainers != nil {
		return r.clusterContainers, nil
	}

	ownerRefs := r.container.GetOwnerReferences()
	if len(ownerRefs) == 0 {
		return nil, errors.New("no owner references found")
	} else if len(ownerRefs) > 1 {
		return nil, errors.New("more than one owner reference found")
	}

	ownerUid := ownerRefs[0].UID
	logger.Debug("Owner UID", "uid", ownerUid)

	cluster, err := discovery.GetClusterByUID(ctx, r.Client, ownerUid)
	if err != nil {
		return nil, err
	}

	clusterContainers := discovery.GetClusterContainers(ctx, r.Manager.GetClient(), cluster, "")
	if len(clusterContainers) == 0 {
		err := fmt.Errorf("no containers found in cluster %s", cluster.Name)
		return nil, err
	}

	msg := fmt.Sprintf("Found %d containers in cluster", len(clusterContainers))
	logger.Debug(msg, "cluster", cluster.Name)
	r.clusterContainers = clusterContainers
	return clusterContainers, nil
}

func (r *containerReconcilerLoop) stopForceAndEnsureNoPod(ctx context.Context) error {
	//TODO: Can we search pods by ownership?

	container := r.container

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureNoPod")
	defer end()

	pod := &v1.Pod{}
	err := r.Get(ctx, client.ObjectKey{Name: container.Name, Namespace: container.Namespace}, pod)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		logger.Error(err, "Error getting pod")
		return err
	}

	// setting for forceful termination, as we are in container delete flow
	logger.Info("Deleting pod", "pod", pod.Name)
	err = r.writeAllowForceStopInstruction(ctx, pod)
	if err != nil {
		// do not return error, as we are deleting pod anyway
		logger.Error(err, "Error writing allow force stop instruction")
	}

	if r.ContainerNodeIsAlive() && pod.Status.Phase == v1.PodRunning && container.IsActive() {
		if r.container.HasAgent() {
			logger.Debug("Force-stopping weka local")
			err = r.runWekaLocalStop(ctx, pod, true)
			if err != nil {
				logger.Error(err, "Error force-stopping weka local")
				return err
			}
		}
	}

	err = r.Delete(ctx, pod)
	if err != nil {
		logger.Error(err, "Error deleting pod")
		return err
	}
	logger.AddEvent("Pod deleted")
	return lifecycle.NewWaitError(errors.New("Pod deleted, reconciling for retry"))
}

func (r *containerReconcilerLoop) ensurePod(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	nodeInfo := &discovery.DiscoveryNodeInfo{}
	if !container.IsDiscoveryContainer() {
		var err error
		nodeInfo, err = r.GetNodeInfo(ctx)
		if err != nil {
			return err
		}
	}

	image := container.Spec.Image

	if r.IsNotAlignedImage() {
		// do not create pod with spec image if we know in advance that we cannot upgrade
		canUpgrade, err := r.upgradeConditionsPass(ctx)
		if err != nil || !canUpgrade {
			logger.Info("Cannot upgrade to new image, using last applied", "image", image, "error", err)
			image = container.Status.LastAppliedImage
		}
	}

	// refresh container join ips (if there are any)
	if len(container.Spec.JoinIps) > 0 {
		ownerRef := container.GetOwnerReferences()
		if len(ownerRef) == 0 {
			return errors.New("no owner reference found")
		}
		owner := ownerRef[0]

		joinIps, _ := services.ClustersJoinIps.GetJoinIps(ctx, owner.Name, container.Namespace)
		if len(joinIps) > 0 {
			container.Spec.JoinIps = joinIps
		}
	}

	desiredPod, err := resources.NewPodFactory(container, nodeInfo).Create(ctx, &image)
	if err != nil {
		return errors.Wrap(err, "Failed to create pod spec")
	}

	if err := ctrl.SetControllerReference(container, desiredPod, r.Scheme); err != nil {
		return errors.Wrapf(err, "Error setting controller reference")
	}

	if err := r.Create(ctx, desiredPod); err != nil {
		return errors.Wrap(err, "Failed to create pod")
	}
	r.pod = desiredPod
	err = r.refreshPod(ctx)
	if err != nil {
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) CleanupUnschedulable(ctx context.Context) error {
	kubeService := r.KubeService

	pod := r.pod
	container := r.container

	unschedulable := false
	unschedulableSince := time.Time{}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == v1.PodScheduled && condition.Status == v1.ConditionFalse && condition.Reason == "Unschedulable" {
			unschedulable = true
			unschedulableSince = condition.LastTransitionTime.Time
		}
	}

	if !unschedulable {
		return nil // cleanin up only unschedulable
	}

	if pod.Spec.NodeName != "" {
		return nil // cleaning only such that scheduled by node affinity
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "CleanupIfNeeded")
	defer end()

	nodeName := container.GetNodeAffinity()
	if nodeName == "" {
		err := errors.New("Node affinity not set")
		return err
	}

	_, err := kubeService.GetNode(ctx, types.NodeName(nodeName))
	if !apierrors.IsNotFound(err) {
		return nil // node still exists, handling only not found node
	}

	// We are safe to delete clients after a configurable while
	// TODO: Make configurable, for now we delete after 5 minutes since downtime
	// relying onlastTransitionTime of Unschedulable condition
	rescheduleAfter := 30 * time.Second
	if time.Since(unschedulableSince) > rescheduleAfter {
		logger.Info("Deleting unschedulable pod")
		err := r.Delete(ctx, container)
		if err != nil {
			logger.Error(err, "Error deleting client container")
			return err
		}
		return errors.New("Pod is outdated and will be deleted")
	}
	return nil
}

func (r *containerReconcilerLoop) WaitForRunning(ctx context.Context) error {
	pod := r.pod

	if pod.Status.Phase == v1.PodRunning {
		return nil
	}

	return lifecycle.NewWaitError(errors.New("Pod is not running"))

}

func (r *containerReconcilerLoop) cleanupFinished(ctx context.Context) error {
	pod := r.pod

	if pod.Status.Phase != v1.PodSucceeded {
		// should we also check for Failed status?
		return nil
	}

	err := r.stopForceAndEnsureNoPod(ctx)
	if err != nil {
		return err
	}

	return lifecycle.NewWaitError(errors.New("Pod is finished and will be deleted"))
}

func (r *containerReconcilerLoop) updateStatusWaitForDrivers(ctx context.Context) error {
	if r.container.Status.Status != WaitForDrivers {
		r.container.Status.Status = WaitForDrivers
		err := r.Status().Update(ctx, r.container)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) EnsureDrivers(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	if r.node == nil {
		return errors.New("node not found")
	}

	details := r.container.ToContainerDetails()
	if r.container.Spec.DriversLoaderImage != "" {
		details.Image = r.container.Spec.DriversLoaderImage
	} else if r.IsNotAlignedImage() {
		// do not create pod with spec image if we know in advance that we cannot upgrade
		canUpgrade, err := r.upgradeConditionsPass(ctx)
		if err != nil || !canUpgrade {
			logger.Debug("Cannot upgrade to new image, using last applied", "image", details.Image, "error", err)
			details.Image = r.container.Status.LastAppliedImage
		}
	}

	if !operations.DriversLoaded(r.node, details.Image, r.container.HasFrontend()) {
		err := r.updateStatusWaitForDrivers(ctx)
		if err != nil {
			return err
		}
	} else {
		return nil
	}

	logger.Info("Loading drivers", "image", details.Image)

	driversLoader := operations.NewLoadDrivers(r.Manager, r.node, *details, r.container.Spec.DriversDistService, r.container.HasFrontend(), false)
	err := operations.ExecuteOperation(ctx, driversLoader)
	if err != nil {
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) driversLoaded(ctx context.Context) (bool, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "driversLoaded")
	defer end()

	if !r.container.RequiresDrivers() {
		return true, nil
	}

	pod := r.pod

	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return false, err
	}
	stdout, stderr, err := executor.ExecNamed(ctx, "CheckDriversLoaded", []string{"bash", "-ce", "cat /tmp/weka-drivers.log"})
	if err != nil {
		return false, fmt.Errorf("error checking drivers loaded: %s", stderr.String())
	}

	missingDriverName := strings.TrimSpace(stdout.String())

	if missingDriverName == "" {
		logger.InfoWithStatus(codes.Ok, "Drivers already loaded")
		return true, nil
	}

	logger.Info("Driver not loaded", "missing_driver", missingDriverName)
	return false, nil
}

func (r *containerReconcilerLoop) fetchResults(ctx context.Context) error {
	container := r.container

	if container.Status.ExecutionResult != nil {
		return nil
	}

	executor, err := r.ExecService.GetExecutor(ctx, container)
	if err != nil {
		return nil
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "FetchResults", []string{"cat", "/weka-runtime/results.json"})
	if err != nil {
		return fmt.Errorf("Error fetching results, stderr: %s", stderr.String())
	}

	result := stdout.String()
	if result == "" {
		return errors.New("Empty result")
	}

	// update container to set execution result on container object
	container.Status.ExecutionResult = &result
	err = r.Status().Update(ctx, container)
	if err != nil {
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) EnsureDrives(ctx context.Context) error {
	container := r.container
	pod := r.pod
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureDrives", "cluster_guid", container.Status.ClusterID, "container_id", container.Status.ClusterID)
	defer end()

	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		return err
	}

	wekaService := services.NewWekaService(r.ExecService, container)

	driveListoptions := services.DriveListOptions{
		ContainerId: container.Status.ClusterContainerID,
	}
	drivesAdded, err := wekaService.ListDrives(ctx, driveListoptions)
	if err != nil {
		return err
	}
	if len(drivesAdded) == container.Spec.NumDrives {
		logger.InfoWithStatus(codes.Ok, "All drives are already added")
		return nil
	}

	allowedMisses := r.container.Spec.NumDrives - len(drivesAdded)

	kDrives, err := r.getKernelDrives(ctx, executor)
	if err != nil {
		return err
	}

	logger.Debug("Kernel drives", "drives", kDrives)

	drivesAddedBySerial := make(map[string]bool)
	for _, drive := range drivesAdded {
		drivesAddedBySerial[drive.SerialNumber] = true
	}

	// TODO: Not validating part of added drives and trying all over
	for _, drive := range container.Status.Allocations.Drives {
		l := logger.WithValues("drive_name", drive)
		l.Info("Attempting to configure drive")

		if _, ok := kDrives[drive]; !ok {
			allowedMisses--
			if allowedMisses < 0 {
				return errors.New("Not enough drives found")
			}
		}

		if _, ok := drivesAddedBySerial[drive]; ok {
			err := fmt.Errorf("drive %s is already added", drive)
			l.Error(err, "")
			return err
		}

		if kDrives[drive].Partition == "" {
			err := fmt.Errorf("drive %v is not partitioned", kDrives[drive])
			l.Error(err, "Error configuring drive")
			return err
		}

		l = l.WithValues("partition", kDrives[drive].Partition)
		l.Info("Verifying drive signature")
		cmd := fmt.Sprintf("hexdump -v -e '1/1 \"%%.2x\"' -s 8 -n 16 %s", kDrives[drive].Partition)
		stdout, stderr, err := executor.ExecNamed(ctx, "GetPartitionSignature", []string{"bash", "-ce", cmd})
		if err != nil {
			// Force resign was here and needs new place. probably under wekaContainer delete, or tomsbtone delete, or claim delete
			return nil
		}

		if stdout.String() != "90f0090f90f0090f90f0090f90f0090f" {
			l.Info("Drive has Weka signature on it, verifying ownership")
			isCurrentCluster, err := r.driveIsOwnedByCurrentCluster(ctx, stdout.String())
			if err != nil {
				return err
			}

			exists, err := r.isExistingCluster(ctx, stdout.String())
			if err != nil {
				return err
			}
			if exists && !isCurrentCluster {
				return errors.New("Drive belongs to existing cluster (not current)")
			} else if isCurrentCluster {
				l.Info("Drive belongs to current cluster")
			} else {
				l.WithValues("another_cluster_guid", stdout.String()).Info("Drive belongs to non-existing cluster")
			}

			l.Info("Resigning drive")
			err = r.forceResignDrive(ctx, executor, kDrives[drive].DevicePath) // This changes UUID, effectively making claim obsolete
			if err != nil {
				return err
			}
		}

		l.Info("Adding drive into system")
		// TODO: We need to login here. Maybe handle it on wekaauthcli level?
		cmd = fmt.Sprintf("weka cluster drive add %d %s", *container.Status.ClusterContainerID, kDrives[drive].DevicePath)
		_, stderr, err = executor.ExecNamed(ctx, "WekaClusterDriveAdd", []string{"bash", "-ce", cmd})
		if err != nil {
			if !strings.Contains(stderr.String(), "Device is already in use") {
				l.WithValues("stderr", stderr.String(), "command", cmd).Error(err, "Error adding drive into system")
				return errors.Wrap(err, stderr.String())
			} else {
				l.Info("Drive already added into system")
			}
		} else {
			l.Info("Drive added into system", "drive", drive)
		}
	}

	logger.InfoWithStatus(codes.Ok, "Drives added")
	return nil
}

func (r *containerReconcilerLoop) getKernelDrives(ctx context.Context, executor util.Exec) (map[string]operations.DriveInfo, error) {
	stdout, _, err := executor.ExecNamed(ctx, "FetchKernelDrives",
		[]string{"bash", "-ce", "cat /opt/weka/k8s-runtime/drives.json"})
	if err != nil {
		return nil, err
	}
	var drives []operations.DriveInfo
	err = json.Unmarshal(stdout.Bytes(), &drives)
	if err != nil {
		return nil, err
	}
	serialIdMap := make(map[string]operations.DriveInfo)
	for _, drive := range drives {
		serialIdMap[drive.SerialId] = drive
	}

	return serialIdMap, nil
}

func (r *containerReconcilerLoop) initialDriveSign(ctx context.Context, executor util.Exec, drive string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "initialDriveSign")
	defer end()

	logger.Info("Signing cloud drive", "drive", drive)
	cmd := fmt.Sprintf("weka local exec -- /weka/tools/weka_sign_drive %s", drive) // no-force and claims should keep us safe
	_, stderr, err := executor.ExecNamed(ctx, "WekaSignDrive", []string{"bash", "-ce", cmd})
	if err != nil {
		logger.Error(err, "Error pre-signing drive", "drive", drive, "stderr", stderr.String())
		return err
	}
	logger.InfoWithStatus(codes.Ok, "Cloud drives signed")
	return nil
}

func (r *containerReconcilerLoop) isDrivePresigned(ctx context.Context, executor util.Exec, drive string) (bool, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	stdout, stderr, err := executor.ExecNamed(ctx, "CheckDriveIsPresigned", []string{"bash", "-ce", "blkid -s PART_ENTRY_TYPE -o value -p " + getSignatureDevice(drive)})
	if err != nil {
		logger.Error(err, "Error checking if drive is presigned", "drive", drive, "stderr", stderr.String(), "stdout", stdout.String())
		return false, errors.Wrap(err, stderr.String())
	}
	const WEKA_SIGNATURE = "993ec906-b4e2-11e7-a205-a0a8cd3ea1de"
	return strings.TrimSpace(stdout.String()) == WEKA_SIGNATURE, nil
}

func getSignatureDevice(drive string) string {
	driveSignTarget := fmt.Sprintf("%s1", drive)
	if strings.Contains(drive, "/dev/disk/by-path/pci-") {
		return fmt.Sprintf("%s-part1", drive)
	}
	if strings.Contains(drive, "nvme") {
		return fmt.Sprintf("%sp1", drive)
	}
	return driveSignTarget
}

func (r *containerReconcilerLoop) forceResignDrive(ctx context.Context, executor util.Exec, drive string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "forceResignDrive")
	defer end()

	logger.Info("Resigning drive with --force")
	cmd := fmt.Sprintf("weka local exec -- /weka/tools/weka_sign_drive --force %s", drive)
	_, stderr, err := executor.ExecNamed(ctx, "WekaSignDrive", []string{"bash", "-ce", cmd})
	if err != nil {
		logger.Error(err, "Error signing drive", "drive", drive, "stderr", stderr.String())
	}
	return err
}

func (r *containerReconcilerLoop) driveIsOwnedByCurrentCluster(ctx context.Context, guid string) (bool, error) {
	currentClusterId := r.container.Status.ClusterID
	if currentClusterId == "" {
		return false, errors.New("cluster id not set")
	}

	stripped := strings.ReplaceAll(currentClusterId, "-", "")
	return stripped == guid, nil
}

func (r *containerReconcilerLoop) isExistingCluster(ctx context.Context, guid string) (bool, error) {
	// TODO: Query by status?
	// TODO: Cache?
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "isExistingCluster")
	defer end()

	logger.WithValues("cluster_guid", guid).Info("Verifying for existing cluster")

	clusters, err := discovery.GetAllClusters(ctx, r.Client)
	if err != nil {
		return false, err
	}
	for _, cluster := range clusters {
		// strip `-` from saved cluster name
		stripped := strings.ReplaceAll(cluster.Status.ClusterID, "-", "")
		if stripped == guid {
			logger.InfoWithStatus(codes.Ok, "Cluster found")
			return true, nil
		}
	}
	logger.InfoWithStatus(codes.Ok, "Cluster not found")
	return false, nil
}

func (r *containerReconcilerLoop) JoinS3Cluster(ctx context.Context) error {
	wekaService := services.NewWekaService(r.ExecService, r.container)
	return wekaService.JoinS3Cluster(ctx, *r.container.Status.ClusterContainerID)
}

func (r *containerReconcilerLoop) JoinNfsInterfaceGroups(ctx context.Context) error {
	wekaService := services.NewWekaService(r.ExecService, r.container)
	err := wekaService.JoinNfsInterfaceGroups(ctx, *r.container.Status.ClusterContainerID)
	if !errors.As(err, &services.NfsInterfaceGroupAlreadyJoined{}) {
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) getAndStoreActiveMounts(ctx context.Context) error {
	activeMounts, err := r.getActiveMounts(ctx)
	if err != nil && errors.Is(err, &NoWekaFsDriverFound{}) {
		// if no weka fs driver found, we can assume that there are no active mounts
		val := 0
		r.activeMounts = &val
		return nil
	}
	if err != nil {
		err = fmt.Errorf("error getting active mounts: %w", err)
		return err
	}
	r.activeMounts = activeMounts
	return nil
}

func (r *containerReconcilerLoop) getActiveMounts(ctx context.Context) (*int, error) {
	pods, err := r.getNodeAgentPods(ctx)
	if err != nil {
		return nil, err
	}
	if len(pods) == 0 {
		err := errors.New("no node agent pods found")
		return nil, err
	}

	pod := pods[0]

	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		err = errors.Wrap(err, "error getting node agent token")
		return nil, err
	}

	url := "http://" + pod.Status.PodIP + ":8090/getActiveMounts"

	resp, err := util.SendGetRequest(ctx, url, util.RequestOptions{AuthHeader: "Token " + token})
	if err != nil {
		err = errors.Wrap(err, "error sending getActiveMountsget request")
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return nil, &NoWekaFsDriverFound{}
		}

		err := errors.New("getActiveMounts request failed")
		return nil, err
	}

	var activeMountsResp struct {
		ActiveMounts int `json:"active_mounts"`
	}
	err = json.NewDecoder(resp.Body).Decode(&activeMountsResp)
	if err != nil {
		err = errors.Wrap(err, "error decoding response")
		return nil, err
	}

	return &activeMountsResp.ActiveMounts, nil
}

func (r *containerReconcilerLoop) upgradeConditionsPass(ctx context.Context) (bool, error) {
	// Necessary conditions for FE upgrade:
	// 1. all FE containers on single host should have same image
	// 2. check active mounts == 0
	if !r.container.HasFrontend() {
		return true, nil
	}

	if r.container.Status.LastAppliedImage == "" {
		// first time, no image to compare
		return true, nil
	}

	// skip all checks if hot upgrade is allowed
	if r.container.Spec.AllowHotUpgrade {
		return true, nil
	}

	ok, err := r.noActiveMountsRestriction(ctx)
	if !ok || err != nil {
		return false, err
	}

	nodeName := r.container.GetNodeAffinity()
	// get all frontend pods on same node
	pods, err := r.getFrontendPodsOnNode(ctx, string(nodeName))
	if err != nil {
		return false, err
	}

	// check if all pods have same image
	for _, pod := range pods {
		for _, podContainer := range pod.Spec.Containers {
			if r.pod != nil && pod.UID == r.pod.UID {
				// skip self
				continue
			}
			if podContainer.Name == "weka-container" {
				if podContainer.Image != r.container.Spec.Image {
					err := fmt.Errorf("pod %s on same node %s has different image %s", pod.Name, nodeName, podContainer.Image)
					return false, err
				}
			}
		}
	}
	return true, nil
}

func (r *containerReconcilerLoop) noActiveMountsRestriction(ctx context.Context) (bool, error) {
	// do not check active mounts for s3 containers
	if r.container.IsS3Container() {
		return true, nil
	}

	// NOTE: active mounts are fetched from node agent pod as the separate step in the flow
	activeMounts := r.activeMounts

	if activeMounts != nil && *activeMounts != 0 {
		err := fmt.Errorf("active mounts: %d", *activeMounts)
		return false, err
	}

	return true, nil
}

func (r *containerReconcilerLoop) handleImageUpdate(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "handleImageUpdate")
	defer end()

	container := r.container
	pod := r.pod

	upgradeType := container.Spec.UpgradePolicyType
	if upgradeType == "" {
		upgradeType = weka.UpgradePolicyTypeManual
	}

	if container.Spec.Image != container.Status.LastAppliedImage {
		canUpgrade, err := r.upgradeConditionsPass(ctx)
		if err != nil || !canUpgrade {
			err := fmt.Errorf("cannot upgrade: %w", err)

			// if we are in all-at-once upgrade mode, check if we already
			// have CondContainerImageUpdated set to false with the same reason
			// In this case, consider this as expected error
			if container.Spec.UpgradePolicyType == weka.UpgradePolicyTypeAllAtOnce {
				cond := meta.FindStatusCondition(container.Status.Conditions, condition.CondContainerImageUpdated)
				if cond != nil && cond.Status == metav1.ConditionFalse && cond.Message == err.Error() {
					return lifecycle.NewExpectedError(err)
				}
			}

			return err
		}

		var wekaPodContainer v1.Container
		wekaPodContainer, err = r.getWekaPodContainer(pod)
		if err != nil {
			return err
		}

		if wekaPodContainer.Image != container.Spec.Image {
			if container.HasFrontend() {
				err = r.runPreUpgradeSteps(ctx)
				if err != nil && errors.Is(err, &NoWekaFsDriverFound{}) {
					logger.Info("No wekafs driver found, force terminating pod")
					err := r.writeAllowForceStopInstruction(ctx, pod)
					if err != nil {
						logger.Error(err, "Error writing allow force stop instruction")
						return err
					}
					return r.Delete(ctx, pod)
				}
				if err != nil {
					return err
				}
			}
			if container.Spec.Mode == weka.WekaContainerModeClient && container.Spec.UpgradePolicyType == weka.UpgradePolicyTypeManual {
				// leaving client delete operation to user and we will apply lastappliedimage if pod got restarted
				return nil
			}

			err := r.writeAllowStopInstruction(ctx, pod)
			if err != nil {
				logger.Error(err, "Error writing allow stop instruction")
				return err
			}

			logger.Info("Deleting pod to apply new image")
			// delete pod
			err = r.Delete(ctx, pod)
			if err != nil {
				return err
			}

			return nil
		}

		if pod.GetDeletionTimestamp() != nil {
			logger.Info("Pod is being deleted, waiting")
			return errors.New("Pod is being deleted, waiting")
		}
	}
	return nil
}

func (r *containerReconcilerLoop) runPreUpgradeSteps(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "runPreUpgradeSteps")
	defer end()

	container := r.container
	pod := r.pod

	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		return err
	}

	logger.Info("Running prepare-upgrade")
	err = r.runPrepareUpgrade(ctx, executor)
	if err != nil {
		return err
	}

	if !container.Spec.AllowHotUpgrade {
		logger.Debug("Hot upgrade is not enabled, force-stopping weka local")
		err := r.runWekaLocalStop(ctx, pod, true)
		if err != nil {
			return err
		}

		logger.Debug("Removing wekafsio and wekafsgw drivers")
		_, stderr, err := executor.ExecNamed(ctx, "RemoveWekaFsDrivers", []string{"bash", "-ce", "rmmod wekafsio && rmmod wekafsgw"})
		if err != nil {
			err = fmt.Errorf("error removing wekafsio and wekafsgw drivers: %w, stderr: %s", err, stderr.String())
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) runPrepareUpgrade(ctx context.Context, executor util.Exec) error {
	_, stderr, err := executor.ExecNamed(ctx, "PrepareForUpgrade", []string{"bash", "-ce", "echo prepare-upgrade > /proc/wekafs/interface"})
	if err != nil && strings.Contains(stderr.String(), "No such file or directory") {
		err = &NoWekaFsDriverFound{}
		return err
	}
	if err != nil {
		err = fmt.Errorf("error running prepare-upgrade: %w, stderr: %s", err, stderr.String())
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) getWekaPodContainer(pod *v1.Pod) (v1.Container, error) {
	for _, podContainer := range pod.Spec.Containers {
		if podContainer.Name == "weka-container" {
			return podContainer, nil
		}
	}
	return v1.Container{}, errors.New("Weka container not found in pod")
}

func (r *containerReconcilerLoop) PodNotSet() bool {
	return r.pod == nil
}

func (r *containerReconcilerLoop) NodeIsSet() bool {
	return r.node != nil
}

func (r *containerReconcilerLoop) getFailureDomain(ctx context.Context) *string {
	fdLabel := r.container.Spec.FailureDomainLabel
	if fdLabel == nil || *fdLabel == "" {
		return nil
	}

	if fd, ok := r.node.Labels[*fdLabel]; ok {
		return &fd
	}
	return nil
}

func (r *containerReconcilerLoop) selfUpdateAllocations(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "SelfUpdateAllocations")
	defer end()

	container := r.container
	sleepBetween := config.Consts.ContainerUpdateAllocationsSleep

	cs, err := allocator.NewConfigMapStore(ctx, r.Client)
	if err != nil {
		return err
	}

	allAllocations, err := cs.GetAllocations(ctx)
	if err != nil {
		return lifecycle.NewWaitErrorWithDuration(errors.New("allocations are not set yet"), sleepBetween)
	}

	owner := container.GetOwnerReferences()
	nodeName := container.GetNodeAffinity()
	nodeAlloc, ok := allAllocations.NodeMap[nodeName]
	if !ok {
		return lifecycle.NewWaitErrorWithDuration(errors.New("node allocations are not set yet"), sleepBetween)
	}

	allocOwner := allocator.Owner{
		OwnerCluster: allocator.OwnerCluster{
			ClusterName: owner[0].Name,
			Namespace:   container.Namespace,
		},
		Container: container.Name,
		Role:      container.Spec.Mode,
	}

	allocatedDrives, ok := nodeAlloc.Drives[allocOwner]
	if !ok && container.IsDriveContainer() {
		return lifecycle.NewWaitErrorWithDuration(fmt.Errorf("no drives allocated for owner %v", allocOwner), sleepBetween)
	}

	currentRanges, ok := nodeAlloc.AllocatedRanges[allocOwner]
	if !ok {
		return lifecycle.NewWaitErrorWithDuration(fmt.Errorf("no ranges allocated for owner %v", allocOwner), sleepBetween)
	}
	wekaPort := currentRanges["weka"].Base
	agentPort := currentRanges["agent"].Base

	failureDomain := r.getFailureDomain(ctx)

	allocations := &weka.ContainerAllocations{
		Drives:        allocatedDrives,
		WekaPort:      wekaPort,
		AgentPort:     agentPort,
		FailureDomain: failureDomain,
	}
	logger.Info("Updating container with allocations", "allocations", allocations)

	container.Status.Allocations = allocations

	err = r.Status().Update(ctx, container)
	if err != nil {
		err = fmt.Errorf("cannot update container status with allocations: %w", err)
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) WriteResources(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "WriteResources")
	defer end()

	container := r.container
	if r.container.Status.Allocations == nil {
		err := r.selfUpdateAllocations(ctx)
		if err != nil {
			return err
		}
	}

	executor, err := r.ExecService.GetExecutor(ctx, container)
	if err != nil {
		return err
	}

	_, _, err = executor.ExecNamed(ctx, "CheckPersistencyConfigured", []string{"bash", "-ce", "test -f /opt/weka/k8s-runtime/persistency-configured"})
	if err != nil {
		err = errors.New("Persistency is not yet configured")
		return lifecycle.NewWaitError(err)
	}

	resourcesJson, err := json.Marshal(r.container.Status.Allocations)
	if err != nil {
		return err
	}
	//TODO: Safer way of writing? as might be corrupted bash string

	logger.Info("writing resources", "json", string(resourcesJson))
	stdout, stderr, err := executor.ExecNamed(ctx, "WriteResources", []string{"bash", "-ce", fmt.Sprintf(`
mkdir -p /opt/weka/k8s-runtime
echo '%s' > /opt/weka/k8s-runtime/resources.json
`, string(resourcesJson))})
	if err != nil {
		logger.Error(err, "Error writing resources", "stderr", stderr.String(), "stdout", stdout.String())
	}
	return err
}

func (r *containerReconcilerLoop) enforceNodeAffinity(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()
	node := r.pod.Spec.NodeName

	if node == "" {
		return lifecycle.NewWaitError(errors.New("pod is not assigned to node"))
	}

	if !r.container.Spec.NoAffinityConstraints {
		lockname := fmt.Sprintf("%s-%s", node, r.container.Spec.Mode)
		lock := r.nodeAffinityLock.GetLock(lockname)
		lock.Lock()
		defer lock.Unlock()

		var pods []v1.Pod
		var err error
		if !r.container.IsProtocolContainer() {
			pods, err = r.KubeService.GetPodsSimple(ctx, r.container.GetNamespace(), node, r.container.GetLabels())
			if err != nil {
				return err
			}
		} else {
			pods, err = r.getFrontendPodsOnNode(ctx, node)
			if err != nil {
				return err
			}
		}

		for _, pod := range pods {
			if pod.UID == r.pod.UID {
				continue // that's us, skipping
			}
			owner := pod.GetOwnerReferences()
			if len(owner) == 0 {
				continue // not owned pod, no idea what is it, but bypassing
			}

			var ownerContainer weka.WekaContainer
			err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: owner[0].Name}, &ownerContainer)
			if err != nil {
				return err
			}
			if ownerContainer.Status.NodeAffinity != "" {
				deleteErr := r.stopForceAndEnsureNoPod(ctx)
				if deleteErr != nil {
					return deleteErr
				} else {
					return lifecycle.NewWaitError(errors.New("scheduling race, deleting current container"))
				}
			}
		}
		// no one else is using this node, we can safely set it
	}
	r.container.Status.NodeAffinity = weka.NodeName(node)
	logger.Info("binding to node", "node", node, "container_name", r.container.Name)
	return r.Status().Update(ctx, r.container)
}

func (r *containerReconcilerLoop) getFrontendPodsOnNode(ctx context.Context, nodeName string) ([]v1.Pod, error) {
	return r.KubeService.GetPods(ctx, kubernetes.GetPodsOptions{
		Node: nodeName,
		LabelsIn: map[string][]string{
			// NOTE: Clients will not have affinity set, it's a small gap of race of s3 schedule on top of client
			// There is also a possible gap of deploying clients on top of S3
			// But since we do want to allow multiple clients to multiple clusters it becomes much complex
			// So  for now mostly solving case of scheduling of protocol on top of clients, and protocol on top of another protocol
			domain.WekaLabelMode: domain.ContainerModesWithFrontend,
		},
	})
}

func (r *containerReconcilerLoop) handleWekaLocalPsResponse(ctx context.Context, stdout []byte, psErr error) (response []resources.WekaLocalPs, err error) {
	if psErr != nil {
		return nil, psErr
	}

	container := r.container

	err = json.Unmarshal(stdout, &response)
	if err != nil {
		return
	}

	if len(response) == 0 {
		err = errors.New("Expected at least one container to be present, none found")
		return
	}

	found := false
	for _, c := range response {
		if c.Name == container.Spec.WekaContainerName {
			found = true
			break
		} else if c.Name == "envoy" && container.IsEnvoy() {
			found = true
			break
		}
	}

	if !found {
		err = errors.New("weka container not found")
		return
	}
	return
}

func (r *containerReconcilerLoop) reconcileWekaLocalStatus(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container
	pod := r.pod

	wekaLocalPsTimeout := 10 * time.Second
	executor, err := util.NewExecInPodWithTimeout(r.RestClient, r.Manager.GetConfig(), pod, &wekaLocalPsTimeout)
	if err != nil {
		return err
	}

	statusCommand := "weka local ps -J"
	stdout, stderr, err := executor.ExecNamed(ctx, "WekaLocalPs", []string{"bash", "-ce", statusCommand})

	response, err := r.handleWekaLocalPsResponse(ctx, stdout.Bytes(), err)
	if err != nil {
		logger.Error(err, "Error getting weka local ps", "stderr", stderr.String())

		// TODO: Validate agent-specific errors, but should not be very important
		// check if drivers should be force-reloaded
		loaded, driversErr := r.driversLoaded(ctx)
		if driversErr != nil {
			return driversErr
		}

		if !loaded {
			err = r.updateStatusWaitForDrivers(ctx)
			if err != nil {
				return err
			}

			details := r.container.ToContainerDetails()
			driversLoader := operations.NewLoadDrivers(r.Manager, r.node, *details, r.container.Spec.DriversDistService, r.container.HasFrontend(), true)
			loaderErr := operations.ExecuteOperation(ctx, driversLoader)
			if loaderErr != nil {
				err := fmt.Errorf("drivers are not loaded: %v; %v", driversErr, loaderErr)
				return lifecycle.NewWaitError(err)
			}

			err = fmt.Errorf("weka local ps failed: %v, stderr: %s", err, stderr.String())
			return lifecycle.NewWaitError(err)
		}
		return lifecycle.NewWaitError(err)
	}

	status := response[0].RunStatus
	if container.Status.Status != status {
		logger.Info("Updating status", "from", container.Status.Status, "to", status)
		container.Status.Status = status
		container.Status.Message = ""
		if err := r.Status().Update(ctx, container); err != nil {
			return err
		}
		logger.WithValues("status", status).Info("Status updated")
		return nil
	}
	return nil
}

func (r *containerReconcilerLoop) setErrorStatus(ctx context.Context, err error) error {
	container := r.container
	msg := err.Error()

	if container.Status.Status == Error && container.Status.Message == msg {
		return nil
	}
	container.Status.Status = Error
	container.Status.Message = err.Error()
	return r.Status().Update(ctx, container)
}

func (r *containerReconcilerLoop) deleteIfNoNode(ctx context.Context) error {
	if r.container.IsMarkedForDeletion() {
		return nil
	}
	affinity := r.container.GetNodeAffinity()
	if affinity != "" {
		_, err := r.KubeService.GetNode(ctx, types.NodeName(affinity))
		if err != nil {
			if apierrors.IsNotFound(err) {
				deleteError := r.Client.Delete(ctx, r.container)
				if deleteError != nil {
					return deleteError
				}
				return lifecycle.NewWaitError(errors.New("Node is not found, deleting container"))
			}
		}
	}
	return nil
}

func (r *containerReconcilerLoop) isSignOrDiscoverDrivesOperation(ctx context.Context) bool {
	if r.container.Spec.Mode == weka.WekaContainerModeAdhocOpWC && r.container.Spec.Instructions != nil {
		return r.container.Spec.Instructions.Type == "sign-drives"
	}
	if r.container.Spec.Mode == weka.WekaContainerModeAdhocOp && r.container.Spec.Instructions != nil {
		return r.container.Spec.Instructions.Type == "discover-drives"
	}
	return false
}

func (r *containerReconcilerLoop) processResults(ctx context.Context) error {
	switch {
	case r.container.IsDriversBuilder():
		return r.UploadBuiltDrivers(ctx)
	case r.isSignOrDiscoverDrivesOperation(ctx):
		return r.updateNodeAnnotations(ctx)
	default:
		return nil
	}
}

func (r *containerReconcilerLoop) updateNodeAnnotations(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "updateNodeAnnotations")
	defer end()

	container := r.container
	node := r.node

	if node == nil {
		return errors.New("node is not set")
	}

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	var opResult *operations.DriveNodeResults
	err := json.Unmarshal([]byte(*container.Status.ExecutionResult), &opResult)
	if err != nil {
		err = fmt.Errorf("error unmarshalling execution result: %w", err)
		return err
	}

	// Update weka.io/weka-drives annotation
	previousDrives := []string{}
	newDrivesFound := 0
	if existingDrivesStr, ok := node.Annotations["weka.io/weka-drives"]; ok {
		_ = json.Unmarshal([]byte(existingDrivesStr), &previousDrives)
	}

	seenDrives := make(map[string]bool)
	for _, drive := range previousDrives {
		if drive == "" {
			continue // clean bad records of empty serial ids
		}
		seenDrives[drive] = true
	}

	complete := func() error {
		r.container.Status.Status = Completed
		return r.Status().Update(ctx, r.container)
	}

	for _, drive := range opResult.Drives {
		if drive.SerialId == "" { // skip drives without serial id if it was not set for whatever reason
			continue
		}
		if _, ok := seenDrives[drive.SerialId]; !ok {
			newDrivesFound++
		}
		seenDrives[drive.SerialId] = true
	}

	if newDrivesFound == 0 {
		logger.Info("No new drives found")
		return complete()
	}

	updatedDrivesList := []string{}
	for drive := range seenDrives {
		updatedDrivesList = append(updatedDrivesList, drive)
	}
	newDrivesStr, _ := json.Marshal(updatedDrivesList)
	node.Annotations["weka.io/weka-drives"] = string(newDrivesStr)

	// Update weka.io/drives extended resource
	blockedDrives := []string{}
	if blockedDrivesStr, ok := node.Annotations["weka.io/blocked-drives"]; ok {
		_ = json.Unmarshal([]byte(blockedDrivesStr), &blockedDrives)
	}
	availableDrives := len(seenDrives) - len(blockedDrives)
	node.Status.Capacity["weka.io/drives"] = *resource.NewQuantity(int64(availableDrives), resource.DecimalSI)

	if err := r.Status().Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node status: %w", err)
		return err
	}

	if err := r.Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node annotations: %w", err)
		return err
	}
	return complete()
}

type BuiltDriversResult struct {
	WekaVersion           string `json:"weka_version"`
	KernelSignature       string `json:"kernel_signature"`
	WekaPackNotSupported  bool   `json:"weka_pack_not_supported"`
	NoWekaDriversHandling bool   `json:"no_weka_drivers_handling"`
	Err                   string `json:"err"`
}

func (r *containerReconcilerLoop) UploadBuiltDrivers(ctx context.Context) error {
	target := r.container.Spec.UploadResultsTo
	if target == "" {
		return errors.New("uploadResultsTo is not set")
	}

	targetDistcontainer := &weka.WekaContainer{}
	// assuming same namespace
	err := r.Get(ctx, client.ObjectKey{Name: target, Namespace: r.container.Namespace}, targetDistcontainer)
	if err != nil {
		return err
	}

	complete := func() error {
		r.container.Status.Status = Completed
		return r.Status().Update(ctx, r.container)
	}

	// TODO: This is not a best solution, to download version, but, usable.
	// Should replace this with ad-hocy downloader container, that will use newer version(as the one who built), to download using shared storage

	executor, err := r.ExecService.GetExecutor(ctx, targetDistcontainer)
	if err != nil {
		return err
	}

	builderIp := r.pod.Status.PodIP
	builderPort := r.container.GetPort()

	if builderIp == "" {
		return errors.New("Builder IP is not set")
	}

	results := &BuiltDriversResult{}
	err = json.Unmarshal([]byte(*r.container.Status.ExecutionResult), results)
	if err != nil {
		return err
	}

	if results.NoWekaDriversHandling {
		// for legacy drivers handling, we don't have support for weka driver command
		// copy everything from builer's /opt/weka/dist/drivers to targetDistcontainer's /opt/weka/dist/drivers
		cmd := fmt.Sprintf("cd /opt/weka/dist/drivers/ && wget -r -nH --cut-dirs=3 --no-parent --reject=\"index.html*\" http://%s:%d/dist/v1/drivers/", builderIp, builderPort)
		stdout, stderr, err := executor.ExecNamed(ctx, "CopyDrivers",
			[]string{"bash", "-ce", cmd},
		)
		if err != nil {
			err := fmt.Errorf("failed to run command: %s, error: %s, stdout: %s, stderr: %s", cmd, err, stdout.String(), stderr.String())
			return err
		}
		return complete()
	}

	endpoint := fmt.Sprintf("https://%s:%d", builderIp, builderPort)

	// if weka pack is not supported, we don't need to download it
	if !results.WekaPackNotSupported {
		stdout, stderr, err := executor.ExecNamed(ctx, "DownloadVersion",
			[]string{"bash", "-ce",
				"weka version get --driver-only " + results.WekaVersion + " --from " + endpoint,
			},
		)
		if err != nil {
			return errors.Wrap(err, stderr.String()+stdout.String())
		}
	}

	downloadCmd := "weka driver download --without-agent --version " + results.WekaVersion + " --from " + endpoint
	if !results.WekaPackNotSupported {
		downloadCmd += " --kernel-signature " + results.KernelSignature
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "DownloadDrivers",
		[]string{"bash", "-ce", downloadCmd},
	)
	if err != nil {
		return errors.Wrap(err, stderr.String()+stdout.String())
	}

	if results.WekaPackNotSupported {
		url := fmt.Sprintf("%s/dist/v1/drivers/%s-%s.tar.gz.sha256", endpoint, results.WekaVersion, results.KernelSignature)
		cmd := "cd /opt/weka/dist/drivers/ && curl -kO " + url
		stdout, stderr, err = executor.ExecNamed(ctx, "Copy sha256 file",
			[]string{"bash", "-ce", cmd},
		)
		if err != nil {
			err := fmt.Errorf("failed to run command: %s, error: %s, stdout: %s, stderr: %s", cmd, err, stdout.String(), stderr.String())
			return err
		}
	}

	return complete()
}

func (r *containerReconcilerLoop) Noop(ctx context.Context) error {
	return nil
}

func (r *containerReconcilerLoop) ResultsAreSet() bool {
	return r.container.Status.ExecutionResult != nil
}

func (r *containerReconcilerLoop) ResultsAreProcessed() bool {
	for _, c := range r.container.Status.Conditions {
		if c.Type == condition.CondResultsProcessed && c.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *containerReconcilerLoop) cleanupFinishedOneOff(ctx context.Context) error {
	if r.container.IsDriversBuilder() || r.isSignOrDiscoverDrivesOperation(ctx) {
		if r.pod != nil {
			return r.Client.Delete(ctx, r.pod)
		}
	}
	if r.container.IsDriversLoaderMode() { // sounds like
		for _, c := range r.container.Status.Conditions {
			if c.Type == condition.CondResultsProcessed && c.Status == metav1.ConditionTrue {
				if time.Since(c.LastTransitionTime.Time) > time.Minute*5 {
					return r.Client.Delete(ctx, r.container)
				}
			}
		}
	}
	return nil
}

func (r *containerReconcilerLoop) updateDriversBuilderStatus(ctx context.Context) error {
	if r.container.Status.Status != Building {
		r.container.Status.Status = Building
		err := r.Status().Update(ctx, r.container)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) IsNotAlignedImage() bool {
	return r.container.Status.LastAppliedImage != r.container.Spec.Image
}

func (r *containerReconcilerLoop) ensurePodNotRunningState(ctx context.Context) error {
	if r.container.Status.Status != PodStatePodNotRunning {
		r.container.Status.Status = PodStatePodNotRunning
		err := r.Status().Update(ctx, r.container)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) updateAdhocOpStatus(ctx context.Context) error {
	if r.pod.Status.Phase == v1.PodRunning && r.container.Status.Status != PodStatePodRunning {
		r.container.Status.Status = PodStatePodRunning
		err := r.Status().Update(ctx, r.container)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) PodNotRunning() bool {
	return r.pod.Status.Phase != v1.PodRunning
}

func (r *containerReconcilerLoop) ReportOtelMetrics(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "MetricsData", "pod_name", r.container.Name, "namespace", r.container.Namespace, "mode", r.container.Spec.Mode)
	defer end()

	if r.MetricsService == nil {
		logger.Warn("Metrics service is not set")
		return nil
	}

	metrics, err := r.MetricsService.GetPodMetrics(ctx, r.pod)
	if err != nil {
		logger.Warn("Error getting pod metrics", "error", err)
		return nil // we ignore error, as this is not mandatory functionality
	}

	logger.SetAttributes(
		attribute.Float64("cpu_usage", metrics.CpuUsage),
		attribute.Int64("memory_usage", metrics.MemoryUsage),
		attribute.Float64("cpu_request", metrics.CpuRequest),
		attribute.Int64("memory_request", metrics.MemoryRequest),
		attribute.Float64("cpu_limit", metrics.CpuLimit),
		attribute.Int64("memory_limit", metrics.MemoryLimit),
	)

	if r.container.Status.Stats == nil {
		return nil
	}

	if r.container.IsBackend() {
		logger.SetAttributes(
			attribute.Int64("desired_processes", int64(r.container.Status.Stats.Processes.Desired)),
			attribute.Int64("created_processes", int64(r.container.Status.Stats.Processes.Desired)),
			attribute.Int64("active_processes", int64(r.container.Status.Stats.Processes.Active)),
		)
	}
	if r.container.IsDriveContainer() {
		logger.SetAttributes(
			attribute.Int64("desired_drives", int64(r.container.Status.Stats.Drives.DriveCounters.Desired)),
			attribute.Int64("created_drives", int64(r.container.Status.Stats.Drives.DriveCounters.Created)),
			attribute.Int64("active_drives", int64(r.container.Status.Stats.Drives.DriveCounters.Active)),
		)
	}

	return nil
}

func (r *containerReconcilerLoop) SetStatusMetrics(ctx context.Context) error {
	// TODO: Should we be do this locally? it actually will be better to find failures from different container
	// but, if we dont keep locality - performance wise too easy to make mistake and funnel everything throught just one
	// tldr: we need a proper service gateway for weka api, that will both healthcheck and distribute
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "SetStatusMetrics")
	defer end()

	// submit http request to metrics pod
	pods, err2 := r.getNodeAgentPods(ctx)
	if err2 != nil {
		return err2
	}

	if len(pods) == 0 {
		logger.Info("No metrics pod found")
		return nil
	}

	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		return err
	}

	payload := node_agent.GetContainerInfoRequest{ContainerId: string(r.container.GetUID())}

	var response node_agent.ContainerInfoResponse
	foundAlive := false
	for _, pod := range pods {
		if pod.Status.Phase == v1.PodRunning {
			// if multiple found - register on each one of them
			// make post request to metrics pod

			url := "http://" + pod.Status.PodIP + ":8090/getContainerInfo"

			// Convert the payload to JSON
			jsonData, err := json.Marshal(payload)
			if err != nil {
				return err
			}

			resp, err := util.SendJsonRequest(ctx, url, jsonData, util.RequestOptions{AuthHeader: "Token " + token})
			if err != nil {
				return err
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			err = json.NewDecoder(bytes.NewReader(body)).Decode(&response)
			if err != nil {
				logger.Error(err, "Error decoding response body", "body", string(body))
				return err
			}

			if resp.StatusCode != http.StatusOK {
				return errors.New("Error sending register request")
			}

			foundAlive = true
			break // trying just one pod, even if have multiple, for simplicity of error propagation
		}
	}

	if !foundAlive {
		return errors.New("No response from metrics pod")
	}

	r.container.Status.Stats = &response.ContainerMetrics
	if r.container.Status.PrinterColumns == nil {
		r.container.Status.PrinterColumns = &weka.ContainerPrinterColumns{}
	}

	if r.container.HasFrontend() && r.activeMounts != nil {
		r.container.Status.PrinterColumns.ActiveMounts = weka.StringMetric(fmt.Sprintf("%d", *r.activeMounts))
		r.container.Status.Stats.ActiveMounts = weka.IntMetric(int64(*r.activeMounts))
	}

	r.container.Status.Stats.Processes.Desired = weka.IntMetric(int64(r.container.Spec.NumCores) + 1)
	if r.container.IsDriveContainer() {
		r.container.Status.Stats.Drives.DriveCounters.Desired = weka.IntMetric(int64(r.container.Spec.NumDrives))
		r.container.Status.PrinterColumns.Drives = weka.StringMetric(r.container.Status.Stats.Drives.DriveCounters.String())
	}
	r.container.Status.PrinterColumns.Processes = weka.StringMetric(r.container.Status.Stats.Processes.String())
	r.container.Status.Stats.LastUpdate = metav1.NewTime(time.Now())

	return r.Status().Update(ctx, r.container)
}

func (r *containerReconcilerLoop) CondContainerDrivesRemoved() bool {
	return meta.IsStatusConditionTrue(r.container.Status.Conditions, condition.CondContainerDrivesRemoved)
}

func (r *containerReconcilerLoop) CanSkipDeactivate() bool {
	if !r.container.IsActive() {
		// if container state is paused/destroying, it means that cluster is being deleted
		// and we can skip deactivation flow
		return true
	}
	if !r.container.IsBackend() {
		return true
	}
	if !meta.IsStatusConditionTrue(r.container.Status.Conditions, condition.CondJoinedCluster) {
		return true
	}
	if r.container.Spec.Overrides == nil {
		return false
	}
	return r.container.Spec.Overrides.SkipDeactivate
}

func (r *containerReconcilerLoop) CanSkipRemoveFromS3Cluster() bool {
	if !r.container.IsS3Container() {
		return true
	}
	if !meta.IsStatusConditionTrue(r.container.Status.Conditions, condition.CondJoinedS3Cluster) {
		return true
	}
	return r.CanSkipDeactivate()
}

func (r *containerReconcilerLoop) CanSkipDrivesForceResign() bool {
	if r.container.Spec.Overrides == nil {
		return false
	}
	return r.container.Spec.Overrides.SkipDrivesForceResign
}

func (r *containerReconcilerLoop) CanProceedDeletion() bool {
	if r.CanSkipDeactivate() {
		return true
	}
	if !r.container.IsRemoved() {
		return false
	}
	// drives removal is wekacluster reconciler responsibility
	// (after continer deactivation there's no access to weka commands on cluster level)
	if r.container.IsDriveContainer() && !r.container.DrivesRemoved() {
		return false
	}
	return true
}

func (r *containerReconcilerLoop) RegisterContainerOnMetrics(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "RegisterContainerOnMetrics")
	defer end()

	nodeInfo, err := r.GetNodeInfo(ctx)
	if err != nil {
		return err
	}

	// find a pod service node metrics
	payload := node_agent.RegisterContainerPayload{
		ContainerName:     r.container.Name,
		ContainerId:       string(r.container.GetUID()),
		WekaContainerName: r.container.Spec.WekaContainerName,
		PersistencePath:   nodeInfo.GetContainerPersistencePath(r.container.GetUID()),
		Labels:            r.container.GetLabels(),
		Mode:              r.container.Spec.Mode,
	}
	// submit http request to metrics pod
	pods, err2 := r.getNodeAgentPods(ctx)
	if err2 != nil {
		return err2
	}

	if len(pods) == 0 {
		logger.Info("No metrics pod found")
		return nil
	}

	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		return err
	}

	for _, pod := range pods {
		if pod.Status.Phase == v1.PodRunning {
			// if multiple found - register on each one of them
			// make post request to metrics pod

			url := "http://" + pod.Status.PodIP + ":8090/register"

			// Convert the payload to JSON
			jsonData, err := json.Marshal(payload)
			if err != nil {
				logger.Error(err, "Error marshalling payload")
				continue
			}

			resp, err := util.SendJsonRequest(ctx, url, jsonData, util.RequestOptions{AuthHeader: "Token " + token})
			if err != nil {
				logger.Error(err, "Error sending register request")
				continue
			}

			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				logger.Error(err, "Error sending register request", "status", resp.Status)
				continue
			}
		}
	}

	return nil
}

func (r *containerReconcilerLoop) getNodeAgentPods(ctx context.Context) ([]v1.Pod, error) {
	if r.node == nil {
		return nil, errors.New("Node is not set")
	}

	ns, err := util.GetPodNamespace()
	if err != nil {
		return nil, err
	}
	pods, err := r.KubeService.GetPodsSimple(ctx, ns, r.node.Name, map[string]string{
		"app.kubernetes.io/component": "weka-node-agent",
	})
	if err != nil {
		return nil, err
	}
	return pods, nil
}

// a hack putting this global, but also not a harmful one
var nodeAgentToken string
var nodeAgentLastPull time.Time

func (r *containerReconcilerLoop) getNodeAgentToken(ctx context.Context) (string, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getNodeAgentToken")
	defer end()

	if nodeAgentToken != "" && time.Since(nodeAgentLastPull) < time.Minute {
		return nodeAgentToken, nil
	}

	ns, err := util.GetPodNamespace()
	if err != nil {
		return "", err
	}

	secret, err := r.KubeService.GetSecret(ctx, config.Config.Metrics.NodeAgentSecretName, ns)
	if err != nil {
		return "", err
	}
	if secret == nil {
		logger.Info("No secret found")
		return "", errors.New("No secret found")
	}

	tokenRaw := secret.Data["token"]
	token := string(tokenRaw)
	if token == "" {
		logger.Info("No token found")
		return "", errors.New("No token found")
	}
	nodeAgentToken = token
	nodeAgentLastPull = time.Now()

	return token, nil
}

func (r *containerReconcilerLoop) applyCurrentImage(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "applyCurrentImage")
	defer end()

	pod := r.pod
	container := r.container

	wekaContainer, err := r.getWekaPodContainer(pod)
	if err != nil {
		return err
	}
	if wekaContainer.Image != container.Spec.Image {
		return nil
	}

	if pod.Status.Phase != v1.PodRunning {
		logger.Info("Pod is not running yet")
		return errors.New("Pod is not running yet")
	}

	if container.Status.Status != ContainerStatusRunning {
		logger.Info("Container is not running yet")
		return errors.New("Container is not running yet")
	}

	container.Status.LastAppliedImage = container.Spec.Image
	return r.Status().Update(ctx, container)
}

func (r *containerReconcilerLoop) HasNodeAffinity() bool {
	return r.container.GetNodeAffinity() != ""
}
