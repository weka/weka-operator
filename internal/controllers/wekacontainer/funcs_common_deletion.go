// This file contains functions related to deletion of WekaContainer, which are used in both destroying and deleting state flows
package wekacontainer

import (
	"context"
	"encoding/base64"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/operations/umount"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/pkg/util/podexec"
)

// persistentDirCleanupStartedKey is the Status.Timestamps key stamped on the first discovery wait
// during persistent-dir cleanup, so a stuck node discovery (e.g. an unschedulable discovery pod) can
// be detected and surfaced instead of blocking the finalizer forever.
const persistentDirCleanupStartedKey = "PersistentDirCleanupStarted"
const persistentDirCleanupDiscoveryTimeout = 10 * time.Minute
const persistentDirCleanupSlowRequeue = 5 * time.Minute

func (r *containerReconcilerLoop) HandleDeletion(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	logger.Info("Handling container deletion", "container", r.container.Name)

	err := r.finalizeContainer(ctx)
	if err != nil {
		return err
	}
	controllerutil.RemoveFinalizer(r.container, consts.WekaFinalizer)
	controllerutil.RemoveFinalizer(r.container, consts.WekaFinalizerDeprecated)
	err = r.Update(ctx, r.container)
	if err != nil {
		logger.Error(err, "Error removing finalizer")
		return errors.Wrap(err, "Failed to remove finalizer")
	}
	return nil
}

func (r *containerReconcilerLoop) finalizeContainer(ctx context.Context) error {
	ctx, spanLogger := instrumentation.CreateLogSpan(ctx, "finalizeContainer")
	defer spanLogger.End()

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

	// deallocate NICs from node annotation
	if r.node != nil {
		err = r.DeallocateNICs(ctx)
		if err != nil {
			return err
		}
	}

	// remove csi node topology labels
	// NOTE: wekaClient is needed for getCsiDriverName
	if r.wekaClient != nil && r.node != nil && r.WekaContainerManagesCsi() {
		err = r.UnsetCsiNodeTopologyLabels(ctx)
		if err != nil {
			return err
		}
	}
	// CSI node DaemonSet is now managed by WekaClient, not by individual containers
	return nil
}

