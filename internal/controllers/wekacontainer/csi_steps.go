package wekacontainer

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

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
	if r.targetCluster != nil {
		return csi.GetGroupFromTargetCluster(r.targetCluster)
	}
	return csi.GetGroupFromClient(r.wekaClient)
}

func (r *containerReconcilerLoop) getCsiDriverName() string {
	return fmt.Sprintf("%s.weka.io", r.GetCSIGroup())
}

func (r *containerReconcilerLoop) ManageCsiTopologyLabels(ctx context.Context) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	csiDriverName := r.getCsiDriverName()
	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return errors.New("node affinity is not set")
	}

	activeMounts, err := r.getCachedActiveMounts(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get active mounts")
	}

	hasActiveMounts := activeMounts != nil && *activeMounts > 0

	csiTopologyLabelsService := operations.NewCsiTopologyLabelsService(csiDriverName, string(nodeName), r.container, hasActiveMounts)
	if !csiTopologyLabelsService.NodeHasExpectedCsiTopologyLabels(r.node) {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "UpdateNodeCsiTopologyLabels")
		defer end()

		expectedLabels := csiTopologyLabelsService.GetExpectedCsiTopologyLabels()
		logger.Info("Updating node with CSI topology labels", "labels", expectedLabels)

		node := csiTopologyLabelsService.UpdateNodeLabels(r.node, expectedLabels)

		err = r.Update(ctx, node)
		if err != nil {
			return errors.Wrap(err, "failed to update node with CSI topology labels")
		}
	}

	return nil
}

func (r *containerReconcilerLoop) UnsetCsiNodeTopologyLabels(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "UnsetCsiNodeTopologyLabels")
	defer end()

	csiDriverName := r.getCsiDriverName()
	nodeName := r.node.Name

	logger.Info("Unsetting CSI node topology labels", "node", r.node.Name, "csiDriverName", csiDriverName)

	csiTopologyLabelsService := operations.NewCsiTopologyLabelsService(csiDriverName, nodeName, r.container, false)
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
