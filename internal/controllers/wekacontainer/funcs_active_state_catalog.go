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

// IsCatalogClusterFormed checks if the catalog cluster has been created at the cluster level
func (r *containerReconcilerLoop) IsCatalogClusterFormed(ctx context.Context) (bool, error) {
	cluster, err := r.getCluster(ctx)
	if err != nil {
		return false, err
	}

	return meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondCatalogClusterCreated), nil
}

// JoinCatalogCluster adds this container to the catalog cluster
func (r *containerReconcilerLoop) JoinCatalogCluster(ctx context.Context) error {
	isFormed, err := r.IsCatalogClusterFormed(ctx)
	if err != nil {
		return fmt.Errorf("error checking if catalog cluster is formed: %w", err)
	}
	if !isFormed {
		return lifecycle.NewWaitError(fmt.Errorf("catalog cluster is not formed yet, waiting for it to be formed"))
	}

	wekaService := services.NewWekaService(r.ExecService, r.container)
	return wekaService.JoinCatalogCluster(ctx, *r.container.Status.ClusterContainerID)
}

// RemoveFromCatalogCluster removes this container from the catalog cluster before deactivation
func (r *containerReconcilerLoop) RemoveFromCatalogCluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	// Stop pod first (similar to S3 pattern)
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

	if executeInContainer == nil {
		return errors.New("No active container found")
	}

	wekaService := services.NewWekaService(r.ExecService, executeInContainer)
	err = wekaService.RemoveFromCatalogCluster(ctx, *containerId)
	if err != nil {
		return err
	}

	logger.Info("Removed container from catalog cluster", "container_id", *containerId)
	return nil
}
