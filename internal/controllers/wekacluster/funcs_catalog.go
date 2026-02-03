package wekacluster

import (
	"context"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// SelectDataServicesFEContainers returns all data-services-fe containers from the given list
func (r *wekaClusterReconcilerLoop) SelectDataServicesFEContainers(containers []*weka.WekaContainer) []*weka.WekaContainer {
	var feContainers []*weka.WekaContainer
	for _, container := range containers {
		if container.Spec.Mode == weka.WekaContainerModeDataServicesFe {
			feContainers = append(feContainers, container)
		}
	}
	return feContainers
}

// EnsureCatalogCluster creates the catalog cluster with initial data-services and data-services-fe containers
func (r *wekaClusterReconcilerLoop) EnsureCatalogCluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureCatalogCluster")
	defer end()

	container := discovery.SelectActiveContainer(r.containers)
	if container == nil {
		return errors.New("No active container found")
	}

	// Collect container IDs from data-services and data-services-fe containers
	// that have joined the cluster (have ClusterContainerID)
	var containerIds []int

	dataServicesContainers := r.SelectDataServicesContainers(r.containers)
	dataServicesFEContainers := r.SelectDataServicesFEContainers(r.containers)

	for _, c := range dataServicesContainers {
		if c.Status.ClusterContainerID != nil {
			containerIds = append(containerIds, *c.Status.ClusterContainerID)
		}
	}

	for _, c := range dataServicesFEContainers {
		if c.Status.ClusterContainerID != nil {
			containerIds = append(containerIds, *c.Status.ClusterContainerID)
		}
	}

	// Need at least 2 containers (1 data-services + 1 data-services-fe) to form catalog cluster
	if len(containerIds) < 2 {
		logger.Info("Not enough containers to form catalog cluster", "containerIds", len(containerIds))
		return errors.New("Need at least 2 containers (data-services + data-services-fe) to form catalog cluster")
	}

	wekaService := services.NewWekaService(r.ExecService, container)
	err := wekaService.CreateCatalogCluster(ctx, containerIds)
	if err != nil {
		return err
	}

	logger.Info("Catalog cluster ensured", "containerIds", containerIds)
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
