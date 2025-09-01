package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/operations/tempops"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/pkg/util"
)

// ActiveStateFlow returns the steps for a container in the active state
func ActiveStateFlow(r *containerReconcilerLoop) []lifecycle.Step {
	// 1. First part of the flow
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
				r.container.IsClientContainer,
				lifecycle.BoolValue(config.Config.CsiInstallationEnabled),
			},
		},
		&lifecycle.SimpleStep{
			Run: r.FetchTargetCluster,
			Predicates: lifecycle.Predicates{
				func() bool {
					return r.wekaClient != nil && r.wekaClient.Spec.TargetCluster.Name != ""
				},
				lifecycle.BoolValue(config.Config.CsiInstallationEnabled),
			},
		},
		&lifecycle.SimpleStep{
			Run: r.refreshPod,
		},
	}

	// 2. Metrics steps
	metricsSteps := MetricsSteps(r)

	// 3. Second part of the flow
	steps2 := []lifecycle.Step{
		&lifecycle.SimpleStep{
			Run: r.initState,
		},
		&lifecycle.SimpleStep{
			Run: r.deleteIfNoNode,
		},
		&lifecycle.SimpleStep{
			Run: r.checkTolerations,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.NodeNotSet),
				lifecycle.BoolValue(config.Config.CleanupContainersOnTolerationsMismatch),
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
			Run: r.ensurePod,
			Predicates: lifecycle.Predicates{
				r.PodNotSet,
			},
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
			Run: r.updateAdhocOpStatus,
			Predicates: lifecycle.Predicates{
				lifecycle.Or(r.container.IsAdhocOpContainer, r.container.IsDiscoveryContainer),
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
			Run: r.JoinNfsInterfaceGroups,
			Predicates: lifecycle.Predicates{
				r.container.IsNfsContainer,
				r.container.HasJoinIps,
			},
		},
	}

	csiSteps := CsiSteps(r)

	steps := append(steps1, metricsSteps...)
	steps = append(steps, steps2...)
	steps = append(steps, csiSteps...)

	return steps
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
	if r.container.IsWekaContainer() && !r.container.IsDriversBuilder() {
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

func (r *containerReconcilerLoop) ensureStateDeleting(ctx context.Context) error {
	return services.SetContainerStateDeleting(ctx, r.container, r.Client)
}

func (r *containerReconcilerLoop) initState(ctx context.Context) error {
	if r.container.Status.Conditions == nil {
		r.container.Status.Conditions = []metav1.Condition{}
	}

	if r.container.Status.PrinterColumns == nil {
		r.container.Status.PrinterColumns = &weka.ContainerPrinterColumns{}
	}

	changed := false

	if r.container.Status.Status == "" {
		r.container.Status.Status = weka.Init
		changed = true
	}

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

func (r *containerReconcilerLoop) updatePodMetadataOnChange(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "updatePodMetadataOnChange")
	defer end()

	pod := r.pod
	newLabels := resources.LabelsForWekaPod(r.container)
	newAnnotations := resources.AnnotationsForWekaPod(r.container.GetAnnotations(), r.pod.GetAnnotations())
	pod.SetLabels(newLabels)
	pod.SetAnnotations(newAnnotations)

	logger.Info("Updating pod metadata", "new_labels", newLabels, "new_annotations", newAnnotations)

	if err := r.Update(ctx, pod); err != nil {
		return fmt.Errorf("failed to update pod labels: %w", err)
	}
	r.pod = pod
	return nil
}

func (r *containerReconcilerLoop) podMetadataChanged() bool {
	oldLabels := r.pod.GetLabels()
	newLabels := resources.LabelsForWekaPod(r.container)

	if !util.NewHashableMap(newLabels).Equals(util.NewHashableMap(oldLabels)) {
		return true
	}

	oldAnnotations := r.pod.GetAnnotations()
	newAnnotations := resources.AnnotationsForWekaPod(r.container.GetAnnotations(), oldAnnotations)

	return !util.NewHashableMap(newAnnotations).Equals(util.NewHashableMap(oldAnnotations))
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

	if ok := controllerutil.AddFinalizer(container, resources.WekaFinalizer); !ok {
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

func (r *containerReconcilerLoop) IsS3ClusterFormed(ctx context.Context) (bool, error) {
	cluster, err := r.getCluster(ctx)
	if err != nil {
		return false, err
	}

	return meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondS3ClusterCreated), nil
}

func (r *containerReconcilerLoop) HandleNodeNotReady(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleNodeNotReady")
	defer end()

	if r.node == nil {
		return errors.New("node is not set")
	}

	node := r.node
	pod := r.pod

	if !NodeIsReady(node) {
		err := fmt.Errorf("node %s is not ready", node.Name)

		_ = r.RecordEventThrottled(v1.EventTypeWarning, "NodeNotReady", err.Error(), time.Minute)

		// if node is not ready, we should terminate the pod and let it be rescheduled
		if pod != nil && pod.Status.Phase == v1.PodRunning {
			logger.Info("Deleting pod on NotReady node", "pod", pod.Name)
			err := r.deletePod(ctx, pod)
			return lifecycle.NewWaitErrorWithDuration(
				fmt.Errorf("deleting pod on NotReady node, err: %w", err),
				time.Second*15,
			)
		}

		// stop here, no reason to go to the next steps
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	// if node is unschedulable, just send the event
	if NodeIsUnschedulable(node) {
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

func (r *containerReconcilerLoop) JoinS3Cluster(ctx context.Context) error {
	isFormed, err := r.IsS3ClusterFormed(ctx)
	if err != nil {
		return fmt.Errorf("error checking if S3 cluster is formed: %w", err)
	}
	if !isFormed {
		return lifecycle.NewWaitError(fmt.Errorf("S3 cluster is not formed yet, waiting for it to be formed"))
	}

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
		if !r.container.IsProtocolContainer() {
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

func (r *containerReconcilerLoop) ensurePodNotRunningState(ctx context.Context) error {
	return r.updateContainerStatusIfNotEquals(ctx, weka.PodNotRunning)
}

func (r *containerReconcilerLoop) updateAdhocOpStatus(ctx context.Context) error {
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

func (r *containerReconcilerLoop) migrateEnsurePorts(ctx context.Context) error {
	if r.container.Spec.ExposePorts == nil {
		return nil
	}

	if !r.container.IsEnvoy() {
		return nil // we never set old format for anything but envoy
	}

	if len(r.container.Spec.ExposePorts) == 2 {
		r.container.Spec.ExposedPorts = []v1.ContainerPort{{
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

func (r *containerReconcilerLoop) checkTolerations(ctx context.Context) error {
	ignoredTaints := config.Config.TolerationsMismatchSettings.GetIgnoredTaints()

	notTolerated := !util.CheckTolerations(r.node.Spec.Taints, r.container.Spec.Tolerations, ignoredTaints)

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

func (r *containerReconcilerLoop) deleteEnvoyIfNoS3Neighbor(ctx context.Context) error {
	if !r.container.IsEnvoy() {
		return nil // only envoy containers should be checked
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	nodeName := r.container.GetNodeAffinity()

	if nodeName != "" {
		ownerRefs := r.container.GetOwnerReferences()
		if len(ownerRefs) == 0 {
			return errors.New("no owner references found")
		} else if len(ownerRefs) > 1 {
			return errors.New("more than one owner reference found")
		}

		ownerUid := string(ownerRefs[0].UID)
		// Check if there are any S3 wekacontainera on the same node
		s3Containers, err := discovery.GetClusterContainersByClusterUID(ctx, r.Manager.GetClient(), ownerUid, r.container.Namespace, weka.WekaContainerModeS3)
		if err != nil {
			return err
		}

		foundS3Neighbor := false
		for _, s3Container := range s3Containers {
			if s3Container.GetNodeAffinity() == nodeName {
				foundS3Neighbor = true
				break
			}
		}
		if foundS3Neighbor {
			logger.Debug("Found S3 neighbor, not deleting envoy container")
			return nil
		}
	}

	noS3NeighborKey := "NoS3Neighbor"

	if r.container.Status.Timestamps == nil {
		r.container.Status.Timestamps = make(map[string]metav1.Time)
	}
	if since, ok := r.container.Status.Timestamps[noS3NeighborKey]; !ok {
		r.container.Status.Timestamps[noS3NeighborKey] = metav1.Time{Time: time.Now()}
		if err := r.Status().Update(ctx, r.container); err != nil {
			return err
		}

		return lifecycle.NewWaitErrorWithDuration(
			errors.New("Envoy container has no S3 neighbor, waiting for 5 minutes before deleting it"),
			time.Second*15,
		)
	} else if time.Since(since.Time) < config.Config.DeleteEnvoyWithoutS3NeighborTimeout {
		logger.Info("Envoy container has no S3 neighbor, but waiting for 5 minutes before deleting it",
			"waited", time.Since(since.Time).String(),
			"node", nodeName,
		)
		return nil
	}

	_ = r.RecordEvent(
		v1.EventTypeNormal,
		"EnvoyContainerWithoutS3Neighbor",
		"Envoy container has no S3 neighbor, deleting it",
	)

	if err := r.Client.Delete(ctx, r.container); err != nil {
		return errors.Wrap(err, "failed to delete envoy container")
	}

	// Clear the timestamp to avoid re-deleting the container on next reconcile
	delete(r.container.Status.Timestamps, noS3NeighborKey)
	if err := r.Status().Update(ctx, r.container); err != nil {
		return errors.Wrap(err, "failed to update container status after deleting envoy")
	}

	logger.Info("Envoy container deleted as it has no S3 neighbor")

	return nil
}
