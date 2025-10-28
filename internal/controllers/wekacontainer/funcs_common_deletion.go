// This file contains functions related to deletion of WekaContainer, which are used in both destroying and deleting state flows
package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/operations/umount"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/pkg/util"
)

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
	controllerutil.RemoveFinalizer(r.container, resources.WekaFinalizer)
	err = r.Update(ctx, r.container)
	if err != nil {
		logger.Error(err, "Error removing finalizer")
		return errors.Wrap(err, "Failed to remove finalizer")
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

	resourceAllocator, err := allocator.GetAllocator(ctx, r.Client)
	if err != nil {
		return err
	}

	err = resourceAllocator.DeallocateContainer(ctx, container)
	if err != nil {
		logger.Error(err, "Error deallocating container")
		return err
	} else {
		logger.Info("Container deallocated")
	}
	// deallocate from allocmap

	// remove csi node topology labels
	// NOTE: wekaClient is needed for getCsiDriverName
	if r.wekaClient != nil && r.node != nil && r.WekaContainerManagesCsi() {
		err = r.UnsetCsiNodeTopologyLabels(ctx)
		if err != nil {
			return err
		}
	}
	// delete csi node pod
	return r.CleanupCsiNodeServerPod(ctx)
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
		// if r.node != nil && NodeIsUnschedulable(r.node) {
		// 	err := fmt.Errorf("container node is unschedulable, cannot perform cleanup persistent dir operation")
		// 	return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
		// }
		if r.node != nil && !NodeIsReady(r.node) {
			err := fmt.Errorf("container node is not ready, cannot perform cleanup persistent dir operation")
			return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
		}

		nodeInfo, err := r.GetNodeInfo(ctx, container.GetNodeAffinity())
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
		*container.ToOwnerDetails(),
		container.Spec.NodeSelector,
	)

	return operations.ExecuteOperation(ctx, op)
}

func (r *containerReconcilerLoop) writeAllowForceStopInstruction(ctx context.Context, pod *v1.Pod, skipExec bool) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "writeAllowForceStopInstruction", "skipExec", skipExec)
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

	timeout := 1 * time.Minute

	executor, err := util.NewExecInPodWithTimeout(r.RestClient, r.Manager.GetConfig(), pod, &timeout)
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

	timeout := 1 * time.Minute
	executor, err := util.NewExecInPodByName(r.RestClient, r.Manager.GetConfig(), agentPod, "node-agent", &timeout)
	if err != nil {
		return err
	}

	var nodeName string
	if r.node == nil {
		// try to get node name from pod
		nodeName, err = r.getCurrentPodNodeName()
		if err != nil {
			return fmt.Errorf("cannot get current pod node name: %v", err)
		}
	} else {
		nodeName = r.node.Name
	}

	nodeInfo, err := r.GetNodeInfo(ctx, weka.NodeName(nodeName))
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

	err = r.deletePod(ctx, pod)
	if err != nil {
		return err
	}
	logger.AddEvent("Pod deleted")

	// setting for forceful termination, as we are in container delete flow
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

	err = r.deletePod(ctx, pod)
	if err != nil {
		return err
	}
	logger.AddEvent("Pod deleted")

	err = r.writeAllowStopInstruction(ctx, pod, skipExec)
	if err != nil && errors.Is(err, &NodeAgentPodNotRunning{}) {
		logger.Info("Node agent pod not running, skipping weka local stop")
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
			err = r.runWekaLocalStop(ctx, pod, false)
			if err != nil {
				logger.Error(err, "Error stopping weka local")
			}
			// we do not abort on purpose, we still should call delete even if we failed to exec
		}
	}

	return lifecycle.NewWaitError(errors.New("Pod deleted, reconciling for retry"))
}

func (r *containerReconcilerLoop) deletePod(ctx context.Context, pod *v1.Pod) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "deletePod")
	defer end()

	if pod == nil {
		return errors.New("pod is nil")
	}

	if pod.GetDeletionTimestamp() != nil {
		logger.Info("Pod is already being deleted", "pod", pod.Name)
		return nil
	}

	logger.Info("Deleting pod", "pod", pod.Name)

	err := r.Delete(ctx, pod)
	if err != nil {
		logger.Error(err, "Error deleting pod")
		return err
	}

	return nil
}

func (r *containerReconcilerLoop) runWekaLocalStop(ctx context.Context, pod *v1.Pod, force bool) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "runWekaLocalStop")
	defer end()

	timeout := 12 * time.Second
	bashTimeout := 10 * time.Second
	executor, err := util.NewExecInPodWithTimeout(r.RestClient, r.Manager.GetConfig(), pod, &timeout)
	if err != nil {
		return err
	}

	args := []string{"timeout", bashTimeout.String(), "weka", "local", "stop"}

	// we need to use --force flag
	if force {
		args = append(args, "--force")
	} else {
		args = append(args, "-g")
	}

	_, stderr, err := executor.ExecNamed(ctx, "WekaLocalStop", args)
	// hanlde the case when there is no weka-container on the pod
	if err != nil && strings.Contains(err.Error(), "container not found") {
		return nil
	}
	if err != nil {
		err = fmt.Errorf("error stopping weka local: %s, %v", stderr.String(), err)
	}

	return err
}

func (r *containerReconcilerLoop) writeAllowStopInstruction(ctx context.Context, pod *v1.Pod, skipExec bool) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "writeAllowStopInstruction", "skipExec", skipExec)
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

	timeout := 1 * time.Minute

	executor, err := util.NewExecInPodWithTimeout(r.RestClient, r.Manager.GetConfig(), pod, &timeout)
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

func (r *containerReconcilerLoop) waitForMountsOrDrain(ctx context.Context) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "")
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
	details := *deactivatedContainer.ToOwnerDetails()
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
