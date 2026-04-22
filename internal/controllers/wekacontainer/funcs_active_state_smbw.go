package wekacontainer

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

func (r *containerReconcilerLoop) IsSmbwClusterFormed(ctx context.Context) (bool, error) {
	cluster, err := r.getCluster(ctx)
	if err != nil {
		return false, err
	}

	return meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondSmbwClusterCreated), nil
}

func (r *containerReconcilerLoop) JoinSmbwCluster(ctx context.Context) error {
	isFormed, err := r.IsSmbwClusterFormed(ctx)
	if err != nil {
		return fmt.Errorf("error checking if SMB-W cluster is formed: %w", err)
	}
	if !isFormed {
		return lifecycle.NewWaitError(fmt.Errorf("SMB-W cluster is not formed yet, waiting for it to be formed"))
	}

	wekaService := services.NewWekaService(r.ExecService, r.container)
	return wekaService.JoinSmbwCluster(ctx, *r.container.Status.ClusterContainerID)
}

func (r *containerReconcilerLoop) RemoveFromSmbwCluster(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		return errors.New("Container ID is not set")
	}

	logger.SetValues("container_id", *containerId)

	executeInContainer := r.container

	if !NodeIsReady(r.node) || !CanExecInPod(r.pod) {
		containers, err := r.getClusterContainers(ctx)
		if err != nil {
			return err
		}
		executeInContainer = discovery.SelectActiveContainer(containers)
	}

	if executeInContainer == nil {
		return errors.New("No active container found")
	}

	logger.Info("Removing container from SMB-W cluster")

	wekaService := services.NewWekaService(r.ExecService, executeInContainer)
	return wekaService.RemoveFromSmbwCluster(ctx, *containerId)
}
