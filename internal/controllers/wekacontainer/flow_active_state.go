package wekacontainer

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/pkg/util/podexec"
)

// ActiveStateFlow returns the steps for a container in the active state
func ActiveStateFlow(r *containerReconcilerLoop) []lifecycle.Step {
	// First part of the flow
	steps := []lifecycle.Step{
		&lifecycle.SimpleStep{
			// TODO: check if this is still needed
			Run: r.migrateEnsurePorts,
			Predicates: lifecycle.Predicates{
				func() bool {
					return len(r.container.Spec.ExposePorts) != 0
				},
			},
		},
		// put self in state "deleting" if container is marked for deletion
		&lifecycle.SimpleStep{
			Run: r.ensureStateDeleting,
			Predicates: lifecycle.Predicates{
				r.container.IsMarkedForDeletion,
				lifecycle.IsNotFunc(r.container.IsDeletingState),
				lifecycle.IsNotFunc(r.container.IsDestroyingState),
			},
		},
		&lifecycle.SimpleStep{
			Run: r.GetNode,
		},
		&lifecycle.SimpleStep{
			Name: "ReconcileAwsTerminationLifecycle",
			Run:  r.reconcileAwsTerminationLifecycle,
			Predicates: lifecycle.Predicates{
				r.NodeIsAwsProvider,
			},
			ContinueOnError: true,
		},
		&lifecycle.SimpleStep{
			Run: r.GetWekaClient,
			Predicates: lifecycle.Predicates{
				r.WekaContainerManagesCsi,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.FetchTargetCluster,
			Predicates: lifecycle.Predicates{
				func() bool {
					return r.wekaClient != nil && r.wekaClient.Spec.TargetCluster.Name != ""
				},
				lifecycle.BoolValue(config.Config.Csi.Enabled),
			},
		},
		&lifecycle.SimpleStep{
			Run: r.refreshPod,
		},
	}

	metricsSteps := MetricsSteps(r)

	csiSteps := CsiSteps(r)

	// Second part of the flow
	steps2 := []lifecycle.Step{
		&lifecycle.SimpleStep{
			Run: r.initState,
		},
		&lifecycle.SimpleStep{
			Run: r.deleteIfNoNode,
		},
		// Before any mismatch check below can hold or retire this container: claim the node for the
		// csi-node plugin. Asserting it here, ahead of the checks, means the claim is already in place
		// whenever a user removes the client-selector label — the DaemonSet controller reacts to that
		// within milliseconds, far quicker than we could respond to it.
		&lifecycle.SimpleStep{
			Name: "ManageCsiNodeRetainLabel",
			Run:  r.ManageCsiNodeRetainLabel,
			Predicates: lifecycle.Predicates{
				r.WekaContainerManagesCsi,
				r.NodeIsSet,
			},
			ContinueOnError: true,
		},
		&lifecycle.SimpleStep{
			Run: r.deleteIfTolerationsMismatch,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.NodeNotSet),
				lifecycle.BoolValue(config.Config.CleanupContainersOnTolerationsMismatch),
			},
		},
		&lifecycle.SimpleStep{
			Run: r.deleteIfNodeSelectorMismatch,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.NodeNotSet),
				func() bool {
					// Backends and clients are subject to container-level node selector mismatch cleanup.
					// Both have NodeSelector propagated from their parent (WekaCluster/WekaClient).
					//
					// Note: Clients use NodeAffinity for scheduling (specific node name), but NodeSelector
					// is still propagated for validation purposes. Empty NodeSelector matches all nodes,
					// so containers created before NodeSelector propagation was added won't be affected.
					//
					// Aux containers (envoy, telemetry, drivers, operations) are NOT cleaned up
					// on node selector mismatch - they follow their parent containers.
					if r.container.IsBackend() {
						return config.Config.CleanupBackendsOnNodeSelectorMismatch
					}
					if r.container.IsClientContainer() {
						return config.Config.CleanupClientsOnNodeSelectorMismatch
					}
					return false
				},
			},
		},
		&lifecycle.SimpleStep{
			Run: r.ensureFinalizer,
		},
		&lifecycle.SimpleStep{
			Run: r.ensureBootConfigMapInTargetNamespace,
		},
		&lifecycle.SimpleStep{
			Run: r.updatePodMetadataOnChange,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.PodNotSet),
				r.podMetadataChanged,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.updatePodTolerationsOnChange,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.PodNotSet),
			},
		},
		&lifecycle.SimpleStep{
			// in case pod gracefully went down, we dont want to deactivate, and we will drop timestamp once pod comes back
			Run: r.dropStopAttemptRecord,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.PodNotSet),
				func() bool {
					return r.container.IsDriveContainer() || r.container.IsComputeContainer()
				},
				func() bool {
					return r.pod.DeletionTimestamp == nil
				},
			},
		},
		&lifecycle.SimpleStep{
			// A backend pod that has exited (terminal phase) no longer needs the do-not-force-delete
			// finalizer; strip it and reap the dead object so a fresh pod is recreated
			Name: "ReapExitedBackendPod",
			Run:  r.reapExitedBackendPod,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.PodNotSet),
				r.container.IsBackend,
				r.NodeIsSet,
				func() bool {
					return r.pod.Status.Phase == v1.PodSucceeded || r.pod.Status.Phase == v1.PodFailed
				},
			},
		},
		&lifecycle.SimpleStep{
			Run: r.handlePodTermination,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.PodNotSet),
				func() bool {
					return r.pod.DeletionTimestamp != nil
				},
			},
		},
		&lifecycle.SimpleStep{
			Run: r.deleteEnvoyIfNoS3Neighbor,
			Predicates: lifecycle.Predicates{
				r.container.IsEnvoy,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.handleTelemetryComputeNeighbor,
			Predicates: lifecycle.Predicates{
				r.container.IsTelemetry,
			},
		},
		&lifecycle.SimpleStep{
			// let drivers being re-built if node with drivers container is not found
			Run: r.clearStatusOnNodeNotFound,
			Predicates: lifecycle.Predicates{
				r.container.IsDriversContainer,
				// only clear status if we have node affinity set in status, but not in spec
				func() bool {
					return r.container.Spec.NodeAffinity == "" && r.container.Status.NodeAffinity != ""
				},
				r.NodeNotSet,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.uploadedDriversPeriodicCheck,
			Predicates: lifecycle.Predicates{
				r.container.IsOneOff,
				r.ResultsAreProcessed,
				r.container.IsDriversBuilder,
			},
			Throttling: &throttling.ThrottlingSettings{
				Interval:          config.Consts.CheckDriversInterval,
				EnsureStepSuccess: true,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.cleanupFinishedOneOff,
			Predicates: lifecycle.Predicates{
				r.container.IsOneOff,
				r.ResultsAreProcessed,
			},
			FinishOnSuccess: true,
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name:   condition.CondContainerImageUpdated,
				Reason: "ImageUpdate",
			},
			Run: r.handleSpecVersionMismatch,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.PodNotSet),
			},
			SkipStepStateCheck: true,
		},
		&lifecycle.SimpleStep{
			Run: r.EnsureDrivers,
			Predicates: lifecycle.Predicates{
				r.container.RequiresDrivers,
				lifecycle.IsNotFunc(r.container.IsMarkedForDeletion),
				r.HasNodeAffinity, // if we dont have node set yet we can't load drivers, but we do want to load before creating pod if we have affinity
			},
		},
		&lifecycle.SimpleStep{
			Run: r.AllocateNICs,
			Predicates: lifecycle.Predicates{
				r.ShouldAllocateNICs,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.validateNetworkConfig,
			Predicates: lifecycle.Predicates{
				r.PodNotSet,
			},
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.CondContainerMigratedOutFromPVC,
			},
			Run: r.MigratePVC,
			Predicates: lifecycle.Predicates{
				r.PodNotSet,
				func() bool {
					return r.container.Spec.PVC != nil && r.container.Spec.GetOverrides().MigrateOutFromPvc
				},
			},
		},
		&lifecycle.SimpleStep{
			Name: "ReportNodeUnschedulable",
			Run: func(ctx context.Context) error {
				msg := fmt.Sprintf("node %s is unschedulable", r.node.Name)

				return r.RecordEventThrottled(v1.EventTypeWarning, "NodeUnschedulable", msg, time.Minute)
			},
			Predicates: lifecycle.Predicates{
				func() bool { return NodeIsUnschedulable(r.node) },
			},
			ContinueOnError: true,
		},
		// Ensure SSD proxy container exists before setting proxy UID (for drive sharing)
		&lifecycle.SimpleStep{
			Run: r.ensureProxyContainer,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				r.container.UsesDriveSharing,
				r.HasNodeAffinity,
			},
		},
		// Wait for the SSD proxy container to be up (Running + READY) before scheduling the drive pod
		&lifecycle.SimpleStep{
			Run: r.waitForProxyReady,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				r.container.UsesDriveSharing,
				r.HasNodeAffinity,
			},
		},
		// Ensure the DRA DeviceClass and this container's ResourceClaim exist before the pod
		// references them, when NUMA confinement uses the "dra" method. Gated on PodNotSet like
		// ensurePod itself below: this is pod-creation-time wiring, not a steady-state
		// reconciliation concern once the pod exists — a live pod already has its claim reserved,
		// and the recreate-on-drift path in ensureNumaResourceClaimForCPUCount is deliberately
		// conservative about touching a claim that's still in use (see its ReservedFor check).
		&lifecycle.SimpleStep{
			Run: r.ensureNumaDraClaim,
			Predicates: lifecycle.Predicates{
				r.needsNumaDraClaim,
				r.PodNotSet,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.ensurePod,
			Predicates: lifecycle.Predicates{
				r.PodNotSet,
			},
			OnFail: r.setErrorStatus,
		},
		&lifecycle.SimpleStep{
			Run: r.deletePodIfUnschedulable,
			Predicates: lifecycle.Predicates{
				func() bool {
					// do not delete pod if node affinity is set on wekacontainer's spec
					return r.pod.Status.Phase == v1.PodPending && r.container.Spec.NodeAffinity == ""
				},
			},
		},
		&lifecycle.SimpleStep{
			Run: r.reportAdhocPodNotProgressing,
			Predicates: lifecycle.Predicates{
				r.container.IsAdhocOpContainer,
				r.PodIsSet,
				r.adhocPodNotProgressing,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.deleteStuckAdhocContainer,
			Predicates: lifecycle.Predicates{
				r.container.IsAdhocOpContainer,
				r.PodIsSet,
				r.adhocPodNotProgressing,
				r.adhocPodStuckTimeoutElapsed,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.checkPodUnhealthy,
			Predicates: lifecycle.Predicates{
				func() bool {
					return r.container.Status.Status != weka.Unhealthy
				},
			},
		},
		&lifecycle.SimpleStep{
			Run: r.ensurePodNotRunningState,
			Predicates: lifecycle.Predicates{
				r.PodNotRunning,
				func() bool { return r.pod.DeletionTimestamp == nil },
			},
		},
		&lifecycle.SimpleStep{
			Run:   r.enforceNodeAffinity,
			State: &lifecycle.State{Name: condition.CondContainerAffinitySet},
			Predicates: lifecycle.Predicates{
				r.container.MustHaveNodeAffinity,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.setNodeAffinityStatus,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.HasStatusNodeAffinity),
			},
		},
		&lifecycle.SimpleStep{
			Run: r.deletePodIfNodeInfoMismatch,
			Predicates: lifecycle.Predicates{
				r.PodIsSet,
				r.HasStatusNodeAffinity,
				// Skip for Running containers to avoid disrupting live workloads;
				// the check fires naturally on the next restart/reconcile cycle.
				func() bool { return r.container.Status.Status != weka.Running },
				func() bool { return r.pod.DeletionTimestamp == nil },
			},
		},
		&lifecycle.SimpleStep{
			Run: r.reconcileProxyHugepagesSpec,
			Predicates: lifecycle.Predicates{
				r.container.IsSSDProxyContainer,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.EnsureDrivers, // drivers might be off at this point if we had to wait for node affinity
			Predicates: lifecycle.Predicates{
				r.container.RequiresDrivers,
				r.HasNodeAffinity, // if we dont have node set yet we can't load drivers, but we do want to load before creating pod if we have affinity
			},
		},
		&lifecycle.SimpleStep{
			Run: r.HandleNodeNotReady,
		},
		&lifecycle.SimpleStep{
			Run: r.WaitForPodRunning,
		},
		// For drive containers in full-drives mode: ensure the weka-full-drives annotation
		// is present on the node before proceeding. Triggers NewDiscoverDrivesOperation if absent.
		// Gated on annotation absence (not Allocations == nil) so it also fires for already-allocated
		// containers that lack the annotation (e.g. after upgrade from an older operator).
		// Discovery writes [] if no kernel-visible drives are found — that empty-but-present annotation
		// is the signal for UpdateFullDrivesAnnotationFromAddedDrives below to merge in-Weka drives.
		&lifecycle.SimpleStep{
			Run: r.EnsureNodeFullDrivesAnnotation,
			Predicates: lifecycle.Predicates{
				func() bool {
					return r.container.IsDriveContainer() && !r.container.UsesDriveSharing()
				},
				r.HasNodeAffinity,
				lifecycle.IsNotFunc(r.NodeNotSet),
				lifecycle.IsNotFunc(r.NodeHasFullDrivesAnnotation),
			},
		},
		// Backend containers allocate their own resources
		&lifecycle.SimpleStep{
			Run: r.AllocateResources,
			State: &lifecycle.State{
				Name: condition.CondContainerResourcesAllocated,
			},
			Predicates: lifecycle.Predicates{
				r.container.IsAllocatable,
				func() bool {
					return r.container.Status.Allocations == nil
				},
			},
		},
		&lifecycle.SimpleStep{
			Run: r.AllocateDrivesIfNeeded,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				r.NeedsDrivesToAllocate,
			},
			ContinueOnError: true,
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{Name: condition.CondContainerResourcesWritten},
			Run:   r.WriteResources,
			Predicates: lifecycle.Predicates{
				lifecycle.Or(
					r.container.IsAllocatable,
					r.container.IsClientContainer, // nics/machine-identifiers
				),
			},
		},
		&lifecycle.SimpleStep{
			Run: r.checkUnhealyPodResources,
			Predicates: lifecycle.Predicates{
				lifecycle.Or(
					r.container.IsAllocatable,
					r.container.IsClientContainer, // nics/machine-identifiers
				),
				func() bool {
					return r.container.Status.Status == weka.Unhealthy
				},
			},
			ContinueOnError: true,
		},
		&lifecycle.SimpleStep{
			Run: r.updateDriversBuilderStatus,
			Predicates: lifecycle.Predicates{
				r.container.IsDriversBuilder,
				lifecycle.IsNotFunc(r.container.IsDistMode), // TODO: legacy "dist" mode is currently used both for building drivers and for distribution
				lifecycle.IsNotFunc(r.ResultsAreProcessed),
			},
		},
		&lifecycle.SimpleStep{
			Run: r.setPodRunningStatus,
			Predicates: lifecycle.Predicates{
				lifecycle.Or(r.container.IsAdhocOpContainer, r.container.IsDiscoveryContainer),
				func() bool {
					return r.container.Status.Status != weka.PodRunning
				},
			},
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{Name: condition.CondResultsReceived},
			Run:   r.fetchResults,
			Predicates: lifecycle.Predicates{
				r.container.IsOneOff,
			},
			SkipStepStateCheck: false,
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{Name: condition.CondResultsProcessed},
			Run:   r.processResults,
			Predicates: lifecycle.Predicates{
				r.container.IsOneOff,
			},
		},
		&lifecycle.SimpleStep{
			Name: "ReconcileManagementIPs",
			Run:  r.reconcileManagementIPs,
			Predicates: lifecycle.Predicates{
				func() bool {
					// we don't want to reconcile management IPs for containers that are already Running
					return len(r.container.Status.GetManagementIps()) == 0 && r.container.Status.Status != weka.Running
				},
				func() bool {
					return r.container.IsBackend() || r.container.IsClientContainer()
				},
			},
			OnFail: r.setErrorStatus,
		},
		&lifecycle.SimpleStep{
			Name: "PeriodicReconcileManagementIPs",
			Run:  r.reconcileManagementIPs,
			Predicates: lifecycle.Predicates{
				func() bool {
					// we want to periodically reconcile management IPs for containers that are already Running
					return r.container.Status.Status == weka.Running
				},
				func() bool {
					return r.container.IsBackend() || r.container.IsClientContainer()
				},
			},
			Throttling: &throttling.ThrottlingSettings{
				Interval: time.Minute * 3,
			},
			ContinueOnError: true,
		},
		&lifecycle.SimpleStep{
			Name: "ReconcileWekaLocalStatus",
			Run:  r.reconcileWekaLocalStatus,
			Predicates: lifecycle.Predicates{
				r.container.IsWekaContainer,
				r.PodIsSet,
			},
			OnFail: r.setErrorStatus,
		},
		&lifecycle.SimpleStep{
			Name: "DeleteEnvoyIfProcessNotExists",
			Run:  r.deleteEnvoyIfProcessNotExists,
			Predicates: lifecycle.Predicates{
				r.container.IsEnvoy,
				r.PodIsSet,
				func() bool {
					return r.container.Status.Status == weka.Error
				},
			},
			ContinueOnError: true,
		},
		&lifecycle.SimpleStep{
			Run: r.setJoinIpsIfStuckInStemMode,
			Predicates: lifecycle.Predicates{
				r.container.ShouldJoinCluster,
				func() bool {
					return r.container.Status.ClusterContainerID == nil && len(r.container.Spec.JoinIps) == 0
				},
				func() bool {
					return r.container.Status.InternalStatus == "STEM"
				},
			},
		},
		&lifecycle.SimpleStep{
			Run: r.applyCurrentImage,
			Predicates: lifecycle.Predicates{
				r.IsNotAlignedImage,
			},
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name:    condition.CondJoinedCluster,
				Message: "Container joined cluster",
			},
			Run: r.reconcileClusterStatus,
			Predicates: lifecycle.Predicates{
				r.container.ShouldJoinCluster,
			},
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.CondVirtualDrivesAdded,
			},
			Run: r.AddVirtualDrives,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				func() bool {
					return r.container.UsesDriveSharing()
				},
			},
		},
		&lifecycle.SimpleStep{
			Name: "AddVirtualDrivesPeriodic",
			Run:  r.AddVirtualDrives,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				func() bool {
					return r.container.UsesDriveSharing()
				},
			},
			Throttling: &throttling.ThrottlingSettings{
				Interval:          config.Consts.PeriodicDrivesCheckInterval,
				EnsureStepSuccess: true,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.UpdateWekaAddedDrives,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				func() bool {
					return r.container.Status.InternalStatus == "READY"
				},
				r.NeedWekaDrivesListUpdate,
			},
		},
		// After updating AddedDrives from Weka, merge in-Weka drives into the weka-full-drives
		// annotation. Gated on annotation being present (not empty) — EnsureNodeFullDrivesAnnotation
		// above always runs first and writes at least [] when no kernel-visible drives are found,
		// so a present annotation means discovery has completed for this node.
		&lifecycle.SimpleStep{
			Run: r.UpdateFullDrivesAnnotationFromAddedDrives,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				lifecycle.IsNotFunc(r.container.UsesDriveSharing),
				lifecycle.IsNotFunc(r.NodeNotSet),
				r.NodeHasFullDrivesAnnotation,
				func() bool {
					return len(r.container.Status.AddedDrives) > 0
				},
			},
		},
		&lifecycle.SimpleStep{
			Run: r.MarkDrivesForRemoval,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				r.AddedDrivesNotAlignedWithAllocations,
				func() bool { return config.Config.RemoveFailedDrivesFromWeka },
			},
		},
		&lifecycle.SimpleStep{
			Run: r.RemoveDrives,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
			},
			ContinueOnError: true,
			Throttling: &throttling.ThrottlingSettings{
				Interval:                    config.Consts.PeriodicDrivesCheckInterval,
				DisableRandomPreSetInterval: true,
				EnsureStepSuccess:           true,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.RemoveDrivesByPhysicalUuids,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				func() bool {
					return r.container.UsesDriveSharing()
				},
			},
			ContinueOnError: true,
			Throttling: &throttling.ThrottlingSettings{
				Interval:                    config.Consts.PeriodicDrivesCheckInterval,
				DisableRandomPreSetInterval: true,
				EnsureStepSuccess:           true,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.RemoveDrivesByVirtualUuids,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				func() bool {
					return r.container.UsesDriveSharing()
				},
			},
			ContinueOnError: true,
			Throttling: &throttling.ThrottlingSettings{
				Interval:                    config.Consts.PeriodicDrivesCheckInterval,
				DisableRandomPreSetInterval: true,
				EnsureStepSuccess:           true,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.EnsureDrives,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				r.HasDrivesToAdd,
			},
			OnFail: r.setDrivesErrorStatus,
			Throttling: &throttling.ThrottlingSettings{
				Interval:                    config.Consts.PeriodicDrivesCheckInterval,
				DisableRandomPreSetInterval: true,
				EnsureStepSuccess:           true,
			},
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name:    condition.CondJoinedS3Cluster,
				Message: "Joined s3 cluster",
			},
			Run: r.JoinS3Cluster,
			Predicates: lifecycle.Predicates{
				r.container.IsS3Container,
				r.container.HasJoinIps,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.EnsureNfsInterfaceGroupPorts,
			Predicates: lifecycle.Predicates{
				r.container.IsNfsContainer,
				r.container.HasJoinIps,
			},
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name:    condition.CondJoinedSmbwCluster,
				Message: "Joined SMB-W cluster",
			},
			Run: r.JoinSmbwCluster,
			Predicates: lifecycle.Predicates{
				r.container.IsSmbwContainer,
				r.container.HasJoinIps,
			},
		},
	}

	steps = append(steps, metricsSteps...)
	steps = append(steps, csiSteps...)
	steps = append(steps, steps2...)

	return steps
}

