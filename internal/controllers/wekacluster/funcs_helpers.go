package wekacluster

import (
	"context"
	"fmt"
	"time"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/services/discovery"
)

func (r *wekaClusterReconcilerLoop) getClient() client.Client {
	return r.Manager.GetClient()
}

func (r *wekaClusterReconcilerLoop) getCurrentContainers(ctx context.Context) error {
	currentContainers := discovery.GetClusterContainers(ctx, r.getClient(), r.cluster, "")
	r.containers = currentContainers
	return nil
}

func (r *wekaClusterReconcilerLoop) updateClusterStatusIfNotEquals(ctx context.Context, newStatus weka.WekaClusterStatusEnum) error {
	if r.cluster.Status.Status != newStatus {
		r.cluster.Status.Status = newStatus
		err := r.getClient().Status().Update(ctx, r.cluster)
		if err != nil {
			err := fmt.Errorf("failed to update cluster status: %w", err)
			return err
		}
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) ClusterIsInGracefulDeletion() bool {
	if !r.cluster.IsMarkedForDeletion() {
		return false
	}

	deletionTime := r.cluster.GetDeletionTimestamp().Time
	gracefulDestroyDuration := r.cluster.GetGracefulDestroyDuration()
	hitTimeout := deletionTime.Add(gracefulDestroyDuration)

	return hitTimeout.After(time.Now())
}

func (r *wekaClusterReconcilerLoop) HasPostFormClusterScript() bool {
	return r.cluster.Spec.GetOverrides().PostFormClusterScript != ""
}

func (r *wekaClusterReconcilerLoop) HasS3Containers() bool {
	cluster := r.cluster

	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		return false
	}
	if template.S3Containers == 0 {
		return false
	}

	for _, container := range r.containers {
		if container.Spec.Mode == weka.WekaContainerModeS3 {
			return true
		}
	}

	return false
}

func (r *wekaClusterReconcilerLoop) HasNfsContainers() bool {
	return len(r.SelectNfsContainers(r.containers)) > 0
}

func (r *wekaClusterReconcilerLoop) SelectS3Containers(containers []*weka.WekaContainer) []*weka.WekaContainer {
	var s3Containers []*weka.WekaContainer
	for _, container := range containers {
		if container.Spec.Mode == weka.WekaContainerModeS3 {
			s3Containers = append(s3Containers, container)
		}
	}

	return s3Containers
}

func (r *wekaClusterReconcilerLoop) SelectNfsContainers(containers []*weka.WekaContainer) []*weka.WekaContainer {
	var nfsContainers []*weka.WekaContainer
	for _, container := range containers {
		if container.Spec.Mode == weka.WekaContainerModeNfs {
			nfsContainers = append(nfsContainers, container)
		}
	}

	return nfsContainers
}

func (r *wekaClusterReconcilerLoop) HasDataServicesContainers() bool {
	return len(r.SelectDataServicesContainers(r.containers)) > 0
}

func (r *wekaClusterReconcilerLoop) SelectDataServicesContainers(containers []*weka.WekaContainer) []*weka.WekaContainer {
	var dataServicesContainers []*weka.WekaContainer
	for _, container := range containers {
		if container.Spec.Mode == weka.WekaContainerModeDataServices {
			dataServicesContainers = append(dataServicesContainers, container)
		}
	}

	return dataServicesContainers
}

func (r *wekaClusterReconcilerLoop) selectDataServicesFEContainers(containers []*weka.WekaContainer) []*weka.WekaContainer {
	var feContainers []*weka.WekaContainer
	for _, container := range containers {
		if container.Spec.Mode == weka.WekaContainerModeDataServicesFe {
			feContainers = append(feContainers, container)
		}
	}

	return feContainers
}