package wekacontainer

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sTypes "k8s.io/apimachinery/pkg/types"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/pkg/util"
)

func (r *containerReconcilerLoop) HandleNodeNotReady(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
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

	return nil
}

// Node is removed from the cluster, delete the container if needed
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

// deleteIfTolerationsMismatch checks if container tolerates node taints.
// If not tolerated, sets container state to deleting.
func (r *containerReconcilerLoop) deleteIfTolerationsMismatch(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "deleteIfTolerationsMismatch")
	defer end()

	// No node means no taints to check
	if r.node == nil {
		return nil
	}

	// Do not delete containers without owner references - safety protection
	if len(r.container.GetOwnerReferences()) == 0 {
		return nil
	}

	if r.container.IsMarkedForDeletion() || r.container.IsDeletingState() || r.container.IsDestroyingState() {
		return nil
	}

	if r.isTolerated() {
		return nil
	}

	logger.Info("Container not tolerated on node, marking for deletion",
		"container", r.container.Name,
		"node", r.node.Name)

	_ = r.RecordEvent(v1.EventTypeNormal, "TolerationMismatch", "Toleration mismatch, deleting container")

	return services.SetContainerStateDeleting(ctx, r.container, r.Client)
}

// deleteIfNodeSelectorMismatch checks if container's node selector matches the node.
// If not matched, sets container state to deleting.
func (r *containerReconcilerLoop) deleteIfNodeSelectorMismatch(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "deleteIfNodeSelectorMismatch")
	defer end()

	// No node means no labels to check
	if r.node == nil {
		return nil
	}

	// Do not delete containers without owner references - safety protection
	if len(r.container.GetOwnerReferences()) == 0 {
		return nil
	}

	if r.container.IsMarkedForDeletion() || r.container.IsDeletingState() || r.container.IsDestroyingState() {
		return nil
	}

	// Check if container's node selector matches the actual node
	if util.NodeSelectorMatchesNode(r.container.Spec.NodeSelector, r.node) {
		return nil
	}

	logger.Info("Container node selector doesn't match node, marking for deletion",
		"container", r.container.Name,
		"node", r.node.Name,
		"nodeSelector", r.container.Spec.NodeSelector)

	_ = r.RecordEvent(v1.EventTypeNormal, "NodeSelectorMismatch", "Node selector mismatch, deleting container")

	return services.SetContainerStateDeleting(ctx, r.container, r.Client)
}