func (r *containerReconcilerLoop) checkAllowForceStopInstruction(ctx context.Context, pod *v1.Pod) (bool, error) {
	executor, err := podexec.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
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

func (r *containerReconcilerLoop) ensureStateDeleting(ctx context.Context) error {
	return services.SetContainerStateDeleting(ctx, r.container, r.Client)
}

func (r *containerReconcilerLoop) checkPodUnhealthy(ctx context.Context) error {
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
		cs, err := resources.GetWekaPodContainerStatus(pod)
		if err == nil && cs.RestartCount > 0 {
			if statusErr := r.updateContainerStatusIfNotEquals(ctx, weka.Unhealthy); statusErr != nil {
				return statusErr
			}
			// stop here, no reason to go to the next steps
			return lifecycle.NewWaitErrorWithDuration(errors.New("pod is unhealthy"), time.Second*15)
		}
	}
	return nil
}

// reapExitedBackendPod removes the weka do-not-force-delete-unsafe finalizer from, and reaps, a backend pod
func (r *containerReconcilerLoop) reapExitedBackendPod(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	pod := r.pod
	logger.Info("Reaping exited backend pod, removing weka finalizer so a fresh pod can be recreated",
		"pod", pod.Name, "phase", pod.Status.Phase)

	if err := r.deletePod(ctx, pod); err != nil {
		return err
	}

	return lifecycle.NewWaitErrorWithDuration(
		errors.New("exited backend pod reaped, waiting for recreation"),
		time.Second*5,
	)
}