func (r *containerReconcilerLoop) cleanupPersistentDir(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "cleanupPersistentDir")
	defer logger.End()

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
			// but generic ReconciliationError wrapping error is sort of pointless
			if strings.Contains(err.Error(), "error reconciling object during phase GetNode: Node") && strings.Contains(err.Error(), "not found") {
				logger.Info("node is deleted, no need for cleanup")
				return nil
			}
			if isWaitError(err) {
				return r.handleCleanupDiscoveryWait(ctx, err)
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
		// Always run privileged to ensure access to host files under SELinux enforcement.
		RunPrivileged: true,
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

// isWaitError reports whether err is a *lifecycle.WaitError, possibly inside one or more
// *lifecycle.StepRunError layers from ExecuteOperation's engines. StepRunError has no Unwrap,
// so peel .Err manually — same as the engine's own RunAsReconcilerResponse.
func isWaitError(err error) bool {
	for err != nil {
		if stepErr, ok := err.(*lifecycle.StepRunError); ok {
			err = stepErr.Err
			continue
		}
		_, ok := err.(*lifecycle.WaitError)
		return ok
	}
	return false
}

// handleCleanupDiscoveryWait is called when GetNodeInfo returns a wait error during persistent-dir
// cleanup. Returns the error to propagate.
func (r *containerReconcilerLoop) handleCleanupDiscoveryWait(ctx context.Context, waitErr error) error {
	container := r.container

	if container.Status.Timestamps == nil {
		container.Status.Timestamps = make(map[string]metav1.Time)
	}

	started, ok := container.Status.Timestamps[persistentDirCleanupStartedKey]
	if !ok {
		container.Status.Timestamps[persistentDirCleanupStartedKey] = metav1.Now()
		if err := r.Status().Update(ctx, container); err != nil {
			return err
		}
		return waitErr
	}

	if time.Since(started.Time) < persistentDirCleanupDiscoveryTimeout {
		return waitErr
	}

	msg := fmt.Sprintf(
		"node discovery on node %s is not completing; check 'kubectl -n %s describe wekacontainer %s' for the cause",
		container.GetNodeAffinity(), container.Namespace, operations.DiscoverContainerName(string(container.GetNodeAffinity())),
	)
	_ = r.RecordEventThrottled(v1.EventTypeWarning, "PersistentDirCleanupStuck", msg, persistentDirCleanupSlowRequeue) //nolint:errcheck // error return value intentionally not checked

	return lifecycle.NewWaitErrorWithDuration(waitErr, persistentDirCleanupSlowRequeue)
}

func (r *containerReconcilerLoop) writeAllowForceStopInstruction(ctx context.Context, pod *v1.Pod, skipExec bool) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "writeAllowForceStopInstruction", "skipExec", skipExec)
	defer logger.End()

	// create a Json and sent it to node-agent, required for CoreOS / cri-o container agent
	// since we can't execute directly on pod if it is in terminating state
	err := r.sendStopInstructionsViaAgent(ctx, pod, resources.ShutdownInstructions{AllowStop: false, AllowForceStop: true})
	if err != nil {
		var notRunningErr *NodeAgentPodNotRunning
		var notFoundErr *NodeAgentPodNotFound
		if errors.As(err, &notRunningErr) || errors.As(err, &notFoundErr) {
			logger.Info("Node agent pod not available, will use fallback method for force-stop")
		} else {
			logger.Error(err, "Error writing force-stop instructions via node-agent")
		}
	}
	if skipExec {
		return err
	}

	timeout := 1 * time.Minute

	executor, err := podexec.NewExecInPodWithTimeout(r.RestClient, r.Manager.GetConfig(), pod, &timeout)
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
	ctx, logger := instrumentation.CreateLogSpan(ctx, "sendStopInstructionsViaAgent", "force", instructions.AllowForceStop, "instructions", instructions)
	defer logger.End()

	var nodeName string
	var err error

	if r.node == nil {
		// try to get node name from pod
		nodeName, err = r.getCurrentPodNodeName()
		if err != nil {
			return fmt.Errorf("cannot get current pod node name: %v", err)
		}
	} else {
		nodeName = r.node.Name
	}

	agentPod, err := r.GetNodeAgentPod(ctx, weka.NodeName(nodeName))
	if err != nil {
		return err
	}

	instructionsJson, err := json.Marshal(instructions)
	if err != nil {
		return err
	}

	timeout := 1 * time.Minute
	executor, err := podexec.NewExecInPodByName(r.RestClient, r.Manager.GetConfig(), agentPod, "node-agent", &timeout)
	if err != nil {
		return err
	}

	nodeInfo, err := r.GetNodeInfo(ctx, weka.NodeName(nodeName))
	if err != nil {
		return err
	}
	instructionsBasePath := path.Join(resources.GetPodShutdownInstructionPathOnAgent(nodeInfo.BootID, pod))
	instructionsPath := path.Join(instructionsBasePath, "shutdown_instructions.json")

	// Use base64 encoding to safely pass JSON through shell
	instructionsB64 := base64.StdEncoding.EncodeToString(instructionsJson)
	_, _, err = executor.ExecNamed(ctx, "StopInstructionsViaAgent", []string{"bash", "-ce", fmt.Sprintf("mkdir -p '%s' && echo '%s' | base64 -d > '%s'", instructionsBasePath, instructionsB64, instructionsPath)})
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

	ctx, logger := instrumentation.CreateLogSpan(ctx, "ensureNoPod")
	defer logger.End()

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
	if err != nil {
		var notRunningErr *NodeAgentPodNotRunning
		var notFoundErr *NodeAgentPodNotFound
		if errors.As(err, &notRunningErr) || errors.As(err, &notFoundErr) {
			logger.Info("Node agent pod not available, skipping force stop via agent")
		} else {
			// do not return error, as we are deleting pod anyway
			logger.Error(err, "Error writing allow force stop instruction")
		}
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

	ctx, logger := instrumentation.CreateLogSpan(ctx, "ensureNoPod", "skipExec", skipExec)
	defer logger.End()

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
	if err != nil {
		var notRunningErr *NodeAgentPodNotRunning
		var notFoundErr *NodeAgentPodNotFound
		if errors.As(err, &notRunningErr) || errors.As(err, &notFoundErr) {
			logger.Info("Node agent pod not available, skipping weka local stop via agent")
		} else {
			// do not return error, as we are deleting pod anyway
			logger.Error(err, "Error writing allow stop instruction")
		}
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
	ctx, logger := instrumentation.CreateLogSpan(ctx, "deletePod")
	defer logger.End()

	if pod == nil {
		return errors.New("pod is nil")
	}

	// This is a controller-initiated delete, so it is safe to let the pod object be reaped: strip the
	// weka finalizer we set at creation (and its deprecated alias, for pods created by an older
	// operator). Do this BEFORE the deletionTimestamp short-circuit below — if the pod was already
	// force-removed (manually or automatically) it carries a deletionTimestamp AND our finalizer, and
	// returning early without clearing it would wedge the pod in Terminating forever.
	base := pod.DeepCopy()
	removed := controllerutil.RemoveFinalizer(pod, consts.WekaFinalizer)
	removed = controllerutil.RemoveFinalizer(pod, consts.WekaFinalizerDeprecated) || removed
	if removed {
		if err := r.Patch(ctx, pod, client.MergeFrom(base)); err != nil {
			logger.Error(err, "Error removing weka finalizer from pod", "pod", pod.Name)
			return errors.Wrap(err, "Failed to remove weka finalizer from pod")
		}
		logger.Info("Removed weka finalizer from pod", "pod", pod.Name)
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
	ctx, spanLogger := instrumentation.CreateLogSpan(ctx, "runWekaLocalStop")
	defer spanLogger.End()

	timeout := 12 * time.Second
	bashTimeout := 10 * time.Second
	executor, err := podexec.NewExecInPodWithTimeout(r.RestClient, r.Manager.GetConfig(), pod, &timeout)
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
	// handle the case when there is no weka-container on the pod
	if err != nil && strings.Contains(err.Error(), "container not found") {
		return nil
	}
	if err != nil {
		err = fmt.Errorf("error stopping weka local: %s, %v", stderr.String(), err)
	}

	return err
}

func (r *containerReconcilerLoop) writeAllowStopInstruction(ctx context.Context, pod *v1.Pod, skipExec bool) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "writeAllowStopInstruction", "skipExec", skipExec)
	defer logger.End()

	// create a Json and sent it to node-agent, required for CoreOS / cri-o container agent
	// since we can't execute directly on pod if it is in terminating state
	err := r.sendStopInstructionsViaAgent(ctx, pod, resources.ShutdownInstructions{AllowStop: true, AllowForceStop: false})
	if err != nil {
		var notRunningErr *NodeAgentPodNotRunning
		var notFoundErr *NodeAgentPodNotFound
		if errors.As(err, &notRunningErr) || errors.As(err, &notFoundErr) {
			logger.Info("Node agent pod not available, will use fallback method for stop")
		} else {
			logger.Error(err, "Error writing stop instructions via node-agent")
		}
		// NOTE: No error on purpose, as it's only one of method we attempt to start stopping
	}
	if skipExec {
		return err
	}

	timeout := 1 * time.Minute

	executor, err := podexec.NewExecInPodWithTimeout(r.RestClient, r.Manager.GetConfig(), pod, &timeout)
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

	if r.node == nil {
		// no reason to wait for mounts if node does not exist
		_ = r.RecordEventThrottled(v1.EventTypeNormal, "NodeNotFound", "Node is not found", time.Minute) //nolint:errcheck // error return value intentionally not checked
		return nil
	}

	// TODO: This logic should become native FE logic
	// meanwhile we are working around on operator side
	// if container is being deleted and pos is still alive - we should ensnure no mounts, and drain if drain flag is set to true

	mounts, err := r.GetActiveMounts(ctx)
	if err != nil {
		return err
	}
	if mounts == nil {
		err := errors.New("Mounts are not set")
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "ActiveMounts", err.Error(), time.Minute) //nolint:errcheck // error return value intentionally not checked
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
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "ActiveMounts", err.Error(), time.Minute) //nolint:errcheck // error return value intentionally not checked

		return lifecycle.NewWaitErrorWithDuration(err, 15*time.Second)
	}
}

