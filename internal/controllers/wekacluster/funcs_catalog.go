package wekacluster

import (
	"context"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"

	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// EnsureCatalogCluster creates the catalog cluster with data-services containers.
// Note: Only data-services container IDs are used for catalog cluster creation,
// not data-services-fe container IDs. The FE containers join the catalog cluster
// separately via JoinCatalogCluster.
func (r *wekaClusterReconcilerLoop) EnsureCatalogCluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureCatalogCluster")
	defer end()

	container := discovery.SelectActiveContainer(r.containers)
	if container == nil {
		return errors.New("No active container found")
	}

	// Collect container IDs from data-services containers that have a ready FE on the same node
	// (not data-services-fe - those join separately)
	var containerIds []int

	dataServicesContainers := r.SelectDataServicesContainers(r.containers)
	dataServicesFEContainers := r.selectDataServicesFEContainers(r.containers)

	// Build a map of node -> ready FE container for quick lookup
	readyFEByNode := make(map[string]bool)
	for _, fe := range dataServicesFEContainers {
		// FE is ready if it has a ClusterContainerID and is running on a node
		if fe.Status.ClusterContainerID != nil && fe.GetNodeAffinity() != "" {
			readyFEByNode[string(fe.GetNodeAffinity())] = true
		}
	}

	for _, c := range dataServicesContainers {
		if c.Status.ClusterContainerID == nil {
			continue
		}
		// Only include if there's a ready FE on the same node
		nodeAffinity := string(c.GetNodeAffinity())
		if nodeAffinity != "" && readyFEByNode[nodeAffinity] {
			containerIds = append(containerIds, *c.Status.ClusterContainerID)
		}
	}

	// Need at least 2 data-services containers with ready FE to form catalog cluster
	if len(containerIds) < 2 {
		logger.Info("Not enough data-services containers with ready FE to form catalog cluster",
			"eligibleContainers", len(containerIds),
			"totalDataServicesContainers", len(dataServicesContainers),
			"totalFEContainers", len(dataServicesFEContainers))
		return errors.New("Need at least 2 data-services containers with ready FE to form catalog cluster")
	}

	wekaService := services.NewWekaService(r.ExecService, container)

	// Set the catalog cluster port before creating the cluster
	if err := wekaService.SetCatalogClusterPort(ctx, r.cluster.Status.Ports.DataServicesPort); err != nil {
		return err
	}

	err := wekaService.CreateCatalogCluster(ctx, containerIds)
	if err != nil {
		return err
	}

	logger.Info("Catalog cluster ensured", "containerIds", containerIds, "port", r.cluster.Status.Ports.DataServicesPort)
	return nil
}

// ShouldConfigureCatalog checks if catalog configuration needs to be applied
func (r *wekaClusterReconcilerLoop) ShouldConfigureCatalog() bool {
	return r.cluster.Spec.Catalog != nil &&
		(r.cluster.Spec.Catalog.IndexInterval != "" || r.cluster.Spec.Catalog.RetentionPeriod != "")
}

// EnsureCatalogConfig applies the catalog configuration (index-interval, retention-period)
func (r *wekaClusterReconcilerLoop) EnsureCatalogConfig(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureCatalogConfig")
	defer end()

	container := discovery.SelectActiveContainer(r.containers)
	if container == nil {
		return errors.New("No active container found")
	}

	if r.cluster.Spec.Catalog == nil {
		return nil
	}

	wekaService := services.NewWekaService(r.ExecService, container)
	params := services.CatalogConfigParams{
		IndexInterval:   r.cluster.Spec.Catalog.IndexInterval,
		RetentionPeriod: r.cluster.Spec.Catalog.RetentionPeriod,
	}

	err := wekaService.UpdateCatalogConfig(ctx, params)
	if err != nil {
		return err
	}

	logger.Info("Catalog config updated", "indexInterval", params.IndexInterval, "retentionPeriod", params.RetentionPeriod)
	return nil
}
