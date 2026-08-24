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
	if r.node == nil {
		return errors.New("node is not set")
	}

	node := r.node
	pod := r.pod

	if !NodeIsReady(node) {
		ctx, logger := instrumentation.CreateLogSpan(ctx, "NodeNotReady", "node", node.Name) //nolint:govet // shadowed ctx intentionally scoped to this block
		defer logger.End()

		err := fmt.Errorf("node %s is not ready", node.Name)

		_ = r.RecordEventThrottled(v1.EventTypeWarning, "NodeNotReady", err.Error(), time.Minute) //nolint:errcheck // error return value intentionally not checked

		if !r.container.IsDriversContainer() {
			logger.Info("Skipping pod deletion on NotReady node for non-drivers container")
			return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
		}

		// if node is not ready, we should terminate the pod and let it be rescheduled
		if pod != nil && pod.Status.Phase == v1.PodRunning {
			logger.Info("Deleting pod on NotReady node", "pod", pod.Name)
			deleteErr := r.deletePod(ctx, pod)
			return lifecycle.NewWaitErrorWithDuration(
				fmt.Errorf("deleting pod on NotReady node, err: %w", deleteErr),
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
				deleteError := r.Delete(ctx, r.container)
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
	ctx, logger := instrumentation.CreateLogSpan(ctx, "deleteIfTolerationsMismatch")
	defer logger.End()

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

	_ = r.RecordEvent(v1.EventTypeNormal, "TolerationMismatch", "Toleration mismatch, deleting container") //nolint:errcheck // error return value intentionally not checked

	return services.SetContainerStateDeleting(ctx, r.container, r.Client)
}

// deleteIfNodeSelectorMismatch checks if container's node selector matches the node.
// If not matched, sets container state to deleting.
func (r *containerReconcilerLoop) deleteIfNodeSelectorMismatch(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "deleteIfNodeSelectorMismatch")
	defer logger.End()

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

	// A client container with active mounts must not enter the Deleting flow yet.
	//
	// That flow self-deletes the container object up front (handleStateDeleting) and only then waits for
	// mounts to drain — and a deleted object cannot be revived. Transitioning now would therefore make
	// retirement irreversible: if the label is restored while mounts are still outstanding, nothing can
	// bring the container back. Holding it here instead keeps it in the active flow, where this very
	// check re-runs on every reconcile, so restoring the label simply resumes normal service. Once the
	// mounts are gone we fall through and the existing deletion path runs unchanged.
	//
	// ForceDrain is the deliberate exception: the drain machinery lives in waitForMountsOrDrain, which
	// exists only in the deleting and destroying flows, so holding here would silently stop that
	// override from ever taking effect. Someone who set it has already declared the mounts expendable
	// and wants retirement to complete on its own, so let the transition through.
	if r.container.IsClientContainer() && !r.container.Spec.GetOverrides().ForceDrain {
		activeMounts, err := r.GetActiveMounts(ctx)

		// Every path below holds without erroring. The step has no ContinueOnError, so returning an
		// error here would defer every remaining active-flow step -- ensurePod among them -- and a client
		// pod lost mid-hold with nothing to recreate it could never drain its mounts. That would trade
		// this deadlock for another one, reachable from a merely transient node-agent failure.
		switch {
		case err != nil:
			// Not knowing is not the same as knowing there are none: never read a node-agent failure as
			// "safe to retire".
			logger.Info("Node selector mismatch, holding container because active mounts are unknown",
				"container", r.container.Name,
				"node", r.node.Name,
				"error", err.Error())
			_ = r.RecordEventThrottled(v1.EventTypeWarning, "NodeSelectorMismatchDrainPending", //nolint:errcheck // error return value intentionally not checked
				fmt.Sprintf("Node selector mismatch on node %s, but active mounts could not be determined (%v); not retiring container", r.node.Name, err),
				time.Minute)
			return nil

		case activeMounts == nil:
			// Defensive: fetchActiveMounts does not return (nil, nil) today. Hold rather than retire, for
			// the same reason as above -- waitForMountsOrDrain treats an unset count as fatal.
			logger.Info("Node selector mismatch, holding container because active mounts are unset",
				"container", r.container.Name,
				"node", r.node.Name)
			return nil

		case *activeMounts > 0:
			// The status column is deliberately left to reconcileWekaLocalStatus: asserting Draining from
			// the active flow would flap, since IsStatusOverwritableByLocal does not protect it.
			logger.Info("Node selector mismatch, holding container while mounts are still active",
				"container", r.container.Name,
				"node", r.node.Name,
				"activeMounts", *activeMounts)
			_ = r.RecordEventThrottled(v1.EventTypeWarning, "NodeSelectorMismatchDrainPending", //nolint:errcheck // error return value intentionally not checked
				activeMountsHoldMessage(r.node.Name, *activeMounts), time.Minute)
			return nil
		}
	}

	logger.Info("Container node selector doesn't match node, marking for deletion",
		"container", r.container.Name,
		"node", r.node.Name,
		"nodeSelector", r.container.Spec.NodeSelector)

	_ = r.RecordEvent(v1.EventTypeNormal, "NodeSelectorMismatch", "Node selector mismatch, deleting container") //nolint:errcheck // error return value intentionally not checked

	return services.SetContainerStateDeleting(ctx, r.container, r.Client)
}