func (r *containerReconcilerLoop) invokeDrain(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "invokeDrain")
	defer logger.End()

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
	ctx, spanLogger := instrumentation.CreateLogSpan(ctx, "invokeForceUmountOnHost")
	defer spanLogger.End()
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
	logger := instrumentation.CurrentSpanLogger(ctx)

	nodeName := r.container.GetNodeAffinity()

	// if node name is empty, it means no node affinity was set on wekaContainer,
	// so we should not check if node is alive
	// Note: we only check NodeIsReady, not NodeIsUnschedulable, because cordoned nodes
	// are still functional and the resign drives pod has tolerations to be scheduled there
	if nodeName != "" && !NodeIsReady(r.node) {
		if config.Config.CleanupRemovedNodes {
			_, err := r.KubeService.GetNode(ctx, k8sTypes.NodeName(nodeName))
			if err != nil {
				if apierrors.IsNotFound(err) {
					logger.Info("node is deleted, no need for cleanup")
					return nil
				}
			}
		}
		err := fmt.Errorf("container node is not ready, cannot perform resign drives operation")
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	deactivatedContainer := r.container

	if deactivatedContainer.Status.Allocations == nil || len(deactivatedContainer.Status.Allocations.Drives) == 0 {
		logger.Info("No drives to force resign for container", "container_name", deactivatedContainer.Name)
		return nil
	}

	allSerials := deactivatedContainer.Status.Allocations.Drives
	serials := allSerials
	if r.node != nil {
		if blockedStr, ok := r.node.Annotations[consts.AnnotationBlockedDrives]; ok {
			var blocked []string
			if err := json.Unmarshal([]byte(blockedStr), &blocked); err != nil {
				return fmt.Errorf("failed to unmarshal blocked-drives annotation: %w", err)
			}
			if len(blocked) > 0 {
				blockedSet := make(map[string]struct{}, len(blocked))
				for _, s := range blocked {
					blockedSet[s] = struct{}{}
				}
				serials = make([]string, 0, len(allSerials))
				for _, s := range allSerials {
					if _, isBlocked := blockedSet[s]; !isBlocked {
						serials = append(serials, s)
					}
				}
				logger.Info("Filtered blocked drives from resign payload",
					"total", len(allSerials), "blocked", len(blocked), "resigning", len(serials))
			}
		}
	}

	payload := weka.ForceResignDrivesPayload{
		NodeName:      deactivatedContainer.GetNodeAffinity(),
		DeviceSerials: serials,
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
