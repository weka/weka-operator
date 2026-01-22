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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	err := r.stopAndEnsureNoPod(ctx)
	if err != nil {
		return err
	}

	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		return nil
	}

	containers, err := r.getClusterContainers(ctx)
	if err != nil {
		return err
	}
	executeInContainer := discovery.SelectActiveContainer(containers)

	wekaService := services.NewWekaService(r.ExecService, executeInContainer)
	err = wekaService.RemoveFromSmbwCluster(ctx, *containerId)
	if err != nil {
		// Don't fail if SMB-W cluster doesn't exist
		if err.Error() == "SMB-W cluster is not configured" {
			logger.Info("SMB-W cluster is not configured, skipping removal", "container_id", *containerId)
			return nil
		}
		return errors.Wrap(err, "failed to remove from SMB-W cluster")
	}
	return nil
}
