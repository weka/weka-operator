package wekacontainer

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/operations/csi"
)

func (r *containerReconcilerLoop) WekaContainerManagesCsi() bool {
	return r.container.IsClientContainer() && config.Config.Csi.Enabled
}

func CsiSteps(r *containerReconcilerLoop) []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{
			Name: "ManageCsiTopologyLabels",
			Run:  r.ManageCsiTopologyLabels,
			Predicates: lifecycle.Predicates{
				r.WekaContainerManagesCsi,
				r.NodeIsSet,
			},
			ContinueOnError: true,
		},
	}
}

func (r *containerReconcilerLoop) GetCSIGroup() string {
	return csi.ResolveGroup(r.targetCluster, r.wekaClient)
}

func (r *containerReconcilerLoop) getCsiDriverName() string {
	return fmt.Sprintf("%s.weka.io", r.GetCSIGroup())
}

func (r *containerReconcilerLoop) ManageCsiTopologyLabels(ctx context.Context) error {

	csiDriverName := r.getCsiDriverName()
	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return errors.New("node affinity is not set")
	}

	csiTopologyLabelsService := operations.NewCsiTopologyLabelsService(csiDriverName, string(nodeName), r.container)
	if !csiTopologyLabelsService.NodeHasExpectedCsiTopologyLabels(r.node) {
		spanCtx, logger := instrumentation.CreateLogSpan(ctx, "UpdateNodeCsiTopologyLabels")
		defer logger.End()
		ctx = spanCtx

		expectedLabels := csiTopologyLabelsService.GetExpectedCsiTopologyLabels()
		logger.Info("Updating node with CSI topology labels", "labels", expectedLabels)

		node := csiTopologyLabelsService.UpdateNodeLabels(r.node, expectedLabels)

		err := r.Update(ctx, node)
		if err != nil {
			return errors.Wrap(err, "failed to update node with CSI topology labels")
		}
	}

	return nil
}

func (r *containerReconcilerLoop) UnsetCsiNodeTopologyLabels(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "UnsetCsiNodeTopologyLabels")
	defer logger.End()

	csiDriverName := r.getCsiDriverName()
	nodeName := r.node.Name

	logger.Info("Unsetting CSI node topology labels", "node", r.node.Name, "csiDriverName", csiDriverName)

	csiTopologyLabelsService := operations.NewCsiTopologyLabelsService(csiDriverName, nodeName, r.container)
	node := csiTopologyLabelsService.UpdateNodeLabels(r.node, nil)

	err := r.Update(ctx, node)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("node is deleted, no need for cleanup")
			return nil
		}
		return errors.Wrap(err, "failed to update node to unset CSI topology labels")
	}

	return nil
}

// ManageCsiNodeRetainLabel marks this node as one the csi-node plugin must not be descheduled from.
//
// This runs on every active reconcile so the label is already in place long before a user edits the
// client-selector label. That ordering is the point: the DaemonSet controller reacts to a label change
// within milliseconds, so a marker stamped only after observing the removal would race it and fail
// intermittently under load. UnsetCsiNodeRetainLabel releases it, from finalizeContainer, once the
// container's mounts have drained.
func (r *containerReconcilerLoop) ManageCsiNodeRetainLabel(ctx context.Context) error {
	if r.wekaClient == nil {
		// The key is per-client, so without the owning WekaClient there is nothing to claim. Skipping is
		// the fail-safe direction: it leaves placement exactly as it is today.
		return nil
	}

	retainLabel := csi.GetCsiNodeRetainLabel(r.wekaClient.Namespace, r.wekaClient.Name)
	if r.node.Labels[retainLabel] == csi.CsiNodeRetainLabelValue {
		return nil
	}

	ctx, logger := instrumentation.CreateLogSpan(ctx, "ManageCsiNodeRetainLabel")
	defer logger.End()

	// Patch rather than Update: sibling steps in this same reconcile also write node labels, and a
	// full-object Update would conflict on resourceVersion.
	base := r.node.DeepCopy()
	if r.node.Labels == nil {
		r.node.Labels = map[string]string{}
	}
	r.node.Labels[retainLabel] = csi.CsiNodeRetainLabelValue

	logger.Info("Retaining csi-node on node for serving client container", "node", r.node.Name)

	if err := r.Patch(ctx, r.node, crclient.MergeFrom(base)); err != nil {
		return errors.Wrap(err, "failed to set csi-node retain label on node")
	}

	return nil
}

// UnsetCsiNodeRetainLabel releases the retain marker, allowing the DaemonSet controller to withdraw
// the csi-node plugin from this node if the client selector no longer matches it.
//
// Called from finalizeContainer, which runs after waitForMountsOrDrain — so by this point there is
// nothing left to unmount. This restores the teardown ordering that CleanupCsiNodeServerPod used to
// provide from the same call site before csi-node became a DaemonSet.
func (r *containerReconcilerLoop) UnsetCsiNodeRetainLabel(ctx context.Context) error {
	if r.wekaClient == nil {
		return nil
	}

	retainLabel := csi.GetCsiNodeRetainLabel(r.wekaClient.Namespace, r.wekaClient.Name)
	if _, ok := r.node.Labels[retainLabel]; !ok {
		return nil
	}

	ctx, logger := instrumentation.CreateLogSpan(ctx, "UnsetCsiNodeRetainLabel")
	defer logger.End()

	base := r.node.DeepCopy()
	delete(r.node.Labels, retainLabel)

	logger.Info("Releasing csi-node retain label, mounts are drained", "node", r.node.Name)

	if err := r.Patch(ctx, r.node, crclient.MergeFrom(base)); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("node is deleted, no need for cleanup")
			return nil
		}
		return errors.Wrap(err, "failed to remove csi-node retain label from node")
	}

	return nil
}
