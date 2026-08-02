package wekacontainer

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/pkg/util"
)

const (
	nodeRemovedKey                     = "NodeRemoved"
	nodeRemovalGracePeriod             = 24 * time.Hour   // non-cloud: node may return slowly
	nodeRemovalGracePeriodManagedCloud = 30 * time.Minute // managed cloud (aws/oci); aligns w/ managedNodesPodTerminationTimeout
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

	affinity := r.container.GetNodeAffinity()
	if affinity == "" {
		return nil
	}

	node, err := r.KubeService.GetNode(ctx, k8sTypes.NodeName(affinity))
	nodePresent := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	// For non-backend containers the toggle never applied: keep immediate cleanup.
	mode := config.Config.CleanupRemovedNodes
	if container.IsBackend() {
		switch mode {
		case config.CleanupRemovedNodesOff:
			return nil
		case config.CleanupRemovedNodesAuto:
			onCloud, err := r.removedNodeOnSupportedCloud(ctx, node)
			if err != nil {
				return err
			}
			gracePeriod := nodeRemovalGracePeriod
			if onCloud {
				gracePeriod = nodeRemovalGracePeriodManagedCloud
			}
			return r.handleBackendNodeRemovalGrace(ctx, nodePresent, gracePeriod)
		}
		// CleanupRemovedNodesOn falls through to immediate delete below.
	}

	if nodePresent {
		return nil
	}
	deleteError := r.Delete(ctx, r.container)
	if deleteError != nil {
		return deleteError
	}
	return lifecycle.NewWaitError(errors.New("Node is not found, deleting container"))
}

// removedNodeOnSupportedCloud reports whether the container's affinity node is on a supported cloud
// provider (aws/oci). When the node is still present its ProviderID is checked directly. Once the node
// object is gone its ProviderID is no longer available, so the provider is inferred from any other live
// node in the cluster: a Weka cluster's nodes are homogeneous (all on the same cloud), so any surviving
// node answers the question without needing the removed node's own ProviderID persisted anywhere.
func (r *containerReconcilerLoop) removedNodeOnSupportedCloud(ctx context.Context, node *v1.Node) (bool, error) {
	if node != nil {
		return discovery.IsSupportedCloudProvider(node.Spec.ProviderID), nil
	}

	nodes, err := r.KubeService.GetNodes(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("failed to list nodes to determine cloud provider: %w", err)
	}
	for i := range nodes {
		if discovery.IsSupportedCloudProvider(nodes[i].Spec.ProviderID) {
			return true, nil
		}
	}
	return false, nil
}

// handleBackendNodeRemovalGrace implements CleanupRemovedNodesAuto for backend containers:
// when the affinity node is gone, hold the container in Stale status for nodeRemovalGracePeriod
// before deleting it. If the node returns before the grace period elapses, the stamp is cleared.
func (r *containerReconcilerLoop) handleBackendNodeRemovalGrace(ctx context.Context, nodePresent bool, gracePeriod time.Duration) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "handleBackendNodeRemovalGrace")
	defer logger.End()

	if r.container.Status.Timestamps == nil {
		r.container.Status.Timestamps = make(map[string]metav1.Time)
	}

	if nodePresent {
		// Node came back — clear any grace stamp and let the normal flow recover the status.
		if _, ok := r.container.Status.Timestamps[nodeRemovedKey]; ok {
			delete(r.container.Status.Timestamps, nodeRemovedKey)
			if err := r.Status().Update(ctx, r.container); err != nil {
				return err
			}
		}
		return nil
	}

	// Node is gone.
	// The backend pod on a removed node has no live process and holds no drives; reap it immediately
	// (strip the do-not-force-delete finalizer + delete) so it is not stuck in Terminating for the whole
	// Stale window. The container itself stays Stale — only the dead pod object is removed.
	if r.pod != nil && (r.pod.Status.Phase == v1.PodSucceeded || r.pod.Status.Phase == v1.PodFailed) {
		if err := r.deletePod(ctx, r.pod); err != nil {
			return err
		}
	}

	stamp, ok := r.container.Status.Timestamps[nodeRemovedKey]
	if !ok {
		r.container.Status.Timestamps[nodeRemovedKey] = metav1.Time{Time: time.Now()}
		r.container.Status.Status = weka.Stale
		// New timestamp is a data change, so persist unconditionally; a single status-subresource
		// update carries both the Stale status and the stamp.
		if err := r.Status().Update(ctx, r.container); err != nil {
			return err
		}
		return lifecycle.NewWaitErrorWithDuration(
			errors.New("backend node removed, marking container Stale and waiting before removal"),
			time.Second*15,
		)
	}

	if time.Since(stamp.Time) < gracePeriod {
		if err := r.updateContainerStatusIfNotEquals(ctx, weka.Stale); err != nil {
			return err
		}
		logger.Info("backend node removed, within grace period, waiting before removal",
			"waited", time.Since(stamp.Time).String(),
		)
		return lifecycle.NewWaitErrorWithDuration(
			errors.New("backend node removed, within grace period"),
			time.Second*30,
		)
	}

	// Grace elapsed and node still gone — remove the container.
	_ = r.RecordEvent( //nolint:errcheck // error return value intentionally not checked
		v1.EventTypeNormal,
		"BackendNodeRemovedGraceElapsed",
		"backend node removed for longer than the grace period, deleting container",
	)
	if err := r.Delete(ctx, r.container); err != nil {
		return err
	}
	return lifecycle.NewWaitError(errors.New("backend node removed past grace period, deleting container"))
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
	// ForceDrain is the deliberate exception. The drain machinery lives in waitForMountsOrDrain, which
	// exists only in the deleting and destroying flows, and it cannot simply be lifted into the active
	// flow -- ensurePod runs later in the same flow and would race to recreate the pod invokeDrain just
	// stopped. Holding here would therefore silently neuter the override, so the transition is allowed
	// through instead.
	//
	// This is the least-bad option rather than a clean one, on two counts. ForceDrain is inherited from
	// the parent WekaClient onto every one of its containers, so an operator who set it for ordinary
	// teardown has also opted every node under that client out of the reversibility above: a mistaken
	// selector edit there is still irreversible. And ForceDrain alone stops the client without
	// unmounting on the host -- that is UmountOnHost, a separate override -- so a stale mount can still
	// wedge waitForMountsOrDrain after the object has been deleted.
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
			_ = r.RecordEventThrottled(v1.EventTypeWarning, "NodeSelectorMismatchDrainPending", //nolint:errcheck // error return value intentionally not checked
				fmt.Sprintf("Node selector mismatch on node %s, but the active mount count is unset; not retiring container", r.node.Name),
				time.Minute)
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