func (r *containerReconcilerLoop) WaitForPodRunning(ctx context.Context) error {
	pod := r.pod

	if pod.Status.Phase == v1.PodRunning {
		return nil
	}

	return lifecycle.NewWaitErrorWithDuration(errors.New("Pod is not running"), time.Second*10)
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

		var wekaContainers []weka.WekaContainer
		var err error
		if !r.container.IsProtocolContainer() || config.Config.AllowMultipleProtocolsPerNode {
			wekaContainers, err = r.KubeService.GetWekaContainersSimple(ctx, r.container.GetNamespace(), node, r.container.GetLabels())
			if err != nil {
				return err
			}
		} else {
			wekaContainers, err = r.getFrontendWekaContainerOnNode(ctx, node)
			if err != nil {
				return err
			}
		}

		for i := range wekaContainers {
			wc := &wekaContainers[i]
			if wc.UID == r.container.UID {
				continue // that's us, skipping
			}

			if wc.Status.NodeAffinity != "" {
				// evicting for reschedule
				spanCtx, logger := instrumentation.CreateLogSpan(ctx, "enforceNodeAffinity-evict")
				logger.Info("Another container is already using this node, evicting it", "other_container", wc.Name, "container_name", r.container.Name, "node", node)
				//goland:noinspection ALL
				logger.End()
				if err := r.ensureStateDeleting(spanCtx); err != nil {
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
	logger := instrumentation.CurrentSpanLogger(ctx)

	nodeName := r.pod.Spec.NodeName
	if nodeName == "" {
		return lifecycle.NewWaitError(errors.New("pod is not assigned to node"))
	}

	// get node before setting status - if node is not found, we will return error and retry
	// NOTE: let kubernetes terminate pod if node is not found and get it rescheduled
	_, err := r.KubeService.GetNode(ctx, k8sTypes.NodeName(nodeName))
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("node not found: %s", nodeName)
	}

	r.container.Status.NodeAffinity = weka.NodeName(nodeName)
	r.container.Status.PrinterColumns.NodeAffinity = nodeName
	logger.Info("binding to node", "node", nodeName, "container_name", r.container.Name)
	return r.Status().Update(ctx, r.container)
}

func (r *containerReconcilerLoop) clearStatusOnNodeNotFound(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

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

// Possible use cases:
// - wekacontainer was created with wrong node selector, node selector was changed, but pod is still in Pending state
// - drivers container is in Pending state, but node affinity is set, so we want to change node affinity and reschedule pod
func (r *containerReconcilerLoop) deletePodIfUnschedulable(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

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
		return nil // cleaning up only unschedulable
	}

	// relying on lastTransitionTime of Unschedulable condition
	rescheduleAfter := config.Config.DeleteUnschedulablePodsAfter
	if time.Since(unschedulableSince) > rescheduleAfter {
		// handle drivers container
		// if node affinity is set in container status, try to reschedule pod
		if container.IsDriversContainer() && r.container.Status.NodeAffinity != "" {
			logger.Debug("Pod is unschedulable, cleaning container status", "unschedulable_since", unschedulableSince)

			// clear status before deleting pod (let reconciler start from the beginning)
			if err := r.clearStatus(ctx); err != nil {
				err = fmt.Errorf("error clearing status: %w", err)
				return err
			}
		}

		_ = r.RecordEvent( //nolint:errcheck // error return value intentionally not checked
			v1.EventTypeWarning,
			"UnschedulablePod",
			fmt.Sprintf("Pod is unschedulable since %s, deleting it", unschedulableSince),
		)

		err := r.deletePod(ctx, pod)
		if err != nil {
			err = fmt.Errorf("error deleting unschedulable pod: %w", err)
			return err
		}
		return errors.New("Pod is unschedulable and is being deleted")
	}
	return nil
}

func (r *containerReconcilerLoop) ensurePodNotRunningState(ctx context.Context) error {
	return r.updateContainerStatusIfNotEquals(ctx, weka.PodNotRunning)
}

func (r *containerReconcilerLoop) setPodRunningStatus(ctx context.Context) error {
	if r.pod.Status.Phase == v1.PodRunning {
		return r.updateContainerStatusIfNotEquals(ctx, weka.PodRunning)
	}
	return nil
}

func (r *containerReconcilerLoop) applyCurrentImage(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	pod := r.pod
	container := r.container

	podContainer, err := resources.GetWekaPodContainer(pod)
	if err != nil {
		return err
	}

	if podContainer.Image != container.Spec.Image {
		logger.Info("Current image does not match spec", "pod_image", podContainer.Image, "spec_image", container.Spec.Image)
		return nil
	}

	if pod.Status.Phase != v1.PodRunning {
		logger.Info("Pod is not running yet")
		return errors.New("Pod is not running yet")
	}

	if container.Status.Status != weka.Running {
		logger.Info("Container is not fully running yet", "status", container.Status.Status)
		return errors.New("Container is not fully running yet")
	}

	if !container.IsServiceContainer() && !container.IsSSDProxyContainer() {
		// Check STATUS == READY (skip if InternalStatus not yet populated)
		if container.Status.InternalStatus != "" && container.Status.InternalStatus != "READY" {
			logger.Info("Container is not READY yet", "status", container.Status.InternalStatus)
			return lifecycle.NewWaitError(fmt.Errorf("container status is not READY: %s", container.Status.InternalStatus))
		}

		// ReconcileWekaLocalStatus leaves this nil when it could not read `weka local ps --json` -
		// most notably when it bails out early on a NotReady node. Without it the lease and
		// IO-process gates below would all read zero values and silently pass, letting a rolling
		// upgrade advance past a container we know nothing about. Wait instead.
		if r.localContainer == nil {
			logger.Info("Weka local status is not available yet")
			return lifecycle.NewWaitError(errors.New("weka local status is not available yet"))
		}

		// Check VALID LEASE (only available in Weka >= 5.1.2; nil means field absent, skip)
		if r.localContainer.InternalStatus.HasLease != nil && !*r.localContainer.InternalStatus.HasLease {
			logger.Info("Container does not have a valid lease")
			return lifecycle.NewWaitError(errors.New("container does not have a valid lease"))
		}

		ioProcessesNotUp, hasIoProcessesNotUp := r.localContainer.InternalStatus.IoProcessesNotUp()
		if hasIoProcessesNotUp {
			// Not up yet: drop any previously recorded "IO processes up" anchor, and keep waiting
			// (uncapped - we don't give up and proceed anyway).
			if _, ok := container.Status.Timestamps[string(weka.TimestampIoProcessesUp)]; ok {
				delete(container.Status.Timestamps, string(weka.TimestampIoProcessesUp))
				if updateErr := r.Status().Update(ctx, container); updateErr != nil {
					return updateErr
				}
			}

			msg := fmt.Sprintf("container has IO processes not up: %s", ioProcessesNotUp)

			// The wait is uncapped by design, so also raise an event: otherwise a permanently
			// wedged IO process stalls the whole cluster's rolling upgrade with no signal outside
			// the operator log.
			_ = r.RecordEventThrottled(v1.EventTypeWarning, "IoProcessesNotUp", msg, time.Minute) //nolint:errcheck // error return value intentionally not checked

			// Logged at Info, not Debug: the wait below is uncapped, so the reported
			// process ids have to be visible at the default log level.
			logger.Info("Container has IO processes not up", "io_processes_not_up", ioProcessesNotUp)
			return lifecycle.NewWaitError(errors.New(msg))
		}

		// Checked before the settle wait below so the settle window does not start ticking while the
		// cluster still reports the old version.
		if r.container.ShouldJoinCluster() {
			if err := r.verifyClusterContainerApplied(ctx); err != nil {
				return err
			}
		}

		// IO processes are up. If a settle period is configured, hold off marking the image applied
		// until it has elapsed since IO processes were first observed up. Resolved here rather than
		// above so the not-up path does not depend on reading the owner object. The override lives on
		// whichever object owns the container - WekaClient for clients, WekaCluster for the rest.
		waitSince := config.Config.Timeouts.WaitSinceIoProcessesUpTimeout

		if r.container.ShouldJoinCluster() {
			override, overrideErr := r.resolveWaitSinceIoProcessesUpOverride(ctx)
			switch {
			case overrideErr == nil:
				if override != nil {
					waitSince = override.Duration
				}
			case errors.Is(overrideErr, errNoOverrideOwner):
				// Nothing to retry: proceed on the operator-wide default.
				logger.Error(overrideErr, "No owner override available, using default waitSinceIoProcessesUpTimeout")
			default:
				// Requeue instead of using the default: that default is 0 in the shipped chart, so a
				// transient read failure would collapse a configured settle window to no wait at all.
				return overrideErr
			}
		}

		if waitSince > 0 {
			anchor, ok := container.Status.Timestamps[string(weka.TimestampIoProcessesUp)]

			// An anchor older than the pod belongs to a previous one
			if ok && pod.Status.StartTime != nil && anchor.Time.Before(pod.Status.StartTime.Time) {
				ok = false
			}

			if !ok {
				container.Status.Timestamps[string(weka.TimestampIoProcessesUp)] = metav1.Time{Time: time.Now()}
				if updateErr := r.Status().Update(ctx, container); updateErr != nil {
					return updateErr
				}
				return lifecycle.NewWaitErrorWithDuration(errors.New("waiting for IO processes to settle"), waitSince)
			}

			if elapsed := time.Since(anchor.Time); elapsed < waitSince {
				return lifecycle.NewWaitErrorWithDuration(
					fmt.Errorf("waiting for IO processes to settle, %v elapsed of %v", elapsed, waitSince),
					max(waitSince-elapsed, time.Second),
				)
			}
		}
	}

	// Clear the settle anchor so the next image roll re-arms the wait from scratch instead of
	// reusing this roll's timestamp (which would make the wait a no-op). Persisted by the
	// Status().Update below. Deleting an absent key is a no-op.
	delete(container.Status.Timestamps, string(weka.TimestampIoProcessesUp))

	logger.Info("Updating LastAppliedImage", "image", container.Spec.Image)

	container.Status.LastAppliedImage = container.Spec.Image

	// handleSpecVersionMismatch sets LastAppliedPodConfigHash via the pod annotation; this covers
	// pre-existing pods that have no annotation and are skipped by that path.
	if podConfigVer := targetPodConfigHash(container); podConfigVer != "" {
		container.Status.LastAppliedPodConfigHash = podConfigVer
	}

	return r.Status().Update(ctx, container)
}
