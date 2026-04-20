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
	currentContainers, err := discovery.GetClusterContainers(ctx, r.getClient(), r.cluster, "")
	if err != nil {
		return fmt.Errorf("failed to get cluster containers: %w", err)
	}

	r.containers = currentContainers
	return nil
}

func (r *wekaClusterReconcilerLoop) updateClusterStatusIfNotEquals(ctx context.Context, newStatus weka.WekaClusterStatusEnum) error {
	if r.cluster.Status.Status != newStatus {
		r.cluster.Status.Status = newStatus
		err := r.getClient().Status().Update(ctx, r.cluster)
		if err != nil {
			wrapErr := fmt.Errorf("failed to update cluster status: %w", err)
			return wrapErr
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

func (r *wekaClusterReconcilerLoop) HasRunningS3Containers() bool {
	nums := allocator.GetWekaContainerNumbers(r.cluster.Spec.Dynamic)

	c := discovery.SelectRunningContainersByRole(r.containers, nums.S3, weka.WekaContainerModeS3)
	return len(c) > 0
}

func (r *wekaClusterReconcilerLoop) HasRunningNfsContainers() bool {
	nums := allocator.GetWekaContainerNumbers(r.cluster.Spec.Dynamic)

	c := discovery.SelectRunningContainersByRole(r.containers, nums.Nfs, weka.WekaContainerModeNfs)
	return len(c) > 0
}

func (r *wekaClusterReconcilerLoop) HasRunningDataServicesContainers() bool {
	nums := allocator.GetWekaContainerNumbers(r.cluster.Spec.Dynamic)

	c := discovery.SelectRunningContainersByRole(r.containers, nums.DataServices, weka.WekaContainerModeDataServices)
	return len(c) > 0
}

func (r *wekaClusterReconcilerLoop) HasSmbwContainers() bool {
	return len(r.SelectSmbwContainers(r.containers)) > 0
}

func (r *wekaClusterReconcilerLoop) SelectSmbwContainers(containers []*weka.WekaContainer) []*weka.WekaContainer {
	var smbwContainers []*weka.WekaContainer
	for _, container := range containers {
		if container.Spec.Mode == weka.WekaContainerModeSmbw {
			smbwContainers = append(smbwContainers, container)
		}
	}

	return smbwContainers
}

// ValidateDriveTypesRatio validates that driveTypesRatio.tlc > 0 when driveTypesRatio is specified.
// This prevents QLC-only configurations which are not supported.
func (r *wekaClusterReconcilerLoop) ValidateDriveTypesRatio(ctx context.Context) error {
	cluster := r.cluster
	if cluster.Spec.Dynamic == nil {
		return nil
	}

	driveTypesRatio := cluster.Spec.Dynamic.DriveTypesRatio
	if driveTypesRatio == nil {
		return nil
	}

	if driveTypesRatio.Tlc == 0 {
		return fmt.Errorf("driveTypesRatio.tlc must be greater than 0; TLC-only and mixed TLC/QLC configurations are supported, but QLC-only is not allowed")
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) ClusterIsPaused() bool {
	p := r.cluster.Spec.GetOverrides().Paused
	return p != nil && *p
}

// ClusterIsExplicitlyUnpaused returns true when paused is explicitly set to false (not nil).
func (r *wekaClusterReconcilerLoop) ClusterIsExplicitlyUnpaused() bool {
	p := r.cluster.Spec.GetOverrides().Paused
	return p != nil && !*p
}

// ClusterStatusIsSuspended returns true when the cluster is in a suspended state
// that should be cleared once recovery is complete:
// - "Paused" (manual pause flow)
// - "GracePeriod" when deletion was cancelled (rescue flow)
func (r *wekaClusterReconcilerLoop) ClusterStatusIsSuspended() bool {
	if r.cluster.Status.Status == weka.WekaClusterStatusPaused {
		return true
	}
	return r.cluster.Status.Status == weka.WekaClusterStatusGracePeriod && r.ClusterDeletionCancelled()
}

func (r *wekaClusterReconcilerLoop) HasPausedContainers() bool {
	for _, container := range r.containers {
		if container.IsPaused() {
			return true
		}
	}
	return false
}
