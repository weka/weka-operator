package wekacontainer

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sTypes "k8s.io/apimachinery/pkg/types"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/pkg/util"
)

// ActiveStateFlow returns the steps for a container in the active state
func ActiveStateFlow(r *containerReconcilerLoop) []lifecycle.Step {
	// First part of the flow
	steps1 := []lifecycle.Step{
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
			Run: r.checkTolerations,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.NodeNotSet),
				lifecycle.BoolValue(config.Config.CleanupContainersOnTolerationsMismatch),
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
			Run: r.handleImageUpdate,
			Predicates: lifecycle.Predicates{
				func() bool {
					return r.container.Status.LastAppliedImage != ""
				},
				r.IsNotAlignedImage,
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
			Run: r.checkPodUnhealty,
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
		// Ensure SSD proxy container exists before setting proxy UID (for drive sharing)
		&lifecycle.SimpleStep{
			Run: r.ensureProxyContainer,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				r.container.UsesDriveSharing,
				r.HasNodeAffinity,
			},
		},
		// Set ssdproxy UID after proxy exists and before pod is created
		&lifecycle.SimpleStep{
			Run: r.SetSSDProxyUID,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				r.container.UsesDriveSharing,
				r.HasNodeAffinity,
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
			Run: r.applyCurrentImage,
			Predicates: lifecycle.Predicates{
				r.IsNotAlignedImage,
			},
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
					return r.container.Spec.UseDriveSharing
				},
			},
		},
		&lifecycle.SimpleStep{
			Name: "AddVirtualDrivesPeriodic",
			Run:  r.AddVirtualDrives,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				func() bool {
					return r.container.Spec.UseDriveSharing
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
		&lifecycle.SimpleStep{
			Run: r.MarkDrivesForRemoval,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				r.AddedDrivesNotAligedWithAllocations,
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
			State: &lifecycle.State{
				Name:    condition.CondNfsInterfaceGroupsConfigured,
				Message: "NFS interface groups configured",
			},
			Run: r.EnsureNfsInterfaceGroupPorts,
			Predicates: lifecycle.Predicates{
				r.container.IsNfsContainer,
				r.container.HasJoinIps,
				r.ShouldEnsureNfsInterfaceGroupPorts,
			},
		},
	}

	steps := append(steps1, metricsSteps...)
	steps = append(steps, csiSteps...)
	steps = append(steps, steps2...)

	return steps
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

func (r *containerReconcilerLoop) ensureStateDeleting(ctx context.Context) error {
	return services.SetContainerStateDeleting(ctx, r.container, r.Client)
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
		if !(r.container.IsProtocolContainer() && !config.Config.AllowMultipleProtocolsPerNode) {
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

		for _, wc := range wekaContainers {
			if wc.UID == r.container.UID {
				continue // that's us, skipping
			}

			if wc.Status.NodeAffinity != "" {
				// evicting for reschedule
				ctx, logger, end := instrumentation.GetLogSpan(ctx, "enforceNodeAffinity-evict")
				logger.Info("Another container is already using this node, evicting it", "other_container", wc.Name, "container_name", r.container.Name, "node", node)
				//goland:noinspection ALL
				defer end()
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

// Possible use cases:
// - wekacontainer was created with wrong node selector, node selector was changed, but pod is still in Pending state
// - drivers container is in Pending state, but node affinity is set, so we want to change node affinity and reschedule pod
func (r *containerReconcilerLoop) deletePodIfUnschedulable(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

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

		_ = r.RecordEvent(
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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

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

	if !slices.Contains(
		[]weka.ContainerStatus{weka.Running, weka.PodRunning},
		container.Status.Status,
	) {
		logger.Info("Container is not running yet")
		return errors.New("Container is not running yet")
	}

	logger.Info("Updating LastAppliedImage", "image", container.Spec.Image)

	container.Status.LastAppliedImage = container.Spec.Image
	return r.Status().Update(ctx, container)
}
