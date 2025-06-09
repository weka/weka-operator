package controllers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/types"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/operations/csi"
	"github.com/weka/weka-operator/internal/controllers/operations/tempops"
	"github.com/weka/weka-operator/internal/controllers/operations/umount"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/node_agent"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
	"github.com/weka/weka-operator/pkg/workers"
)

func NodeIsReady(node *v1.Node) bool {
	if node == nil {
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

func NodeIsUnschedulable(node *v1.Node) bool {
	if node == nil {
		return false
	}
	return node.Spec.Unschedulable
}

func CanExecInPod(pod *v1.Pod) bool {
	// Review uses/split into few functions
	// pod deletion check is too aggressive, and matters mostly for s3/nfs, while openshift that has problem with deletiontimestamp is not in scope right now
	// s3 already does not rely on this function, and seems like the only place that actually cares for locality
	return pod != nil && pod.Status.Phase == v1.PodRunning && pod.DeletionTimestamp == nil
}

func NewContainerReconcileLoop(r *ContainerController, restClient rest.Interface) *containerReconcilerLoop {
	//TODO: We creating new client on every loop, we should reuse from reconciler, i.e pass it by reference
	mgr := r.Manager
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
		Recorder:       mgr.GetEventRecorderFor("wekaContainer-controller"),
		RestClient:     restClient,
		ThrottlingMap:  r.ThrottlingMap,
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
	Recorder         record.EventRecorder
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
	activeMounts  *int
	ThrottlingMap *util.ThrottlingSyncMap
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

func ContainerReconcileSteps(r *ContainerController, container *weka.WekaContainer) lifecycle.ReconciliationSteps {
	restClient := r.RestClient

	loop := NewContainerReconcileLoop(r, restClient)
	loop.container = container

	return lifecycle.ReconciliationSteps{
		Client:       loop.Client,
		StatusObject: loop.container,
		Throttler:    r.ThrottlingMap.WithPartition("container/" + loop.container.Name),
		Conditions:   &loop.container.Status.Conditions,
		Steps: []lifecycle.Step{
			{
				Run: loop.migrateEnsurePorts,
				Predicates: lifecycle.Predicates{
					func() bool {
						return len(loop.container.Spec.ExposePorts) != 0
					},
				},
				ContinueOnPredicatesFalse: true,
			},
			// put self in state "destroying" if container is marked for deletion
			{
				Run: loop.ensureStateDeleting,
				Predicates: lifecycle.Predicates{
					container.IsMarkedForDeletion,
					lifecycle.IsNotFunc(container.IsDeletingState),
					lifecycle.IsNotFunc(container.IsDestroyingState),
				},
				ContinueOnPredicatesFalse: true,
			},
			{Run: loop.GetNode},
			{Run: loop.refreshPod},
			// if cluster marked container state as deleting, update status and put deletion timestamp
			{
				Run: loop.handleStateDeleting,
				Predicates: lifecycle.Predicates{
					container.IsDeletingState,
				},
				ContinueOnPredicatesFalse: true,
			},
			// if cluster marked container state as destroying, update status and put deletion timestamp
			{
				Run: loop.handleStateDestroying,
				Predicates: lifecycle.Predicates{
					container.IsDestroyingState,
				},
				ContinueOnPredicatesFalse: true,
			},
			// this will allow go back into deactivate flow if we detected that container joined the cluster
			// at this point we would be stuck on weka local stop if container just-joined cluster, while we decided to delete it
			{
				Name: "ensurePodOnDeletion",
				Run:  lifecycle.ForceNoError(loop.ensurePod),
				Predicates: lifecycle.Predicates{
					loop.PodNotSet,
					loop.ShouldDeactivate,
					container.IsMarkedForDeletion,
					lifecycle.IsNotTrueCondition(condition.CondContainerRemoved, &container.Status.Conditions),
					lifecycle.IsNotFunc(container.IsS3Container), // no need to recover S3 container on deactivate
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Name: "SetStatusMetrics",
				Run:  lifecycle.ForceNoError(loop.SetStatusMetrics),
				Predicates: lifecycle.Predicates{
					lifecycle.BoolValue(config.Config.Metrics.Containers.Enabled),
					lifecycle.IsNotFunc(loop.PodNotSet),
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
								weka.WekaContainerModeEnvoy,
							}, container.Spec.Mode)
					},
				},
				Throttled:                 config.Config.Metrics.Containers.PollingRate,
				ContinueOnPredicatesFalse: true,
			},
			{
				Name: "ReportOtelMetrics",
				Run:  lifecycle.ForceNoError(loop.ReportOtelMetrics),
				Predicates: lifecycle.Predicates{
					lifecycle.IsNotFunc(loop.PodNotSet),
				},
				Throttled:                 config.Config.Metrics.Containers.PollingRate,
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition:  condition.CondRemovedFromS3Cluster,
				CondReason: "Deletion",
				Run:        loop.RemoveFromS3Cluster,
				Predicates: lifecycle.Predicates{
					loop.ShouldDeactivate,
					container.IsS3Container,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition:  condition.CondRemovedFromNFS,
				CondReason: "Deletion",
				Run:        loop.RemoveFromNfs,
				Predicates: lifecycle.Predicates{
					loop.ShouldDeactivate,
					container.IsNfsContainer,
				},
				ContinueOnPredicatesFalse: true,
			},
			//{
			//	Condition:  condition.CondContainerDrivesDeactivated,
			//	CondReason: "Deletion",
			//	Run:        loop.DeactivateDrives,
			//	Predicates: lifecycle.Predicates{
			//		loop.ShouldDeactivate,
			//		container.IsDriveContainer,
			//	},
			//	ContinueOnPredicatesFalse: true,
			//},
			{
				Condition:  condition.CondContainerDeactivated,
				CondReason: "Deletion",
				Run:        loop.DeactivateWekaContainer,
				Predicates: lifecycle.Predicates{
					loop.ShouldDeactivate,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run:        loop.RemoveDeactivatedContainersDrives,
				Condition:  condition.CondContainerDrivesRemoved,
				CondReason: "Deletion",
				Predicates: lifecycle.Predicates{
					loop.ShouldDeactivate,
					container.IsDriveContainer,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run:        loop.RemoveDeactivatedContainers,
				Condition:  condition.CondContainerRemoved,
				CondReason: "Deletion",
				Predicates: lifecycle.Predicates{
					loop.ShouldDeactivate,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Name: "reconcileClusterStatusOnDeletion",
				Run:  lifecycle.ForceNoError(loop.reconcileClusterStatus),
				Predicates: lifecycle.Predicates{
					container.ShouldJoinCluster,
					container.IsMarkedForDeletion,
					func() bool {
						return container.Status.ClusterContainerID == nil
					},
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.stopForceAndEnsureNoPod, // we want to force stop drives to release
				Predicates: lifecycle.Predicates{
					container.IsMarkedForDeletion,
					lifecycle.Or(
						loop.ShouldDeactivate, // if we were deactivating - we should also force stop, as we are safe at this point
						container.IsDestroyingState,
						func() bool {
							return container.Spec.GetOverrides().SkipDeactivate
						},
					),
					container.IsBackend, // if we needed to deactivate - we would not reach this point without deactivating
					// is it safe to force stop
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.waitForMountsOrDrain,
				// we do not try to align with whether we did stop - if we did stop for a some reason - good, graceful will succeed after it, if not - this is a protection
				Predicates: lifecycle.Predicates{
					container.IsMarkedForDeletion,
					container.IsClientContainer,
					lifecycle.IsNotFunc(loop.PodNotSet),
					func() bool {
						return !container.Spec.GetOverrides().SkipActiveMountsCheck
					},
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.stopForceAndEnsureNoPod, // we do not rely on graceful stop on clients until we test multiple weka versions with it under various failures
				Predicates: lifecycle.Predicates{
					container.IsMarkedForDeletion,
					container.IsClientContainer,
					lifecycle.IsNotFunc(loop.PodNotSet),
				},
				ContinueOnPredicatesFalse: true,
			},
			//TODO: Should we wait for mounts to go away on client before stopping on delete?
			{
				Run: loop.stopAndEnsureNoPod,
				// we do not try to align with whether we did stop - if we did stop for a some reason - good, graceful will succeed after it, if not - this is a protection
				Predicates: lifecycle.Predicates{
					container.IsMarkedForDeletion,
					container.IsWekaContainer,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition:  condition.CondContainerDrivesResigned,
				CondReason: "Deletion",
				Run:        loop.ResignDrives,
				Predicates: lifecycle.Predicates{
					container.IsMarkedForDeletion,
					lifecycle.IsNotFunc(loop.CanSkipDrivesForceResign),
					container.IsDriveContainer,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.HandleDeletion,
				Predicates: lifecycle.Predicates{
					container.IsMarkedForDeletion,
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
			{Run: loop.initState},
			{Run: loop.deleteIfNoNode},
			{
				Run: loop.checkTolerations,
				Predicates: lifecycle.Predicates{
					lifecycle.IsNotFunc(loop.NodeNotSet),
					lifecycle.BoolValue(config.Config.CleanupContainersOnTolerationsMismatch),
				},
				ContinueOnPredicatesFalse: true,
			},
			{Run: loop.ensureFinalizer},
			{Run: loop.ensureBootConfigMapInTargetNamespace},
			{
				Run: loop.updatePodLabelsOnChange,
				Predicates: lifecycle.Predicates{
					lifecycle.IsNotFunc(loop.PodNotSet),
					loop.podLabelsChanged,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				// in case pod gracefully went down, we dont want to deactivate, and we will drop timestamp once pod comes back
				Run: loop.dropStopAttemptRecord,
				Predicates: lifecycle.Predicates{
					lifecycle.IsNotFunc(loop.PodNotSet),
					func() bool {
						return container.IsDriveContainer() || container.IsComputeContainer()
					},
					func() bool {
						return loop.pod.DeletionTimestamp == nil
					},
				},
				ContinueOnPredicatesFalse: true,
			},
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
				// let drivers being re-built if node with drivers container is not found
				Run: loop.clearStatusOnNodeNotFound,
				Predicates: lifecycle.Predicates{
					container.IsDriversContainer,
					// only clear status if we have node affinity set in status, but not in spec
					func() bool {
						return container.Spec.NodeAffinity == "" && container.Status.NodeAffinity != ""
					},
					loop.NodeNotSet,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.uploadedDriversPeriodicCheck,
				Predicates: lifecycle.Predicates{
					loop.container.IsOneOff,
					loop.ResultsAreProcessed,
					loop.container.IsDriversBuilder,
				},
				ContinueOnPredicatesFalse: true,
				Throttled:                 config.Consts.CheckDriversInterval,
				ThrolltingSettings: util.ThrolltingSettings{
					EnsureStepSuccess: true,
				},
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
					lifecycle.IsNotFunc(container.IsMarkedForDeletion),
					loop.HasNodeAffinity, // if we dont have node set yet we can't load drivers, but we do want to load before creating pod if we have affinity
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.AllocateNICs,
				Predicates: lifecycle.Predicates{
					loop.ShouldAllocateNICs,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition: condition.CondContainerMigratedOutFromPVC,
				Run:       loop.MigratePVC,
				Predicates: lifecycle.Predicates{
					loop.PodNotSet,
					func() bool {
						return loop.container.Spec.PVC != nil && loop.container.Spec.GetOverrides().MigrateOutFromPvc
					},
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
				Run: loop.deletePodIfUnschedulable,
				Predicates: lifecycle.Predicates{
					container.IsDriversContainer,
					func() bool {
						// if node affinity is set in container status, try to reschedule pod
						// (do not delete pod if node affinity is set on wekacontainer's spec)
						return loop.pod.Status.Phase == v1.PodPending && container.Status.NodeAffinity != ""
					},
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.checkPodUnhealty,
				Predicates: lifecycle.Predicates{
					func() bool {
						return container.Status.Status != weka.Unhealthy
					},
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
				Run: loop.setNodeAffinityStatus,
				Predicates: lifecycle.Predicates{
					lifecycle.IsNotFunc(loop.HasNodeAffinity),
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
				Run: loop.cleanupFinished,
				Predicates: lifecycle.Predicates{
					func() bool {
						return loop.pod.Status.Phase == v1.PodSucceeded
					},
				},
				ContinueOnPredicatesFalse: true,
			},
			{Run: loop.WaitForNodeReady},
			{Run: loop.WaitForPodRunning},
			{
				Run:       loop.WriteResources,
				Condition: condition.CondContainerResourcesWritten,
				Predicates: lifecycle.Predicates{
					lifecycle.Or(
						container.IsAllocatable,
						container.IsClientContainer, // nics/machine-identifiers
					),
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
					lifecycle.Or(container.IsAdhocOpContainer, container.IsDiscoveryContainer),
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
				Name: "ReconcileManagementIPs",
				Run:  loop.reconcileManagementIPs,
				Predicates: lifecycle.Predicates{
					func() bool {
						// we don't want to reconcile management IPs for containers that are already Running
						return len(loop.container.Status.GetManagementIps()) == 0 && container.Status.Status != weka.Running
					},
					func() bool {
						return container.IsBackend() || container.Spec.Mode == weka.WekaContainerModeClient
					},
				},
				ContinueOnPredicatesFalse: true,
				OnFail:                    loop.setErrorStatus,
			},
			{
				Name: "PeriodicReconcileManagementIPs",
				Run:  lifecycle.ForceNoError(loop.reconcileManagementIPs),
				Predicates: lifecycle.Predicates{
					func() bool {
						// we want to periodically reconcile management IPs for containers that are already Running
						return container.Status.Status == weka.Running
					},
					func() bool {
						return container.IsBackend() || container.IsClientContainer()
					},
				},
				ContinueOnPredicatesFalse: true,
				Throttled:                 time.Minute * 3,
			},
			{
				Name: "ReconcileWekaLocalStatus",
				Run:  loop.reconcileWekaLocalStatus,
				Predicates: lifecycle.Predicates{
					container.IsWekaContainer,
					lifecycle.IsNotFunc(container.IsMarkedForDeletion),
					loop.IsStatusOvervwritableByLocal,
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
				Run: loop.setJoinIpsIfStuckInStemMode,
				Predicates: lifecycle.Predicates{
					container.ShouldJoinCluster,
					func() bool {
						return container.Status.ClusterContainerID == nil && len(container.Spec.JoinIps) == 0
					},
					func() bool {
						return container.Status.InternalStatus == "STEM"
					},
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
				Run: loop.EnsureDrives,
				Predicates: lifecycle.Predicates{
					container.IsDriveContainer,
					func() bool {
						return len(loop.container.Status.Allocations.Drives) > 0
					},
					func() bool {
						return loop.container.Status.InternalStatus == "READY"
					},
				},
				ContinueOnPredicatesFalse: true,
				OnFail:                    loop.setDrivesErrorStatus,
				Throttled:                 config.Consts.PeriodicDrivesCheckInterval,
				ThrolltingSettings: util.ThrolltingSettings{
					EnsureStepSuccess: true,
				},
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
				Condition:             condition.CondCsiDeployed,
				SkipOwnConditionCheck: true,
				Run:                   loop.DeployCsiNodeServerPod,
				Predicates: lifecycle.Predicates{
					container.IsClientContainer,
					lifecycle.BoolValue(config.Config.CsiInstallationEnabled),
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.CleanupCsiNodeServerPod,
				Predicates: lifecycle.Predicates{
					container.IsClientContainer,
					lifecycle.IsTrueCondition(condition.CondCsiDeployed, &container.Status.Conditions),
					lifecycle.BoolValue(!config.Config.CsiInstallationEnabled),
				},
				ContinueOnPredicatesFalse: true,
			},
		},
	}
}

func (r *containerReconcilerLoop) ShouldAllocateNICs() bool {
	if !r.container.IsBackend() && !r.container.IsClientContainer() {
		return false
	}

	if r.container.Spec.Network.EthDevice != "" || len(r.container.Spec.Network.EthDevices) > 0 || len(r.container.Spec.Network.DeviceSubnets) > 0 {
		return false
	}

	if r.node == nil {
		return false
	}

	if r.container.IsMarkedForDeletion() {
		return false
	}

	if r.container.Spec.Network.UdpMode {
		return false
	}

	if !strings.HasPrefix(r.node.Spec.ProviderID, "aws://") && !strings.HasPrefix(r.node.Spec.ProviderID, "ocid1.") {
		return false

	}

	annotationAllocations := make(domain.Allocations)
	allocationsStr, ok := r.node.Annotations[domain.WEKAAllocations]
	if ok {
		err := json.Unmarshal([]byte(allocationsStr), &annotationAllocations)
		if err != nil {
			return true
		}
		allocationIdentifier := domain.GetAllocationIdentifier(r.container.Namespace, r.container.Name)
		nicsAllocationsNumber := 0
		if _, ok = annotationAllocations[allocationIdentifier]; ok {
			nicsAllocationsNumber = len(annotationAllocations[allocationIdentifier].NICs)
		}
		if nicsAllocationsNumber >= r.container.Spec.NumCores {
			return false
		}
	}

	return true
}

func (r *containerReconcilerLoop) AllocateNICs(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "allocateNICs")
	defer end()

	logger.Debug("Allocating container NICS", "name", r.container.ObjectMeta.Name)

	nicsStr, ok := r.node.Annotations[domain.WEKANICs]
	if !ok {
		err := fmt.Errorf("node %s does not have weka-nics annotation, but dpdk is enabled", r.node.Name)
		logger.Error(err, "")
		return err
	}

	annotationAllocations := make(domain.Allocations)
	allocationsStr, ok := r.node.Annotations[domain.WEKAAllocations]
	if ok {
		err := json.Unmarshal([]byte(allocationsStr), &annotationAllocations)
		if err != nil {
			return fmt.Errorf("failed to unmarshal weka-allocations: %v", err)
		}
	}

	var allNICs []domain.NIC
	err := json.Unmarshal([]byte(nicsStr), &allNICs)
	if err != nil {
		return fmt.Errorf("failed to unmarshal weka-nics: %v", err)
	}

	allocatedNICs := make(map[string]types.Nil)
	for _, alloc := range annotationAllocations {
		for _, nicIdentifier := range alloc.NICs {
			allocatedNICs[nicIdentifier] = types.Nil{}
		}
	}

	allocationIdentifier := domain.GetAllocationIdentifier(r.container.Namespace, r.container.Name)
	nicsAllocationsNumber := 0
	if _, ok := annotationAllocations[allocationIdentifier]; ok {
		nicsAllocationsNumber = len(annotationAllocations[allocationIdentifier].NICs)
	} else {
		annotationAllocations[allocationIdentifier] = domain.Allocation{NICs: []string{}}
	}

	requiredNicsNumber := r.container.Spec.NumCores
	logger.Debug("Allocated NICs", "allocatedNICs", allocatedNICs, "container", r.container.Name)
	if nicsAllocationsNumber >= requiredNicsNumber {
		logger.Debug("Container already allocated NICs", "name", r.container.ObjectMeta.Name)
		return nil
	}
	logger.Info("Allocating NICs", "requiredNicsNumber", requiredNicsNumber, "nicsAllocationsNumber", nicsAllocationsNumber, "container", r.container.Name)
	for range make([]struct{}, requiredNicsNumber-nicsAllocationsNumber) {
		for _, nic := range allNICs {
			if _, ok = allocatedNICs[nic.MacAddress]; !ok {
				allocatedNICs[nic.MacAddress] = types.Nil{}
				logger.Debug("Allocating NIC", "nic", nic.MacAddress, "container", r.container.Name)
				nics := append(annotationAllocations[allocationIdentifier].NICs, nic.MacAddress)
				annotationAllocations[allocationIdentifier] = domain.Allocation{NICs: nics}
				break
			}
		}
	}
	allocationsBytes, err := json.Marshal(annotationAllocations)
	if err != nil {
		return fmt.Errorf("failed to marshal weka-allocations: %v", err)
	}

	r.node.Annotations[domain.WEKAAllocations] = string(allocationsBytes)
	return r.Client.Update(ctx, r.node)
}

func (r *containerReconcilerLoop) HandleDeletion(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	logger.Info("Handling container deletion", "container", r.container.Name)

	err := r.removeAllocations(ctx)
	if err != nil {
		logger.Error(err, "Failed to remove container allocations annotations from node")
		return err
	}

	err = r.finalizeContainer(ctx)
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
	ctx, _, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	err := r.stopAndEnsureNoPod(ctx)
	if err != nil {
		return err
	}

	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		return nil
	}

	containers, err := r.getClusterContainers(ctx)
	executeInContainer := discovery.SelectActiveContainer(containers)

	wekaService := services.NewWekaService(r.ExecService, executeInContainer)
	return wekaService.RemoveFromS3Cluster(ctx, *containerId)
}

func (r *containerReconcilerLoop) RemoveFromNfs(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		return errors.New("Container ID is not set")
	}

	executeInContainer := r.container

	if !NodeIsReady(r.node) || !CanExecInPod(r.pod) {
		containers, err := r.getClusterContainers(ctx)
		if err != nil {
			return err
		}
		executeInContainer = discovery.SelectActiveContainer(containers)
	}

	if executeInContainer == nil {
		return errors.New("No active container found")
	}

	logger.Info("Removing container from NFS", "container_id", *containerId)

	wekaService := services.NewWekaService(r.ExecService, executeInContainer)
	return wekaService.RemoveNfsInterfaceGroups(ctx, *containerId)
}

func (r *containerReconcilerLoop) DeactivateDrives(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		return errors.New("Container ID is not set")
	}

	executeInContainer := r.container

	if !NodeIsReady(r.node) || !CanExecInPod(r.pod) {
		containers, err := r.getClusterContainers(ctx)
		if err != nil {
			return err
		}
		executeInContainer = discovery.SelectActiveContainer(containers)
	}

	if executeInContainer == nil {
		return errors.New("No active container found")
	}

	wekaService := services.NewWekaService(r.ExecService, executeInContainer)
	statusActive := "ACTIVE"
	statusInactive := "INACTIVE"

	drives, err := wekaService.ListContainerDrives(ctx, *containerId)
	if err != nil {
		return err
	}

	return workers.ProcessConcurrently(ctx, drives, 5, func(ctx context.Context, drive services.Drive) error {
		switch drive.Status {
		case statusActive:
			logger.Info("Deactivating drive", "drive_id", drive.Uuid)
			return wekaService.DeactivateDrive(ctx, drive.Uuid)
		case statusInactive:
			return nil
		default:
			return fmt.Errorf("drive %s has status '%s', wait for it to become 'INACTIVE'", drive.SerialNumber, drive.Status)
		}
	}).AsError()
}

func (r *containerReconcilerLoop) DeactivateWekaContainer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		return errors.New("Container ID is not set")
	}

	if r.container.IsS3Container() {
		err := r.stopAndEnsureNoPod(ctx)
		if err != nil {
			return err
		}
	}

	containers, err := r.getClusterContainers(ctx)
	if err != nil {
		return err
	}

	execInContainer := discovery.SelectActiveContainer(containers)
	if execInContainer == nil {
		return errors.New("No active container found")
	}

	timeout := 30 * time.Second
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, execInContainer, &timeout)

	wekaContainer, err := wekaService.GetWekaContainer(ctx, *containerId)
	if err != nil {
		return err
	}

	logger.Info("Container status", "container_id", *containerId, "status", wekaContainer.Status)

	switch wekaContainer.Status {
	case "INACTIVE":
		// nothing to do
		return nil
	case "DEACTIVATING":
		return lifecycle.NewWaitErrorWithDuration(
			errors.New("container is deactivating"),
			time.Second*15,
		)
	default:
		logger.Info("Deactivating container", "container_id", *containerId)

		err := wekaService.DeactivateContainer(ctx, *containerId)
		if err != nil {
			return err
		}

		return lifecycle.NewWaitErrorWithDuration(
			errors.New("container deactivation started"),
			time.Second*15,
		)
	}
}

func (r *containerReconcilerLoop) s3ContainerPreDeactivate(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "s3ContainerPreDeactivate")
	defer end()

	executeInContainer := r.container

	nodeReady := NodeIsReady(r.node)
	podAvailable := r.pod != nil && r.pod.Status.Phase == v1.PodRunning

	wekaService := services.NewWekaService(r.ExecService, executeInContainer)

	// TODO: temporary check caused by weka s3 container remove behavior
	if podAvailable && nodeReady {
		// check that local s3 container does not exist anymore
		// if it does, wait for it to be removed
		localContainers, err := wekaService.ListLocalContainers(ctx)
		if err != nil {
			if strings.Contains(err.Error(), "weka-agent isn't running") {
				logger.Info("weka-agent isn't running, skipping local containers check")
				return nil
			}

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
	} else {
		err := errors.New("cannot check local s3 container - node is not ready or pod is not available")
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	return nil
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
	if execInContainer == nil {
		return errors.New("No active container found")
	}

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
	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		return errors.New("Container ID is not set")
	}

	// if less then 1 minute passed from deactivation - hold on the removal
	reset := func() {}
	if !r.container.IsS3Container() {
		throttler := r.ThrottlingMap.WithPartition("cluster/" + r.container.Status.ClusterID + "/" + r.container.Spec.Mode)
		if !throttler.ShouldRun("removeDeactivatedContainers", time.Minute, util.ThrolltingSettings{EnsureStepSuccess: false}) {
			return lifecycle.NewWaitErrorWithDuration(
				errors.New("throttling removal of containers from weka"),
				time.Second*15,
			)
		}
		reset = func() {
			throttler.Reset("removeDeactivatedContainers")
		}
	}

	err := r.removeDeactivatedContainers(ctx, *containerId)
	if err != nil {
		// in case of error - we do not want to throttle
		reset()
		return err
	}

	return nil
}

func (r *containerReconcilerLoop) removeAllocations(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "removeAllocations")
	defer end()

	if r.node == nil {
		return nil
	}

	logger.Info("Removing allocations", "container_id", r.container.Status.ClusterContainerID, "container_name", r.container.Name)
	annotationAllocations := make(domain.Allocations)
	patch := client.MergeFrom(r.node.DeepCopy())
	allocationsStr, ok := r.node.Annotations[domain.WEKAAllocations]
	if ok {
		err := json.Unmarshal([]byte(allocationsStr), &annotationAllocations)
		if err != nil {
			return fmt.Errorf("failed to unmarshal weka-allocations: %v", err)
		}
	} else {
		logger.Info("No allocations found in node annotations")
		return nil
	}
	updated := false
	updatedAnnotationAllocations := make(domain.Allocations)
	for key, alloc := range annotationAllocations {
		if domain.GetAllocationIdentifier(r.container.ObjectMeta.Namespace, r.container.ObjectMeta.Name) == key {
			logger.Info("Removing allocation", "allocation_id", key)
			updated = true
		} else {
			updatedAnnotationAllocations[key] = alloc
		}
	}
	if updated {
		allocationsBytes, err := json.Marshal(updatedAnnotationAllocations)
		if err != nil {
			return fmt.Errorf("failed to marshal weka-allocations: %v", err)
		}
		r.node.Annotations[domain.WEKAAllocations] = string(allocationsBytes)
		err = r.Patch(ctx, r.node, patch)
		if err != nil {
			return fmt.Errorf("failed to patch node annotations: %v", err)
		}
	}

	return nil
}

func (r *containerReconcilerLoop) removeDeactivatedContainers(ctx context.Context, containerId int) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containers, err := r.getClusterContainers(ctx)
	if err != nil {
		return err
	}

	execInContainer := discovery.SelectActiveContainer(containers)
	if execInContainer == nil {
		return errors.New("No active container found")
	}

	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	logger.Info("Removing container", "container_id", containerId)

	err = wekaService.RemoveContainer(ctx, containerId)
	if err != nil {
		err = errors.Wrap(err, "Failed to remove container")
		return err
	}

	return nil
}

func (r *containerReconcilerLoop) ResignDrives(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	nodeName := r.container.GetNodeAffinity()

	// if node name is empty, it means no node affinity was set on wekaContainer,
	// so we should not check if node is alive
	if nodeName != "" && (!NodeIsReady(r.node) || NodeIsUnschedulable(r.node)) {
		if config.Config.CleanupRemovedNodes {
			_, err := r.KubeService.GetNode(ctx, k8sTypes.NodeName(nodeName))
			if err != nil {
				if apierrors.IsNotFound(err) {
					logger.Info("node is deleted, no need for cleanup")
					return nil
				}
			}
		}
		err := fmt.Errorf("container node is not ready or is unschedulable, cannot perform resign drives operation")
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
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
	details := *deactivatedContainer.ToContainerDetails()
	details.Image = config.Config.SignDrivesImage
	op := operations.NewResignDrivesOperation(
		r.Manager,
		&payload,
		deactivatedContainer,
		details,
		nil,
		emptyCallback,
		nil,
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
	upgradeRunning := false

	// TODO: do we actually use instructions on non-weka containers in weka_runtime? Consider when breaking out into steps
	// Consider also generating a python mapping along with version script so we can use stuff like IsWekaContainer on python side
	if r.container.IsWekaContainer() {
		if err := r.updateContainerStatusIfNotEquals(ctx, weka.PodTerminating); err != nil {
			return err
		}
	}

	skipExec := false
	if r.node != nil {
		skipExec = strings.Contains(r.node.Status.NodeInfo.ContainerRuntimeVersion, "cri-o")
	}

	if r.node == nil {
		return nil
	}

	if container.Spec.Image != container.Status.LastAppliedImage && container.Status.LastAppliedImage != "" {
		var wekaPodContainer v1.Container
		wekaPodContainer, err := r.getWekaPodContainer(pod)
		if err != nil {
			return err
		}

		if wekaPodContainer.Image != container.Spec.Image {
			upgradeRunning = true
		}
	}

	if r.container.Spec.GetOverrides().PodDeleteForceReplace {
		_ = r.writeAllowForceStopInstruction(ctx, pod, skipExec)
		return r.runWekaLocalStop(ctx, pod, true)
	}

	if r.container.Spec.GetOverrides().UpgradeForceReplace {
		if upgradeRunning {
			_ = r.writeAllowForceStopInstruction(ctx, pod, skipExec)
			return r.runWekaLocalStop(ctx, pod, true)
		}
	}

	if container.IsBackend() && config.Config.EvictContainerOnDeletion && !(container.IsComputeContainer() && container.Spec.GetOverrides().UpgradePreventEviction) && !(container.IsS3Container()) {
		// unless overrides were used, we are not allowing container to stop on-pod-deletion
		// unless this was a force delete, or a force-upgrade scenario, we are not allowing container to stop on-pod-deletion and unless going deactivate flow
		logger.Info("Evicting container on pod deletion")
		err := r.ensureStateDeleting(ctx)
		if err != nil {
			return err
		}
		return lifecycle.NewWaitError(errors.New("evicting container on pod deletion"))
	}

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
		if upgradeRunning {
			logger.Info("Upgrade detected")
			err := r.runFrontendUpgradePrepare(ctx)
			if err != nil && errors.Is(err, &NoWekaFsDriverFound{}) {
				logger.Info("No wekafs driver found, skip prepare-upgrade")
			} else if err != nil {
				return err
			}
		}
	}

	err := r.writeAllowStopInstruction(ctx, pod, skipExec)
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

		// TODO: changing api to get IsComputeContainer is too much, we should have out-of-api helper functions
		if r.container.Status.Timestamps == nil {
			r.container.Status.Timestamps = make(map[string]metav1.Time)
		}
		if container.IsDriveContainer() || container.IsComputeContainer() {
			if since, ok := r.container.Status.Timestamps[string(weka.TimestampStopAttempt)]; !ok {
				r.container.Status.Timestamps[string(weka.TimestampStopAttempt)] = metav1.Time{Time: time.Now()}
				if err := r.Status().Update(ctx, r.container); err != nil {
					return err
				}
			} else {
				if time.Since(since.Time) > 5*time.Minute && !(container.Spec.GetOverrides().MigrateOutFromPvc && container.Spec.PVC != nil) {
					// lets start deactivate flow, we are doing it by deleting weka container
					if err := r.ensureStateDeleting(ctx); err != nil {
						return err
					} else {
						return lifecycle.NewWaitError(errors.New("deleting weka container"))
					}
				}
			}
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
	ctx, _, end := instrumentation.GetLogSpan(ctx, "runWekaLocalStop")
	defer end()
	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		return err
	}

	args := []string{"weka", "local", "stop"}

	// we need to use --force flag
	if force {
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

func (r *containerReconcilerLoop) findAdjacentNodeAgent(ctx context.Context, pod *v1.Pod) (*v1.Pod, error) {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "findAdjacentNodeAgent", "node", r.container.GetNodeAffinity())
	defer end()

	agentPods, err := r.getNodeAgentPods(ctx)
	var waitErr *lifecycle.WaitError
	if err != nil && errors.As(err, &waitErr) {
		return nil, err
	}
	if err != nil {
		err = fmt.Errorf("failed to get node agent pods: %w", err)
		return nil, err
	}
	if len(agentPods) == 0 {
		return nil, errors.New("There are no agent pods on node")
	}

	var targetNodeName string
	if r.container.GetNodeAffinity() != "" {
		targetNodeName = string(r.container.GetNodeAffinity())
	} else {
		if r.pod == nil {
			return nil, errors.New("Pod is nil and no affinity on container")
		}
		targetNodeName = pod.Spec.NodeName
	}

	for _, agentPod := range agentPods {
		if agentPod.Spec.NodeName == targetNodeName {
			if agentPod.Status.Phase == v1.PodRunning {
				return &agentPod, nil
			}
			return nil, &NodeAgentPodNotRunning{}
		}
	}
	return nil, errors.New("No agent pod found on the same node")
}

func (r *containerReconcilerLoop) sendStopInstructionsViaAgent(ctx context.Context, pod *v1.Pod, instructions resources.ShutdownInstructions) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "sendStopInstructionsViaAgent", "force", instructions.AllowForceStop, "instructions", instructions)
	defer end()

	agentPod, err := r.findAdjacentNodeAgent(ctx, pod)
	if err != nil {
		return err
	}

	instructionsJson, err := json.Marshal(instructions)
	if err != nil {
		return err
	}

	executor, err := util.NewExecInPodByName(r.RestClient, r.Manager.GetConfig(), agentPod, "node-agent")
	if err != nil {
		return err
	}

	nodeInfo, err := r.GetNodeInfo(ctx)
	if err != nil {
		return err
	}
	instructionsBasePath := path.Join(resources.GetPodShutdownInstructionPathOnAgent(nodeInfo.BootID, pod))
	instructionsPath := path.Join(instructionsBasePath, "shutdown_instructions.json")

	_, _, err = executor.ExecNamed(ctx, "StopInstructionsViaAgent", []string{"bash", "-ce", fmt.Sprintf("mkdir -p '%s' && echo '%s' > '%s'", instructionsBasePath, instructionsJson, instructionsPath)})
	if err != nil {
		logger.Error(err, "Error writing stop instructions via node-agent")
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) writeAllowForceStopInstruction(ctx context.Context, pod *v1.Pod, skipExec bool) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	// create a Json and sent it to node-agent, required for CoreOS / cri-o container agent
	// since we can't execute directly on pod if it is in terminating state
	err := r.sendStopInstructionsViaAgent(ctx, pod, resources.ShutdownInstructions{AllowStop: false, AllowForceStop: true})
	if err != nil {
		logger.Error(err, "Error writing force-stop instructions via node-agent")
	}
	if skipExec {
		return err
	}

	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		return err
	}

	_, _, err = executor.ExecNamed(ctx, "AllowForceStop", []string{"bash", "-ce", "touch /tmp/.allow-force-stop && kill 1"})
	if err != nil {
		if !strings.Contains(err.Error(), "container not found") {
			return err
		}
	}
	logger.Info("Force stop instruction written")
	return nil
}

func (r *containerReconcilerLoop) writeAllowStopInstruction(ctx context.Context, pod *v1.Pod, skipExec bool) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	// create a Json and sent it to node-agent, required for CoreOS / cri-o container agent
	// since we can't execute directly on pod if it is in terminating state
	err := r.sendStopInstructionsViaAgent(ctx, pod, resources.ShutdownInstructions{AllowStop: true, AllowForceStop: false})
	if err != nil {
		logger.Error(err, "Error writing stop instructions via node-agent")
		// NOTE: No error on purpose, as it's only one of method we attempt to start stopping
	}
	if skipExec {
		return err
	}
	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		return err
	}

	_, _, err = executor.ExecNamed(ctx, "AllowStop", []string{"bash", "-ce", "touch /tmp/.allow-stop && kill 1"})
	if err != nil {
		if !strings.Contains(err.Error(), "container not found") {
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) ensureStateDeleting(ctx context.Context) error {
	return services.SetContainerStateDeleting(ctx, r.container, r.Client)
}

func (r *containerReconcilerLoop) handleStateDeleting(ctx context.Context) error {
	statusUpdated := false

	if r.container.IsClientContainer() {
		activeMounts, _ := r.getCachedActiveMounts(ctx)
		if activeMounts != nil && *activeMounts > 0 {
			if err := r.updateContainerStatusIfNotEquals(ctx, weka.Draining); err != nil {
				return err
			}
			statusUpdated = true
		}
	}

	if !statusUpdated {
		if err := r.updateContainerStatusIfNotEquals(ctx, weka.Deleting); err != nil {
			return err
		}
	}

	if !r.container.IsMarkedForDeletion() {
		// self-delete
		err := r.Delete(ctx, r.container)
		if err != nil {
			return err
		}
		return lifecycle.NewWaitError(errors.New("Container is being deleting, refetching"))
	}
	return nil
}

func (r *containerReconcilerLoop) handleStateDestroying(ctx context.Context) error {
	statusUpdated := false

	if r.container.IsClientContainer() {
		activeMounts, _ := r.getCachedActiveMounts(ctx)
		if activeMounts != nil && *activeMounts > 0 {
			if err := r.updateContainerStatusIfNotEquals(ctx, weka.Draining); err != nil {
				return err
			}
			statusUpdated = true
		}
	}

	if !statusUpdated {
		if err := r.updateContainerStatusIfNotEquals(ctx, weka.Destroying); err != nil {
			return err
		}
	}

	if !r.container.IsMarkedForDeletion() {
		// self-delete
		err := r.Delete(ctx, r.container)
		if err != nil {
			return err
		}
		return lifecycle.NewWaitError(errors.New("Container is being deleting, refetching"))
	}
	return nil
}

func (r *containerReconcilerLoop) handleStatePaused(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	if r.container.Status.Status != weka.Paused {
		err := r.stopForceAndEnsureNoPod(ctx)
		if err != nil {
			return err
		}

		logger.Debug("Updating status", "from", r.container.Status.Status, "to", weka.Paused)
		r.container.Status.Status = weka.Paused

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

	if r.container.Status.PrinterColumns == nil {
		r.container.Status.PrinterColumns = &weka.ContainerPrinterColumns{}
	}

	changed := false

	if r.HasNodeAffinity() && r.container.Status.PrinterColumns.NodeAffinity == "" {
		r.container.Status.PrinterColumns.NodeAffinity = string(r.container.GetNodeAffinity())
		changed = true
	}

	// save printed management IPs if not set (for the back-compatibility with "single" managementIP)
	if r.container.Status.GetPrinterColumns().ManagementIPs == "" && len(r.container.Status.GetManagementIps()) > 0 {
		r.container.Status.PrinterColumns.SetManagementIps(r.container.Status.GetManagementIps())
		changed = true
	}

	if changed {
		if err := r.Status().Update(ctx, r.container); err != nil {
			return errors.Wrap(err, "Failed to update status")
		}
	}
	return nil
}

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

func (r *containerReconcilerLoop) updatePodLabelsOnChange(ctx context.Context) error {
	pod := r.pod
	pod.SetLabels(resources.LabelsForWekaPod(r.container))

	if err := r.Update(ctx, pod); err != nil {
		return fmt.Errorf("failed to update pod labels: %w", err)
	}
	r.pod = pod
	return nil
}

func (r *containerReconcilerLoop) podLabelsChanged() bool {
	oldLabels := r.pod.GetLabels()
	newLabels := resources.LabelsForWekaPod(r.container)

	return !util.NewHashableMap(newLabels).Equals(util.NewHashableMap(oldLabels))
}

func (r *containerReconcilerLoop) checkPodUnhealty(ctx context.Context) error {
	pod := r.pod

	// check ContainersReady
	podContainersReady := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == v1.ContainersReady && condition.Status == v1.ConditionTrue {
			podContainersReady = true
			break
		}
	}

	if !podContainersReady {
		// check pod's RESTARTS
		for _, containerStatus := range pod.Status.ContainerStatuses {
			if containerStatus.Name == "weka-container" {
				if containerStatus.RestartCount > 0 {
					err := r.updateContainerStatusIfNotEquals(ctx, weka.Unhealthy)
					if err != nil {
						return err
					}
					// stop here, no reason to go to the next steps
					err = errors.New("pod is unhealthy")
					return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
				}
			}
		}
	}
	return nil
}

func (r *containerReconcilerLoop) reconcileManagementIPs(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	ipAddresses, err := r.getManagementIps(ctx)
	if err != nil {
		err = errors.New("waiting for management IPs")
		return err
	}

	logger.WithValues("management_ips", ipAddresses).Info("Got management IPs")
	if !util.SliceEquals(container.Status.ManagementIPs, ipAddresses) {
		container.Status.ManagementIPs = ipAddresses

		r.container.Status.PrinterColumns.SetManagementIps(ipAddresses)
		if err := r.Status().Update(ctx, container); err != nil {
			logger.Error(err, "Error updating status")
			return err
		}
		return nil
	}
	return nil
}

func (r *containerReconcilerLoop) getManagementIps(ctx context.Context) ([]string, error) {
	executor, err := r.ExecService.GetExecutor(ctx, r.container)
	if err != nil {
		return nil, err
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "GetManagementIps", []string{"cat", "/opt/weka/k8s-runtime/management_ips"})
	if err != nil {
		err = fmt.Errorf("Error reading management IPs: %v, %s", err, stderr.String())
		return nil, err
	}

	ips := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	return ips, nil
}

func (r *containerReconcilerLoop) setJoinIpsIfStuckInStemMode(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	ownerRef := container.GetOwnerReferences()
	if len(ownerRef) == 0 {
		return errors.New("no owner references found")
	}

	owner := ownerRef[0]
	clusterGuid := string(owner.UID)

	clusterCreationTime, err := services.ClustersCachedInfo.GetClusterCreationTime(ctx, clusterGuid)
	if err != nil {
		return fmt.Errorf("error getting cluster creation time: %w", err)
	}

	// if cluster creation time is more than 1 minute, set join ips in the container spec
	if time.Since(clusterCreationTime) > time.Minute {
		joinIps, _ := services.ClustersCachedInfo.GetJoinIps(ctx, clusterGuid, owner.Name, container.Namespace)
		if len(joinIps) > 0 {
			container.Spec.JoinIps = joinIps
			executor, err := r.ExecService.GetExecutor(ctx, r.container)
			if err != nil {
				return fmt.Errorf("error getting executor: %w", err)
			}
			// 1. Reconfigure container with new join ips
			cmd := []string{"weka", "local", "resources", "join-ips"}
			cmd = append(cmd, joinIps...)
			_, stderr, err := executor.ExecNamed(ctx, "WekaLocalResources", cmd)
			if err != nil {
				return fmt.Errorf("error executing weka local resources: %w, %s", err, stderr.String())
			}
			// 2. Apply local resources change
			_, stderr, err = executor.ExecNamed(ctx, "WekaLocalResourcesApply", []string{"weka", "local", "resources", "apply", "-f"})
			if err != nil {
				return fmt.Errorf("error executing weka local resources apply: %w, %s", err, stderr.String())
			}
			// 3. Restart container
			_, stderr, err = executor.ExecNamed(ctx, "WekaLocalRestart", []string{"weka", "local", "restart", "--force"})
			if err != nil {
				return fmt.Errorf("error executing weka local restart: %w, %s", err, stderr.String())
			}

			logger.Info("Setting join ips in the container spec", "join_ips", joinIps)
			if err := r.Update(ctx, container); err != nil {
				return fmt.Errorf("error updating container: %w", err)
			}
		}
	}

	return nil
}

func (r *containerReconcilerLoop) reconcileClusterStatus(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container
	pod := r.pod

	if r.pod == nil {
		return errors.New("Pod is not found")
	}

	if r.container.Status.ClusterContainerID != nil {
		return nil
	}

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

	waitDuration := time.Second * 15
	if !response.HasValue && response.Exception != nil {
		return lifecycle.NewWaitErrorWithDuration(errors.New(*response.Exception), waitDuration)
	}
	if response.Value == nil {
		return lifecycle.NewWaitErrorWithDuration(errors.New("no value in response from weka local container-get-identity"), waitDuration)
	}
	if response.Value.ClusterId == "" || response.Value.ClusterId == "00000000-0000-0000-0000-000000000000" {
		return lifecycle.NewWaitErrorWithDuration(errors.New("container is not part of the cluster yet"), waitDuration)
	}

	container.Status.ClusterContainerID = &response.Value.ContainerId
	container.Status.ClusterID = response.Value.ClusterId
	logger.InfoWithStatus(
		codes.Ok,
		"Cluster GUID and container ID are updated in WekaContainer status",
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

	// delete csi node pod
	csiNodePodNamespace, _ := util.GetPodNamespace()
	csiNodePod := &v1.Pod{}
	csiNodePod.Name = fmt.Sprintf("%s-csi-node", r.container.Name)
	csiNodePod.Namespace = csiNodePodNamespace
	_ = r.Delete(ctx, csiNodePod)

	return nil
}

func (r *containerReconcilerLoop) cleanupPersistentDir(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "cleanupPersistentDir")
	defer end()

	container := r.container

	if container.Spec.GetOverrides().SkipCleanupPersistentDir {
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

	var persistencePath string
	if r.container.Spec.PVC == nil {

		if r.node != nil && NodeIsUnschedulable(r.node) {
			err := fmt.Errorf("container node is unschedulable, cannot perform cleanup persistent dir operation")
			return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
		}
		if r.node != nil && !NodeIsReady(r.node) {
			err := fmt.Errorf("container node is not ready, cannot perform cleanup persistent dir operation")
			return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
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

		persistencePath = nodeInfo.GetHostsideContainerPersistence()
	} else {
		persistencePath = weka.PersistencePathBase + "/containers"
	}

	payload := operations.CleanupPersistentDirPayload{
		NodeName:        container.GetNodeAffinity(),
		ContainerId:     string(container.UID),
		PersistencePath: persistencePath,
	}

	op := operations.NewCleanupPersistentDirOperation(
		r.Manager,
		&payload,
		container,
		*container.ToContainerDetails(),
		container.Spec.NodeSelector,
	)
	return operations.ExecuteOperation(ctx, op)
}

func (r *containerReconcilerLoop) pickMatchingNode(ctx context.Context) (*v1.Node, error) {
	// HACK: Since we don't really know the node affinity, we will try to discover it by labels
	// Assuming, that supplied labels represent unique type of machines
	// this puts a requirement on user to separate machines by labels, which is common approach
	nodes, err := r.KubeService.GetNodes(ctx, r.container.Spec.NodeSelector)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, errors.New("no matching nodes found")
	}

	// if container has affinity, try to find node that satisfies it
	if r.container.Spec.Affinity != nil {
		for _, node := range nodes {
			if kubernetes.NodeSatisfiesAffinity(&node, r.container.Spec.Affinity) {
				return &node, nil
			}
		}
	} else {
		// if no affinity, return first node from the list
		return &nodes[0], nil
	}

	return nil, errors.New("no matching nodes found")
}

func (r *containerReconcilerLoop) GetNodeInfo(ctx context.Context) (*discovery.DiscoveryNodeInfo, error) {
	container := r.container

	if container.IsDiscoveryContainer() {
		return nil, errors.New("cannot get node info for discovery container")
	}

	nodeAffinity := container.GetNodeAffinity()
	if container.GetNodeAffinity() == "" {
		node, err := r.pickMatchingNode(ctx)
		if err != nil {
			return nil, err
		}
		nodeAffinity = weka.NodeName(node.Name)
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

	node, err := r.KubeService.GetNode(ctx, k8sTypes.NodeName(nodeName))
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

func (r *containerReconcilerLoop) getClusterContainers(ctx context.Context) ([]*weka.WekaContainer, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getClusterContainers")
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

	skipExec := false
	if r.node != nil {
		skipExec = strings.Contains(r.node.Status.NodeInfo.ContainerRuntimeVersion, "cri-o") || !NodeIsReady(r.node)
	}

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
	// a lot of assumptions here that absolutely all versions will shut down on force-stop + delete
	err = r.writeAllowForceStopInstruction(ctx, pod, skipExec)
	if err != nil && errors.Is(err, &NodeAgentPodNotRunning{}) {
		logger.Info("Node agent pod not running, skipping force stop")
	} else if err != nil {
		// do not return error, as we are deleting pod anyway
		logger.Error(err, "Error writing allow force stop instruction")
	}

	if NodeIsReady(r.node) && !skipExec {
		if r.container.HasAgent() {
			logger.Debug("Force-stopping weka local")
			// for more graceful flows(when force delete is not set), weka_runtime awaits for more specific instructions then just delete
			// for versions that do not yet support graceful shutdown touch-flag, we will force stop weka local
			// this might impact performance of shrink, but should not be affecting whole cluster deletion
			err = r.runWekaLocalStop(ctx, pod, true)
			if err != nil {
				logger.Error(err, "Error force-stopping weka local")
			}
			// we do not abort on purpose, we still should call delete even if we failed to exec
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

func (r *containerReconcilerLoop) stopAndEnsureNoPod(ctx context.Context) error {
	//TODO: Can we search pods by ownership?
	//TODO: Code duplication with force variant, for now on purpose for easier breaking apart of logic

	container := r.container

	skipExec := false
	if r.node != nil {
		skipExec = strings.Contains(r.node.Status.NodeInfo.ContainerRuntimeVersion, "cri-o") || !NodeIsReady(r.node)
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureNoPod", "skipExec", skipExec)
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

	logger.Info("Deleting pod", "pod", pod.Name)
	err = r.writeAllowStopInstruction(ctx, pod, skipExec)
	if err != nil && errors.Is(err, &NodeAgentPodNotRunning{}) {
		logger.Info("Node agent pod not running, skipping force stop")
	} else if err != nil {
		// do not return error, as we are deleting pod anyway
		logger.Error(err, "Error writing allow stop instruction")
	}

	if NodeIsReady(r.node) && !skipExec {
		if r.container.HasAgent() {
			logger.Debug("Stopping weka local")
			// for more graceful flows(when force delete is not set), weka_runtime awaits for more specific instructions then just delete
			// for versions that do not yet support graceful shutdown touch-flag, we will force stop weka local
			// this might impact performance of shrink, but should not be affecting whole cluster deletion
			err = r.runWekaLocalStop(ctx, pod, true)
			if err != nil {
				logger.Error(err, "Error force-stopping weka local")
			}
			// we do not abort on purpose, we still should call delete even if we failed to exec
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

	if r.IsNotAlignedImage() && !container.Spec.GetOverrides().UpgradeForceReplace {
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

		joinIps, _ := services.ClustersCachedInfo.GetJoinIps(ctx, string(owner.UID), owner.Name, container.Namespace)
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

func (r *containerReconcilerLoop) WaitForNodeReady(ctx context.Context) error {
	node := r.node

	if node != nil && !NodeIsReady(node) {
		err := fmt.Errorf("node %s is not ready", node.Name)

		_ = r.RecordEventThrottled(v1.EventTypeWarning, "NodeNotReady", err.Error(), time.Minute)

		// stop here, no reason to go to the next steps
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	// if node is unschedulable, just send the event
	if node != nil && NodeIsUnschedulable(node) {
		msg := fmt.Sprintf("node %s is unschedulable", node.Name)

		_ = r.RecordEventThrottled(v1.EventTypeWarning, "NodeUnschedulable", msg, time.Minute)

		return nil
	}

	return nil
}

func (r *containerReconcilerLoop) WaitForPodRunning(ctx context.Context) error {
	pod := r.pod

	if pod.Status.Phase == v1.PodRunning {
		return nil
	}

	return lifecycle.NewWaitErrorWithDuration(errors.New("Pod is not running"), time.Second*10)
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
	return r.updateContainerStatusIfNotEquals(ctx, weka.WaitForDrivers)
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
		return false, fmt.Errorf("error checking drivers loaded: %v, %s", err, stderr.String())
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

	// should not happen, but just in case
	if len(container.Status.Allocations.Drives) != container.Spec.NumDrives {
		err := fmt.Errorf("allocated drives count %d does not match requested drives count %d", len(container.Status.Allocations.Drives), container.Spec.NumDrives)
		return err
	}

	if container.Status.Stats != nil {
		if int(container.Status.Stats.Drives.DriveCounters.Active) == len(container.Status.Allocations.Drives) {
			return r.updateContainerStatusIfNotEquals(ctx, weka.Running)
		}
	}

	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		return err
	}

	timeout := time.Minute * 2
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &timeout)

	driveListoptions := services.DriveListOptions{
		ContainerId: container.Status.ClusterContainerID,
	}
	drivesAdded, err := wekaService.ListDrives(ctx, driveListoptions)
	if err != nil {
		return err
	}

	// get drives that were discovered
	// (these drives are requested in allocations and exist in kernel)
	var kDrives map[string]operations.DriveInfo
	// NOTE: used closure not to execute this function if we don't need to add any drives
	getKernelDrives := func() error {
		if kDrives == nil {
			kDrives, err = r.getKernelDrives(ctx, executor)
			if err != nil {
				return fmt.Errorf("error getting kernel drives: %v", err)
			} else {
				logger.Info("Kernel drives fetched", "drives", kDrives)
			}
		}
		return nil
	}

	drivesAddedBySerial := make(map[string]bool)
	for _, drive := range drivesAdded {
		drivesAddedBySerial[drive.SerialNumber] = true
	}

	var errs []error

	// Adding drives to weka one by one
	for _, drive := range container.Status.Allocations.Drives {
		l := logger.WithValues("drive_name", drive)

		// check if drive is already added to weka
		if _, ok := drivesAddedBySerial[drive]; ok {
			l.Info("drive is already added to weka")
			continue
		}

		l.Info("Attempting to configure drive")

		err := getKernelDrives()
		if err != nil {
			return err
		}
		if _, ok := kDrives[drive]; !ok {
			err := fmt.Errorf("drive %s not found in kernel", drive)
			l.Error(err, "Error configuring drive")
			errs = append(errs, err)
			continue
		}

		if kDrives[drive].Partition == "" {
			err := fmt.Errorf("drive %v is not partitioned", kDrives[drive])
			l.Error(err, "Error configuring drive")
			errs = append(errs, err)
			continue
		}

		l = l.WithValues("partition", kDrives[drive].Partition)
		l.Info("Verifying drive signature")
		cmd := fmt.Sprintf("hexdump -v -e '1/1 \"%%.2x\"' -s 8 -n 16 %s", kDrives[drive].Partition)
		stdout, stderr, err := executor.ExecNamed(ctx, "GetPartitionSignature", []string{"bash", "-ce", cmd})
		if err != nil {
			err = fmt.Errorf("Error getting partition signature for drive %s: %s, %v", drive, stderr.String(), err)
			errs = append(errs, err)
			continue
		}

		if stdout.String() != "90f0090f90f0090f90f0090f90f0090f" {
			l.Info("Drive has Weka signature on it, forbidding usage")
			err := fmt.Errorf("drive %s has Weka signature on it, forbidding usage", drive)
			errs = append(errs, err)
			continue
		}

		l.Info("Adding drive into system")
		// TODO: We need to login here. Maybe handle it on wekaauthcli level?
		cmd = fmt.Sprintf("weka cluster drive add %d %s", *container.Status.ClusterContainerID, kDrives[drive].DevicePath)
		_, stderr, err = executor.ExecNamed(ctx, "WekaClusterDriveAdd", []string{"bash", "-ce", cmd})
		if err != nil {
			if !strings.Contains(stderr.String(), "Device is already in use") {
				l.WithValues("stderr", stderr.String(), "command", cmd).Error(err, "Error adding drive into system")
				err = errors.Wrap(err, stderr.String())
				errs = append(errs, err)
				continue
			} else {
				l.Info("Drive already added into system")
			}
		} else {
			l.Info("Drive added into system")
			r.RecordEvent("", "DriveAdded", fmt.Sprintf("Drive %s added", drive))
		}
	}

	if len(errs) > 0 {
		err := fmt.Errorf("errors while adding drives: %v", errs)
		return err
	}

	logger.InfoWithStatus(codes.Ok, "All drives added")

	return r.updateContainerStatusIfNotEquals(ctx, weka.Running)
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
	var nfsErr *services.NfsInterfaceGroupAlreadyJoined
	if !errors.As(err, &nfsErr) {
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) getCachedActiveMounts(ctx context.Context) (*int, error) {
	if r.activeMounts != nil {
		return r.activeMounts, nil
	}

	activeMounts, err := r.getActiveMounts(ctx)
	if err != nil && errors.Is(err, &NoWekaFsDriverFound{}) {
		// if no weka fs driver found, we can assume that there are no active mounts
		val := 0
		r.activeMounts = &val
		return r.activeMounts, nil
	}
	if err != nil {
		return nil, err
	}
	r.activeMounts = activeMounts
	return r.activeMounts, nil
}

func (r *containerReconcilerLoop) getActiveMounts(ctx context.Context) (*int, error) {
	pod, err := r.findAdjacentNodeAgent(ctx, r.pod)
	if err != nil {
		return nil, err
	}

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

	// if container did not join cluster, we can skip active mounts check
	// NOTE: case - pod was stuck in Pending state and wekacontainer CR was deleted afterwards
	// we'd want to allow this container to be recreated by client reconciler
	if r.container.Status.ClusterContainerID == nil {
		return true, nil
	}

	if r.container.Spec.GetOverrides().SkipActiveMountsCheck {
		return true, nil
	}

	activeMounts, err := r.getCachedActiveMounts(ctx)
	if err != nil {
		return false, err
	}

	if activeMounts != nil && *activeMounts != 0 {
		err := fmt.Errorf("%d mounts are still active", *activeMounts)
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "ActiveMounts", err.Error(), time.Minute)

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

	var wekaPodContainer v1.Container
	wekaPodContainer, err := r.getWekaPodContainer(pod)
	if err != nil {
		return err
	}

	if container.Spec.Image != container.Status.LastAppliedImage {
		if container.Spec.GetOverrides().UpgradeForceReplace {
			if pod.GetDeletionTimestamp() != nil {
				logger.Info("Pod is being deleted, waiting")
				return errors.New("Pod is being deleted, waiting")
			}
			if wekaPodContainer.Image == container.Spec.Image {
				return nil
			}
			err := r.writeAllowForceStopInstruction(ctx, pod, false)
			if err != nil {
				logger.Error(err, "Error writing allow force stop instruction")
				return err
			}
			return r.Delete(ctx, pod)
		}

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

		if wekaPodContainer.Image != container.Spec.Image {
			if container.HasFrontend() {
				err = r.runFrontendUpgradePrepare(ctx)
				if err != nil && errors.Is(err, &NoWekaFsDriverFound{}) {
					logger.Info("No wekafs driver found, force terminating pod")
					err := r.writeAllowForceStopInstruction(ctx, pod, false)
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

func (r *containerReconcilerLoop) runFrontendUpgradePrepare(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "runFrontendUpgradePrepare")
	defer end()

	container := r.container
	pod := r.pod

	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		return err
	}

	if !container.Spec.AllowHotUpgrade {
		logger.Debug("Hot upgrade is not enabled, issuing weka local stop")
		err := r.runWekaLocalStop(ctx, pod, false)
		if err != nil {
			return err
		}
	} else {
		logger.Info("Running prepare-upgrade")
		err = r.runDriverPrepareUpgrade(ctx, executor)
		if err != nil {
			return err
		}
		logger.Debug("Hot upgrade is enabled, skipping driver removal")
	}
	return nil
}

func (r *containerReconcilerLoop) runDriverPrepareUpgrade(ctx context.Context, executor util.Exec) error {
	_, stderr, err := executor.ExecNamed(ctx, "PrepareForUpgrade", []string{"bash", "-ce", `
if lsmod | grep wekafsio; then
	if [ -e /proc/wekafs/interface ]; then
		if ! echo prepare-upgrade > /proc/wekafs/interface; then
			echo "Failed to run prepare-upgrade command attempting rmmod instead"
			rmmod wekafsio
		fi 
	fi
fi
`})
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

func (r *containerReconcilerLoop) NodeNotSet() bool {
	return r.node == nil
}

func (r *containerReconcilerLoop) getFailureDomain(ctx context.Context) *string {
	fdConfig := r.container.Spec.FailureDomain
	if fdConfig == nil {
		return nil
	}

	if fdConfig.Label != nil {
		if fd, ok := r.node.Labels[*fdConfig.Label]; ok {
			fd = handleFailureDomainValue(fd)
			return &fd
		}
		return nil
	}
	if len(fdConfig.CompositeLabels) > 0 {
		fdDomainParts := make([]string, 0, len(fdConfig.CompositeLabels))
		for _, fdLabel := range fdConfig.CompositeLabels {
			if fd, ok := r.node.Labels[fdLabel]; ok {
				fdDomainParts = append(fdDomainParts, fd)
			}
		}

		if len(fdDomainParts) == 0 {
			return nil
		}
		// concatenate failure domain parts with "-"
		fdValue := strings.Join(fdDomainParts, "-")
		fdValue = handleFailureDomainValue(fdValue)
		return &fdValue
	}

	return nil
}

func handleFailureDomainValue(fd string) string {
	// failure domain must not exceed 16 characters, and match regular expression “^(?:[^\\/]+)$“'
	// if it exceeds 16 characters, truncate it to 16 characters
	if len(fd) > 16 {
		// get 16 characters' hash value (to guarantee uniqueness)
		return getHash(fd)
	}
	// replace all "/" with "-"
	return strings.ReplaceAll(fd, "/", "-")
}

// Generates SHA-256 hash and takes the first 16 characters
func getHash(s string) string {
	hash := sha256.Sum256([]byte(s))
	return hex.EncodeToString(hash[:])[:16]
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
	if r.container.Status.Allocations == nil && !r.container.IsClientContainer() {
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

	var allocations *weka.ContainerAllocations
	if r.container.Status.Allocations != nil {
		allocations = r.container.Status.Allocations
	} else {
		// client flow
		allocations = &weka.ContainerAllocations{}

		machineIdentifierPath := r.container.Spec.GetOverrides().MachineIdentifierNodeRef
		if machineIdentifierPath == "" {
			if r.node != nil {
				// check if node has "weka.io/machine-identifier-ref" label
				// if yes - use it as machine identifier path
				if val, ok := r.node.Annotations["weka.io/machine-identifier-ref"]; ok && val != "" {
					machineIdentifierPath = r.node.Annotations["weka.io/machine-identifier-ref"]
				}
			}
		}

		if machineIdentifierPath != "" {
			uid, err := util.GetKubeObjectFieldValue[string](r.node, machineIdentifierPath)
			if err != nil {
				return fmt.Errorf("failed to get machine identifier from node: %w and path %s", err, machineIdentifierPath)
			}
			allocations.MachineIdentifier = uid
		}
	}
	allocations.NetDevices, err = getNetDevices(ctx, r.node, r.container)
	if err != nil {
		return err
	}
	var resourcesJson []byte
	resourcesJson, err = json.Marshal(allocations)
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
				// evicting for reschedule
				if err := r.ensureStateDeleting(ctx); err != nil {
					return err
				}
				return lifecycle.NewWaitError(errors.New("scheduling race, deleting current container"))
			}
		}
		// no one else is using this node, we can safely set it
	}
	return r.setNodeAffinityStatus(ctx)
}

func (r *containerReconcilerLoop) setNodeAffinityStatus(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	nodeName := r.pod.Spec.NodeName
	if nodeName == "" {
		return lifecycle.NewWaitError(errors.New("pod is not assigned to node"))
	}

	// get node before setting status - if node is not found, we will return error and retry
	// NOTE: let kuberenetes terminate pod if node is not found and get it rescheduled
	_, err := r.KubeService.GetNode(ctx, k8sTypes.NodeName(nodeName))
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("node not found: %s", nodeName)
	}

	r.container.Status.NodeAffinity = weka.NodeName(nodeName)
	r.container.Status.PrinterColumns.NodeAffinity = nodeName
	logger.Info("binding to node", "node", nodeName, "container_name", r.container.Name)
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

func (r *containerReconcilerLoop) checkContainerNotFound(ctx context.Context, localPsResponse []services.WekaLocalContainer, psErr error) error {
	container := r.container

	if len(localPsResponse) == 0 {
		return errors.New("weka local ps response is empty")
	}

	found := false
	for _, c := range localPsResponse {
		if c.Name == container.Spec.WekaContainerName {
			found = true
			break
		} else if c.Name == "envoy" && container.IsEnvoy() {
			found = true
			break
		}
	}

	if !found {
		return errors.New("weka container not found")
	}
	return nil
}

func (r *containerReconcilerLoop) reconcileWekaLocalStatus(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	wekaLocalPsTimeout := 10 * time.Second
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &wekaLocalPsTimeout)

	localContainers, err := wekaService.ListLocalContainers(ctx)
	if err != nil {
		logger.Error(err, "Error getting weka local ps")
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

			err = fmt.Errorf("weka local ps failed: %v", err)
			return lifecycle.NewWaitError(err)
		}
		return lifecycle.NewWaitError(err)
	}

	err = r.checkContainerNotFound(ctx, localContainers, err)
	if err != nil {
		if err := r.updateContainerStatusIfNotEquals(ctx, weka.Unhealthy); err != nil {
			return err
		}
		return lifecycle.NewWaitErrorWithDuration(err, 15*time.Second)
	}

	var localContainer services.WekaLocalContainer
	for _, c := range localContainers {
		if c.Name == container.Spec.WekaContainerName {
			localContainer = c
			break
		}
	}
	if localContainer.Name == "" {
		localContainer = localContainers[0]
	}
	status := localContainer.RunStatus

	// check local container status and propagate failure message (if any) as event
	internalStatus := localContainer.InternalStatus.DisplayStatus
	if internalStatus != "READY" && localContainer.LastFailure != "" && !container.IsDistMode() {
		msg := fmt.Sprintf(
			"Container is not ready, status: %s, last failure: %s (%s)",
			internalStatus, localContainer.LastFailure, localContainer.LastFailureTime,
		)
		r.RecordEventThrottled(v1.EventTypeWarning, "WekaLocalStatus", msg, time.Minute)
	}

	// skip status update for DrivesAdding
	if status == string(weka.Running) && container.Status.Status == weka.DrivesAdding {
		// update internal status if it changed (this part is for backward compatibility)
		if container.Status.InternalStatus != internalStatus {
			return fmt.Errorf("internal status changed: %s -> %s", container.Status.InternalStatus, internalStatus)
		}
		return nil
	}

	containerStatus := weka.ContainerStatus(status)
	if container.Status.Status != containerStatus || container.Status.InternalStatus != internalStatus {
		logger.Debug("Updating status", "old_status", container.Status.Status, "new_status", containerStatus, "old_internal_status", container.Status.InternalStatus, "new_internal_status", internalStatus)
		container.Status.Status = containerStatus
		container.Status.InternalStatus = internalStatus
		if err := r.Status().Update(ctx, container); err != nil {
			return err
		}
		logger.WithValues("status", status, "internal_status", internalStatus).Info("Status updated")
		return nil
	}
	return nil
}

func (r *containerReconcilerLoop) setErrorStatus(ctx context.Context, stepName string, err error) error {
	// ignore "the object has been modified" errors
	if apierrors.IsConflict(err) {
		return nil
	}

	reason := fmt.Sprintf("%sError", stepName)
	r.RecordEventThrottled(v1.EventTypeWarning, reason, err.Error(), time.Minute)

	if r.container.Status.Status == weka.Error || r.container.Status.Status == weka.Unhealthy {
		return nil
	}
	r.container.Status.Status = weka.Error
	return r.Status().Update(ctx, r.container)
}

func (r *containerReconcilerLoop) setDrivesErrorStatus(ctx context.Context, _ string, err error) error {
	r.RecordEvent(v1.EventTypeWarning, "DrivesAddingError", err.Error())

	if r.container.Status.Status == weka.DrivesAdding {
		return nil
	}
	r.container.Status.Status = weka.DrivesAdding
	return r.Status().Update(ctx, r.container)
}

func (r *containerReconcilerLoop) RecordEvent(eventtype, reason, message string) error {
	if r.container == nil {
		return fmt.Errorf("container is not set")
	}
	if eventtype == "" {
		normal := v1.EventTypeNormal
		eventtype = normal
	}

	r.Recorder.Event(r.container, eventtype, reason, message)
	return nil
}

func (r *containerReconcilerLoop) RecordEventThrottled(eventtype, reason, message string, interval time.Duration) error {
	throttler := r.ThrottlingMap.WithPartition("container/" + r.container.Name)

	if !throttler.ShouldRun(eventtype+reason, interval, util.ThrolltingSettings{EnsureStepSuccess: false}) {
		return nil
	}

	return r.RecordEvent(eventtype, reason, message)
}

func (r *containerReconcilerLoop) clearStatusOnNodeNotFound(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	nodeName := r.container.GetNodeAffinity()

	_, err := r.KubeService.GetNode(ctx, k8sTypes.NodeName(nodeName))
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Node not found, clearing status")
			err = r.clearStatus(ctx)
			if err != nil {
				return err
			}
			return lifecycle.NewWaitError(errors.New("node not found"))
		}
	}
	return nil
}

func (r *containerReconcilerLoop) deletePodIfUnschedulable(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	pod := r.pod

	unschedulable := false
	unschedulableSince := time.Time{}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == v1.PodScheduled && condition.Status == v1.ConditionFalse && condition.Reason == "Unschedulable" {
			unschedulable = true
			unschedulableSince = condition.LastTransitionTime.Time
		}
	}

	if !unschedulable {
		return nil // cleaning up only unschedulable
	}

	// relying on lastTransitionTime of Unschedulable condition
	rescheduleAfter := 1 * time.Minute
	if time.Since(unschedulableSince) > rescheduleAfter {
		logger.Debug("Pod is unschedulable, cleaning container status", "unschedulable_since", unschedulableSince)

		// clear status before deleting pod (let reconciler start from the beginning)
		if err := r.clearStatus(ctx); err != nil {
			err = fmt.Errorf("error clearing status: %w", err)
			return err
		}

		logger.Debug("Deleting pod", "pod", pod.Name)
		err := r.Delete(ctx, pod)
		if err != nil {
			logger.Error(err, "Error deleting client container")
			return err
		}
		return errors.New("Pod is unschedulable and is being deleted")
	}
	return nil
}

func (r *containerReconcilerLoop) deleteIfNoNode(ctx context.Context) error {
	container := r.container

	if container.IsMarkedForDeletion() {
		return nil
	}

	ownerRefs := container.GetOwnerReferences()
	// if no owner references, we cannot delete CRs
	// if we have owner references, we are allowed to delete CRs:
	// - for client containers - always
	// - for backend containers - only if cleanupBackendsOnNodeNotFound is set

	if len(ownerRefs) == 0 && !container.IsDriversLoaderMode() {
		// do not clean up containers without owner references
		// NOTE: allow deleting drivers loader containers
		return nil
	}

	if container.IsBackend() && !config.Config.CleanupRemovedNodes {
		return nil
	}

	affinity := r.container.GetNodeAffinity()
	if affinity != "" {
		_, err := r.KubeService.GetNode(ctx, k8sTypes.NodeName(affinity))
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
	if r.container.Spec.Mode == weka.WekaContainerModeAdhocOp && r.container.Spec.Instructions != nil {
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
		r.container.Status.Status = weka.Completed
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
	}

	updatedDrivesList := []string{}
	for drive := range seenDrives {
		updatedDrivesList = append(updatedDrivesList, drive)
	}
	newDrivesStr, _ := json.Marshal(updatedDrivesList)
	node.Annotations["weka.io/weka-drives"] = string(newDrivesStr)
	// calculate hash, based on o.node.Status.NodeInfo.BootID
	node.Annotations["weka.io/sign-drives-hash"] = domain.CalculateNodeDriveSignHash(node)

	blockedDrives := []string{}
	if blockedDrivesStr, ok := node.Annotations["weka.io/blocked-drives"]; ok {
		_ = json.Unmarshal([]byte(blockedDrivesStr), &blockedDrives)
	}

	for _, drive := range opResult.RawDrives {
		if drive.IsMounted {
			if _, ok := seenDrives[drive.SerialId]; ok {
				// We found mounted drive that previously was used for weka
				// We should block it from being used in the future
				// check if already in blocked list, and if not add it
				if !slices.Contains(blockedDrives, drive.SerialId) {
					blockedDrives = append(blockedDrives, drive.SerialId)
					logger.Info("Blocking drive", "serial_id", drive.SerialId, "reason", "drive is mounted")
				}
			}
		}
	}

	availableDrives := len(seenDrives) - len(blockedDrives)

	// Update weka.io/drives extended resource
	node.Status.Capacity["weka.io/drives"] = *resource.NewQuantity(int64(availableDrives), resource.DecimalSI)
	node.Status.Allocatable["weka.io/drives"] = *resource.NewQuantity(int64(availableDrives), resource.DecimalSI)
	//marshal blocked drives back and update annotation
	blockedDrivesStr, err := json.Marshal(blockedDrives)
	if err != nil {
		err = fmt.Errorf("error marshalling blocked drives: %w", err)
		return err
	}
	node.Annotations["weka.io/blocked-drives"] = string(blockedDrivesStr)

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

func (r *containerReconcilerLoop) getTargetContainer(ctx context.Context) (*weka.WekaContainer, error) {
	target := r.container.Spec.UploadResultsTo
	if target == "" {
		return nil, errors.New("uploadResultsTo is not set")
	}

	targetDistContainer := &weka.WekaContainer{}
	// assuming same namespace
	err := r.Get(ctx, client.ObjectKey{Name: target, Namespace: r.container.Namespace}, targetDistContainer)
	if err != nil {
		return nil, errors.Wrap(err, "error getting target dist container")
	}
	return targetDistContainer, nil
}

func (r *containerReconcilerLoop) UploadBuiltDrivers(ctx context.Context) error {
	targetDistContainer, err := r.getTargetContainer(ctx)
	if err != nil {
		return err
	}

	complete := func() error {
		r.container.Status.Status = weka.Completed
		return r.Status().Update(ctx, r.container)
	}

	// TODO: This is not a best solution, to download version, but, usable.
	// Should replace this with ad-hocy downloader container, that will use newer version(as the one who built), to download using shared storage

	executor, err := r.ExecService.GetExecutor(ctx, targetDistContainer)
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

// check if we actually can load drivers from dist service
// trigger re-build + re-upload if not
func (r *containerReconcilerLoop) uploadedDriversPeriodicCheck(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	if !r.container.IsDriversBuilder() {
		return nil
	}

	if r.container.Status.ExecutionResult == nil {
		logger.Debug("No execution result, skipping")
		return nil
	}

	results := &BuiltDriversResult{}
	err := json.Unmarshal([]byte(*r.container.Status.ExecutionResult), results)
	if err != nil {
		return err
	}

	logger.Info("Try loading drivers", "weka_version", results.WekaVersion, "kernel_signature", results.KernelSignature)

	targetDistContainer, err := r.getTargetContainer(ctx)
	if err != nil {
		return err
	}

	executor, err := r.ExecService.GetExecutor(ctx, targetDistContainer)
	if err != nil {
		return err
	}

	// assuming `weka driver pack` is supported
	downloadCmd := fmt.Sprintf(
		"weka driver download --without-agent --version %s --kernel-signature %s",
		results.WekaVersion, results.KernelSignature,
	)

	stdout, stderr, err := executor.ExecNamed(ctx, "DownloadDrivers",
		[]string{"bash", "-ce", downloadCmd},
	)
	if err != nil {
		err = fmt.Errorf("error downloading drivers: %w, stderr: %s", err, stderr.String())
		logger.Debug(err.Error())

		if strings.Contains(stderr.String(), "Failed to download the drivers") || strings.Contains(stderr.String(), "Version missing") {
			msg := "Cannot load drivers, trigger re-build and re-upload"
			logger.Info(msg)

			r.RecordEvent("", "DriversRebuild", msg)

			if err := r.clearStatus(ctx); err != nil {
				err = fmt.Errorf("error clearing builder results: %w", err)
				return err
			}
		}
		return err
	}

	logger.Debug("Drivers loaded successfully", "stdout", stdout.String())
	return nil
}

func (r *containerReconcilerLoop) clearStatus(ctx context.Context) error {
	r.container.Status = weka.WekaContainerStatus{}
	return r.Status().Update(ctx, r.container)
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
	return r.updateContainerStatusIfNotEquals(ctx, weka.Building)
}

func (r *containerReconcilerLoop) IsNotAlignedImage() bool {
	return r.container.Status.LastAppliedImage != r.container.Spec.Image
}

func (r *containerReconcilerLoop) ensurePodNotRunningState(ctx context.Context) error {
	return r.updateContainerStatusIfNotEquals(ctx, weka.PodNotRunning)
}

func (r *containerReconcilerLoop) updateAdhocOpStatus(ctx context.Context) error {
	if r.pod.Status.Phase == v1.PodRunning {
		return r.updateContainerStatusIfNotEquals(ctx, weka.PodRunning)
	}
	return nil
}

func (r *containerReconcilerLoop) updateContainerStatusIfNotEquals(ctx context.Context, newStatus weka.ContainerStatus) error {
	if r.container.Status.Status != newStatus {
		r.container.Status.Status = newStatus
		err := r.Status().Update(ctx, r.container)
		if err != nil {
			err := fmt.Errorf("failed to update container status: %w", err)
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
	if r.pod == nil {
		return errors.New("on pod is set")
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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	// submit http request to metrics pod
	agentPod, err := r.findAdjacentNodeAgent(ctx, r.pod)
	if err != nil {
		return err
	}

	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		return err
	}

	payload := node_agent.GetContainerInfoRequest{ContainerId: string(r.container.GetUID())}

	url := "http://" + agentPod.Status.PodIP + ":8090/getContainerInfo"

	// Convert the payload to JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return errors.Wrap(err, "Error marshalling payload")
	}

	// limit the getContainerInfo request
	timeout := config.Config.Metrics.Containers.RequestsTimeouts.GetContainerInfo
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := util.SendJsonRequest(ctx, url, jsonData, util.RequestOptions{AuthHeader: "Token " + token})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("getContainerInfo failed: %s, status: %d", string(body), resp.StatusCode)
		return err
	}

	var response node_agent.ContainerInfoResponse
	err = json.NewDecoder(bytes.NewReader(body)).Decode(&response)
	if err != nil {
		logger.Error(err, "Error decoding response body", "body", string(body))
		return err
	}

	patch := client.MergeFrom(r.container.DeepCopy())

	r.container.Status.Stats = &response.ContainerMetrics
	if r.container.Status.PrinterColumns == nil {
		r.container.Status.PrinterColumns = &weka.ContainerPrinterColumns{}
	}

	activeMounts, _ := r.getCachedActiveMounts(ctx)

	if r.container.HasFrontend() && activeMounts != nil {
		r.container.Status.PrinterColumns.ActiveMounts = weka.StringMetric(fmt.Sprintf("%d", *activeMounts))
		r.container.Status.Stats.ActiveMounts = weka.IntMetric(int64(*activeMounts))
	}

	r.container.Status.Stats.Processes.Desired = weka.IntMetric(int64(r.container.Spec.NumCores) + 1)
	if r.container.IsDriveContainer() {
		r.container.Status.Stats.Drives.DriveCounters.Desired = weka.IntMetric(int64(r.container.Spec.NumDrives))
		r.container.Status.PrinterColumns.Drives = weka.StringMetric(r.container.Status.Stats.Drives.DriveCounters.String())
	}
	r.container.Status.PrinterColumns.Processes = weka.StringMetric(r.container.Status.Stats.Processes.String())
	r.container.Status.Stats.LastUpdate = metav1.NewTime(time.Now())

	TracedPatch := func() error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "PatchContainerStatus")
		defer end()
		ret := r.Status().Patch(ctx, r.container, patch)
		if ret != nil {
			logger.SetError(ret, "Error patching container status")
		}
		return nil
	}

	return TracedPatch()
}

func (r *containerReconcilerLoop) ShouldDeactivate() bool {
	if !r.container.IsBackend() {
		return false
	}

	if r.container.Spec.GetOverrides().SkipDeactivate {
		return false
	}

	if !r.container.IsDeletingState() {
		return false
	}

	if r.container.Status.ClusterContainerID == nil {
		return false
	}
	// should deactivate does not represent if deactivation was done or not
	// only if it needs to be deactivated
	// this ensures us that handle deletion wont be reached until deactivation is done, if it should be done
	return true

}

func (r *containerReconcilerLoop) CanSkipDeactivate() bool {
	return r.container.Spec.Overrides.SkipDeactivate
}

func (r *containerReconcilerLoop) CanSkipDrivesForceResign() bool {
	return r.container.Spec.GetOverrides().SkipDrivesForceResign
}

func (r *containerReconcilerLoop) RegisterContainerOnMetrics(ctx context.Context) error {
	scrapeTargets := []node_agent.ScrapeTarget{}

	if r.container.Spec.Mode == weka.WekaContainerModeEnvoy {
		if r.container.Spec.ExposedPorts != nil {
			for _, port := range r.container.Spec.ExposedPorts {
				if port.Name == "envoy-admin" {
					scrapeTargets = append(scrapeTargets, node_agent.ScrapeTarget{
						Port:    int(port.ContainerPort),
						Path:    "/stats/prometheus",
						AppName: "weka_s3_envoy",
					})
				}
			}
		}
	}

	// find a pod service node metrics
	payload := node_agent.RegisterContainerPayload{
		ContainerName:     r.container.Name,
		ContainerId:       string(r.container.GetUID()),
		WekaContainerName: r.container.Spec.WekaContainerName,
		Labels:            r.container.GetLabels(),
		Mode:              r.container.Spec.Mode,
		ScrapeTargets:     scrapeTargets,
	}
	// submit http request to metrics pod
	agentPod, err := r.findAdjacentNodeAgent(ctx, r.pod)
	if err != nil {
		return err
	}

	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		return err
	}

	url := "http://" + agentPod.Status.PodIP + ":8090/register"

	// Convert the payload to JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return errors.Wrap(err, "Error marshalling payload")
	}

	// limit the register request
	timeout := config.Config.Metrics.Containers.RequestsTimeouts.Register
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := util.SendJsonRequest(ctx, url, jsonData, util.RequestOptions{AuthHeader: "Token " + token})
	if err != nil {
		return errors.Wrap(err, "Error sending register request")
	}

	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("error sending register request, status: " + resp.Status)
	}

	return nil
}

func (r *containerReconcilerLoop) getNodeAgentPods(ctx context.Context) ([]v1.Pod, error) {
	if r.node == nil {
		return nil, errors.New("Node is not set")
	}

	if !NodeIsReady(r.node) {
		err := errors.New("Node is not ready")
		return nil, lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
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

	if container.Status.Status != weka.Running {
		logger.Info("Container is not running yet")
		return errors.New("Container is not running yet")
	}

	container.Status.LastAppliedImage = container.Spec.Image
	return r.Status().Update(ctx, container)
}

func (r *containerReconcilerLoop) HasNodeAffinity() bool {
	return r.container.GetNodeAffinity() != ""
}

func (r *containerReconcilerLoop) dropStopAttemptRecord(ctx context.Context) error {
	// clear r.container.Status.Timestamps[TimestampStopAttempt
	if r.container.Status.Timestamps == nil {
		return nil
	}
	if _, ok := r.container.Status.Timestamps[string(weka.TimestampStopAttempt)]; !ok {
		return nil
	}
	delete(r.container.Status.Timestamps, string(weka.TimestampStopAttempt))
	return r.Status().Update(ctx, r.container)
}

func (r *containerReconcilerLoop) IsStatusOvervwritableByLocal() bool {
	// we do not want to overwrite this statuses, as they proxy some higher-level state
	if slices.Contains(
		[]weka.ContainerStatus{
			weka.Deleting,
			weka.Destroying,
			weka.Draining,
			weka.PodTerminating,
		}, r.container.Status.Status) {
		return false
	}
	return true
}

func (r *containerReconcilerLoop) migrateEnsurePorts(ctx context.Context) error {
	if r.container.Spec.ExposePorts == nil {
		return nil
	}

	if !r.container.IsEnvoy() {
		return nil // we never set old format for anything but envoy
	}

	if len(r.container.Spec.ExposePorts) == 2 {
		r.container.Spec.ExposedPorts = []v1.ContainerPort{
			{
				Name:          "envoy",
				ContainerPort: int32(r.container.Spec.ExposePorts[0]),
				HostPort:      int32(r.container.Spec.ExposePorts[0]),
			},
			{
				Name:          "envoy-admin",
				ContainerPort: int32(r.container.Spec.ExposePorts[1]),
				HostPort:      int32(r.container.Spec.ExposePorts[1]),
			},
		}

		r.container.Spec.ExposePorts = nil
		return r.Update(ctx, r.container)
	}

	return nil
}

func (r *containerReconcilerLoop) waitForMountsOrDrain(ctx context.Context) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "waitForMountsOrDrain")
	defer end()

	if r.node == nil {
		// no reason to wait for mounts if node does not exist
		_ = r.RecordEventThrottled(v1.EventTypeNormal, "NodeNotFound", "Node is not found", time.Minute)
		return nil
	}

	// TODO: This logic should become native FE logic
	// meanwhile we are working around on operator side
	// if container is being deleted and pos is still alive - we should ensnure no mounts, and drain if drain flag is set to true

	mounts, err := r.getCachedActiveMounts(ctx)
	if err != nil {
		return err
	}
	if mounts == nil {
		err := errors.New("Mounts are not set")
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "ActiveMounts", err.Error(), time.Minute)
		return err
	}

	if *mounts == 0 {
		return nil
	} else {
		if r.container.Spec.GetOverrides().ForceDrain {
			if err := r.invokeDrain(ctx); err != nil {
				return err
			}
			if r.container.Spec.GetOverrides().UmountOnHost {
				if err := r.invokeForceUmountOnHost(ctx); err != nil {
					return err
				}
			}
		}
		err := fmt.Errorf("%d mounts are still active", *mounts)
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "ActiveMounts", err.Error(), time.Minute)

		return lifecycle.NewWaitErrorWithDuration(err, 15*time.Second)
	}
}

func (r *containerReconcilerLoop) invokeDrain(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "invokeDrain")
	defer end()

	if r.pod == nil {
		return errors.New("Pod is not set, cannot drain")
	}

	executor, err := r.ExecService.GetExecutor(ctx, r.container)
	if err != nil {
		return err
	}
	logger.Warn("invoking drain")
	stdout, stderr, err := executor.ExecNamed(ctx, "DrainDriver", []string{"bash", "-ce", "weka local stop --force && echo drain > /proc/wekafs/interface"})
	if err != nil {
		logger.Error(err, "Error invoking drain", "stdout", stdout.String(), "stderr", stderr.String())
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) invokeForceUmountOnHost(ctx context.Context) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "invokeForceUmountOnHost")
	defer end()
	if r.pod == nil {
		return errors.New("Pod is not set, cannot umount")
	}

	op := umount.NewUmountOperation(
		r.Manager,
		r.container,
	)

	err := operations.ExecuteOperation(ctx, op)
	if err != nil {
		return err
	}
	return op.Cleanup(ctx)
}

func (r *containerReconcilerLoop) checkTolerations(ctx context.Context) error {
	notTolerated := !util.CheckTolerations(r.node.Spec.Taints, r.container.Spec.Tolerations)

	if notTolerated == r.container.Status.NotToleratedOnReschedule {
		return nil
	}

	r.container.Status.NotToleratedOnReschedule = notTolerated
	return r.Status().Update(ctx, r.container)
}

func (r *containerReconcilerLoop) MigratePVC(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "MigratePVC")
	defer end()

	logger.Info("Starting PVC migration operation")

	op := tempops.NewPvcMigrateOperation(r.Manager, r.container)
	err := operations.ExecuteOperation(ctx, op)
	if err != nil {
		logger.Error(err, "PVC migration operation failed")
		return err // Keep retrying until job succeeds or fails definitively
	}

	logger.Info("PVC migration job completed successfully")

	// After successful migration, update the container spec to remove PVC reference
	// and potentially set a flag indicating migration is done.
	// This prevents re-running the migration on subsequent reconciles.
	// This will update also containers like dist service, which is yaml controlled
	// While this is not desired, for cases we plan to use it this should be fine
	patch := client.MergeFrom(r.container.DeepCopy())
	r.container.Spec.PVC = nil
	// Optionally add an annotation or status field to mark migration complete
	// if r.container.Annotations == nil {
	// 	r.container.Annotations = make(map[string]string)
	// }
	// r.container.Annotations["weka.io/pvc-migrated"] = "true"

	err = r.Patch(ctx, r.container, patch)
	if err != nil {
		logger.Error(err, "Failed to patch container spec after PVC migration")
		return errors.Wrap(err, "failed to patch container spec after PVC migration")
	}

	logger.Info("Container spec updated to remove PVC reference")
	// Returning an error here forces a requeue, allowing the reconciler to
	// proceed with pod creation using the updated spec (without PVC).
	return lifecycle.NewExpectedError(errors.New("requeue after successful PVC migration and spec update"))
}

func (r *containerReconcilerLoop) DeployCsiNodeServerPod(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureCsiNodeServerPod")
	defer end()

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return errors.New("node affinity is not set")
	}

	namespace, err := util.GetPodNamespace()
	if err != nil {
		return err
	}

	csiDriverName := r.container.Spec.CsiDriverName
	if csiDriverName == "" {
		return errors.New("failed to get csi driver name from WekaContainer spec")
	}

	csiNodeName := fmt.Sprintf("%s-csi-node", r.container.Name)

	pod := &v1.Pod{}
	err = r.Get(ctx, client.ObjectKey{Name: csiNodeName, Namespace: namespace}, pod)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Creating CSI node pod")
			podSpec := csi.NewCsiNodePod(csiNodeName, namespace, csiDriverName, string(nodeName), r.container.Spec.Tolerations)
			if err = r.Create(ctx, podSpec); err != nil {
				if !apierrors.IsAlreadyExists(err) {
					return errors.Wrap(err, "failed to create CSI node pod")
				}
			}
			return r.Update(ctx, r.container)
		} else {
			return errors.Wrap(err, "failed to get CSI node pod")
		}
	} else {
		return csi.CheckAndDeleteOutdatedCsiNode(ctx, pod, r.Client, csiDriverName, r.container.Spec.Tolerations)
	}
}

func (r *containerReconcilerLoop) CleanupCsiNodeServerPod(ctx context.Context) error {
	namespace, err := util.GetPodNamespace()
	if err != nil {
		return err
	}
	csiNodeName := fmt.Sprintf("%s-csi-node", r.container.Name)
	if err = r.Delete(ctx, &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csiNodeName,
			Namespace: namespace,
		},
	}); err != nil {
		return fmt.Errorf("failed to delete CSI node pod %s: %w", csiNodeName, err)
	}
	return nil
}
