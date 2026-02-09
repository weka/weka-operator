package wekacluster

import (
	"context"
	"fmt"
	"time"

	"github.com/weka/go-lib/pkg/workers"
	"github.com/weka/go-weka-observability/instrumentation"
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

func (r *wekaClusterReconcilerLoop) ShouldSetComputeHugepages() bool {
	return r.cluster.Spec.Template == "dynamic" &&
		r.cluster.Spec.Dynamic != nil &&
		r.cluster.Spec.Dynamic.ComputeHugepages == 0 // skip if user explicitly set
}

// ensureComputeContainersHugepages patches compute containers' Spec.Hugepages based on
// actual drive capacity from sibling drive containers' AddedDrives.
// This handles the case where existing compute containers have stale hugepages values
// after upgrading the operator to a version with capacity-based hugepages calculation.
func (r *wekaClusterReconcilerLoop) ensureComputeContainersHugepages(ctx context.Context) error {
	cluster := r.cluster
	containers := r.containers

	tmpl := allocator.BuildDynamicTemplate(cluster.Spec.Dynamic)

	// Collect "goods" drive containers: those with all expected drives added and SizeBytes > 0
	var goodContainersCapacitySum int64
	goodContainersCount := 0
	for _, c := range containers {
		if !c.IsDriveContainer() {
			continue
		}
		if c.Status.Allocations == nil {
			continue
		}
		expectedDrives := 0
		if c.UsesDriveSharing() {
			expectedDrives = len(c.Status.Allocations.VirtualDrives)
		} else {
			expectedDrives = len(c.Status.Allocations.Drives)
		}
		if expectedDrives == 0 || len(c.Status.AddedDrives) != expectedDrives {
			continue
		}
		containerBytes := int64(0)
		allHaveSize := true
		for _, drive := range c.Status.AddedDrives {
			if drive.SizeBytes == 0 {
				allHaveSize = false
				break
			}
			containerBytes += drive.SizeBytes
		}
		if !allHaveSize {
			continue
		}
		goodContainersCapacitySum += containerBytes
		goodContainersCount++
	}

	if goodContainersCount < 5 {
		return nil // not enough good drive containers yet for reliable extrapolation
	}

	// Extrapolate: avg per-container capacity × expected drive container count
	avgPerContainer := goodContainersCapacitySum / int64(goodContainersCount)
	totalRawBytes := avgPerContainer * int64(tmpl.DriveContainers)
	totalRawCapacityGiB := int(totalRawBytes / (1024 * 1024 * 1024))

	desired := allocator.ComputeCapacityBasedHugepages(
		totalRawCapacityGiB, tmpl.ComputeContainers, tmpl.ComputeCores, tmpl.DriveTypesRatio)

	// Collect compute containers that need updating
	var computeContainers []*weka.WekaContainer
	for _, c := range containers {
		if c.IsComputeContainer() && c.Spec.Hugepages < desired {
			computeContainers = append(computeContainers, c)
		}
	}

	if len(computeContainers) == 0 {
		return nil
	}

	// Patch compute containers in parallel
	return workers.ProcessConcurrently(ctx, computeContainers, 32,
		func(ctx context.Context, c *weka.WekaContainer) error {
			ctx, _, end := instrumentation.GetLogSpan(ctx, "patchComputeContainerHugepages",
				"container", c.Name, "currentHugepages", c.Spec.Hugepages, "desiredHugepages", desired)
			defer end()

			patch := client.MergeFrom(c.DeepCopy())
			c.Spec.Hugepages = desired
			return r.getClient().Patch(ctx, c, patch)
		}).AsError()
}
