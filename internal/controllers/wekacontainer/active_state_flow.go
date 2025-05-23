package wekacontainer

import (
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/config"
)

// ActiveStateFlow returns the steps for a container in the active state
func ActiveStateFlow(r *containerReconcilerLoop) []lifecycle.Step {
	// 1. First part of the flow
	steps1 := []lifecycle.Step{
		// put self in state "deleting" if container is marked for deletion
		&lifecycle.SingleStep{
			Run: r.ensureStateDeleting,
			Predicates: lifecycle.Predicates{
				r.container.IsMarkedForDeletion,
				lifecycle.IsNotFunc(r.container.IsDeletingState),
				lifecycle.IsNotFunc(r.container.IsDestroyingState),
			},
		},
		&lifecycle.SingleStep{
			Run: r.GetNode,
		},
		&lifecycle.SingleStep{
			Run: r.refreshPod,
		},
		&lifecycle.SingleStep{
			Run: r.migrateEnsurePorts,
			Predicates: lifecycle.Predicates{
				func() bool {
					return len(r.container.Spec.ExposePorts) != 0
				},
			},
		},
	}

	// 2. Metrics steps
	metricsSteps := MetricsSteps(r)

	// 3. Second part of the flow
	steps2 := []lifecycle.Step{
		&lifecycle.SingleStep{
			Run: r.initState,
		},
		&lifecycle.SingleStep{
			Run: r.deleteIfNoNode,
		},
		&lifecycle.SingleStep{
			Run: r.checkTolerations,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.NodeNotSet),
				lifecycle.BoolValue(config.Config.CleanupContainersOnTolerationsMismatch),
			},
		},
		&lifecycle.SingleStep{
			Run: r.ensureFinalizer,
		},
		&lifecycle.SingleStep{
			Run: r.ensureBootConfigMapInTargetNamespace,
		},
		&lifecycle.SingleStep{
			Run: r.updatePodLabelsOnChange,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.PodNotSet),
				r.podLabelsChanged,
			},
		},
		&lifecycle.SingleStep{
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
		&lifecycle.SingleStep{
			Run: r.handlePodTermination,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.PodNotSet),
				func() bool {
					return r.pod.DeletionTimestamp != nil
				},
			},
		},
		&lifecycle.SingleStep{
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
		&lifecycle.SingleStep{
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
		&lifecycle.SingleStep{
			Run: r.cleanupFinishedOneOff,
			Predicates: lifecycle.Predicates{
				r.container.IsOneOff,
				r.ResultsAreProcessed,
			},
			FinishOnSuccess: true,
		},
		&lifecycle.SingleStep{
			Condition:  condition.CondContainerImageUpdated,
			CondReason: "ImageUpdate",
			Run:        r.handleImageUpdate,
			Predicates: lifecycle.Predicates{
				func() bool {
					return r.container.Status.LastAppliedImage != ""
				},
				r.IsNotAlignedImage,
				lifecycle.IsNotFunc(r.PodNotSet),
			},
			SkipOwnConditionCheck: true,
		},
		&lifecycle.SingleStep{
			Run: r.EnsureDrivers,
			Predicates: lifecycle.Predicates{
				r.container.RequiresDrivers,
				lifecycle.IsNotFunc(r.container.IsMarkedForDeletion),
				r.HasNodeAffinity, // if we dont have node set yet we can't load drivers, but we do want to load before creating pod if we have affinity
			},
		},
		&lifecycle.SingleStep{
			Run: r.AllocateNICs,
			Predicates: lifecycle.Predicates{
				r.ShouldAllocateNICs,
			},
		},
		&lifecycle.SingleStep{
			Condition: condition.CondContainerMigratedOutFromPVC,
			Run:       r.MigratePVC,
			Predicates: lifecycle.Predicates{
				r.PodNotSet,
				func() bool {
					return r.container.Spec.PVC != nil && r.container.Spec.GetOverrides().MigrateOutFromPvc
				},
			},
		},
		&lifecycle.SingleStep{
			Run: r.ensurePod,
			Predicates: lifecycle.Predicates{
				r.PodNotSet,
			},
		},
		&lifecycle.SingleStep{
			Run: r.deletePodIfUnschedulable,
			Predicates: lifecycle.Predicates{
				r.container.IsDriversContainer,
				func() bool {
					// if node affinity is set in container status, try to reschedule pod
					// (do not delete pod if node affinity is set on wekacontainer's spec)
					return r.pod.Status.Phase == v1.PodPending && r.container.Status.NodeAffinity != ""
				},
			},
		},
		&lifecycle.SingleStep{
			Run: r.checkPodUnhealty,
			Predicates: lifecycle.Predicates{
				func() bool {
					return r.container.Status.Status != weka.Unhealthy
				},
			},
		},
		&lifecycle.SingleStep{
			Run: r.ensurePodNotRunningState,
			Predicates: lifecycle.Predicates{
				r.PodNotRunning,
			},
		},
		&lifecycle.SingleStep{
			Run:       r.enforceNodeAffinity,
			Condition: condition.CondContainerAffinitySet,
			Predicates: lifecycle.Predicates{
				r.container.MustHaveNodeAffinity,
			},
		},
		&lifecycle.SingleStep{
			Run: r.setNodeAffinityStatus,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.HasNodeAffinity),
			},
		},
		&lifecycle.SingleStep{
			Run: r.EnsureDrivers, // drivers might be off at this point if we had to wait for node affinity
			Predicates: lifecycle.Predicates{
				r.container.RequiresDrivers,
				r.HasNodeAffinity, // if we dont have node set yet we can't load drivers, but we do want to load before creating pod if we have affinity
			},
		},
		&lifecycle.SingleStep{
			Run: r.cleanupFinished,
			Predicates: lifecycle.Predicates{
				func() bool {
					return r.pod.Status.Phase == v1.PodSucceeded
				},
			},
		},
		&lifecycle.SingleStep{
			Run: r.WaitForNodeReady,
		},
		&lifecycle.SingleStep{
			Run: r.WaitForPodRunning,
		},
		&lifecycle.SingleStep{
			Run:       r.WriteResources,
			Condition: condition.CondContainerResourcesWritten,
			Predicates: lifecycle.Predicates{
				lifecycle.Or(
					r.container.IsAllocatable,
					r.container.IsClientContainer, // nics/machine-identifiers
				),
			},
		},
		&lifecycle.SingleStep{
			Run: r.updateDriversBuilderStatus,
			Predicates: lifecycle.Predicates{
				r.container.IsDriversBuilder,
				lifecycle.IsNotFunc(r.container.IsDistMode), // TODO: legacy "dist" mode is currently used both for building drivers and for distribution
				lifecycle.IsNotFunc(r.ResultsAreProcessed),
			},
		},
		&lifecycle.SingleStep{
			Run: r.updateAdhocOpStatus,
			Predicates: lifecycle.Predicates{
				lifecycle.Or(r.container.IsAdhocOpContainer, r.container.IsDiscoveryContainer),
			},
		},
		&lifecycle.SingleStep{
			Condition: condition.CondResultsReceived,
			Run:       r.fetchResults,
			Predicates: lifecycle.Predicates{
				r.container.IsOneOff,
			},
			SkipOwnConditionCheck: false,
		},
		&lifecycle.SingleStep{
			Condition: condition.CondResultsProcessed,
			Run:       r.processResults,
			Predicates: lifecycle.Predicates{
				r.container.IsOneOff,
			},
		},
		&lifecycle.SingleStep{
			Name: "ReconcileManagementIPs",
			Run:  r.reconcileManagementIPs,
			Predicates: lifecycle.Predicates{
				func() bool {
					// we don't want to reconcile management IPs for containers that are already Running
					return len(r.container.Status.GetManagementIps()) == 0 && r.container.Status.Status != weka.Running
				},
				func() bool {
					return r.container.IsBackend() || r.container.Spec.Mode == weka.WekaContainerModeClient
				},
			},
			OnFail: r.setErrorStatus,
		},
		&lifecycle.SingleStep{
			Name: "PeriodicReconcileManagementIPs",
			Run:  lifecycle.ForceNoError(r.reconcileManagementIPs),
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
		},
		&lifecycle.SingleStep{
			Name: "ReconcileWekaLocalStatus",
			Run:  r.reconcileWekaLocalStatus,
			Predicates: lifecycle.Predicates{
				r.container.IsWekaContainer,
				lifecycle.IsNotFunc(r.container.IsMarkedForDeletion),
				r.IsStatusOvervwritableByLocal,
			},
			OnFail: r.setErrorStatus,
		},
		&lifecycle.SingleStep{
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
		&lifecycle.SingleStep{
			Run: r.applyCurrentImage,
			Predicates: lifecycle.Predicates{
				r.IsNotAlignedImage,
			},
		},
		&lifecycle.SingleStep{
			Condition: condition.CondJoinedCluster,
			Run:       r.reconcileClusterStatus,
			Predicates: lifecycle.Predicates{
				r.container.ShouldJoinCluster,
			},
			CondMessage: "Container joined cluster",
		},
		&lifecycle.SingleStep{
			Run: r.EnsureDrives,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				func() bool {
					return len(r.container.Status.Allocations.Drives) > 0
				},
				func() bool {
					return r.container.Status.InternalStatus == "READY"
				},
			},
			OnFail: r.setDrivesErrorStatus,
			Throttling: &throttling.ThrottlingSettings{
				Interval:                    config.Consts.PeriodicDrivesCheckInterval,
				DisableRandomPreSetInterval: true,
			},
		},
		&lifecycle.SingleStep{
			Condition:   condition.CondJoinedS3Cluster,
			Run:         r.JoinS3Cluster,
			CondMessage: "Joined s3 cluster",
			Predicates: lifecycle.Predicates{
				r.container.IsS3Container,
				r.container.HasJoinIps,
			},
		},
		&lifecycle.SingleStep{
			Condition:   condition.CondNfsInterfaceGroupsConfigured,
			Run:         r.JoinNfsInterfaceGroups,
			CondMessage: "NFS interface groups configured",
			Predicates: lifecycle.Predicates{
				r.container.IsNfsContainer,
				r.container.HasJoinIps,
			},
		},
	}

	csiSteps := &lifecycle.GroupedSteps{
		Name: "CsiInstallation",
		Predicates: lifecycle.Predicates{
			r.container.IsClientContainer,
			lifecycle.BoolValue(!config.Config.CsiInstallationEnabled),
		},
		Steps: []lifecycle.Step{
			&lifecycle.SingleStep{
				Condition:             condition.CondCsiDeployed,
				SkipOwnConditionCheck: true,
				Run:                   r.DeployCsiNodeServerPod,
			},
			&lifecycle.SingleStep{
				Run: r.CleanupCsiNodeServerPod,
				Predicates: lifecycle.Predicates{
					lifecycle.IsTrueCondition(condition.CondCsiDeployed, &r.container.Status.Conditions),
				},
			},
		},
	}

	steps := append(steps1, metricsSteps...)
	steps = append(steps, steps2...)
	steps = append(steps, csiSteps)

	return steps
}
