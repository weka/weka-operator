package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// clusterComputeDriveCoresFloor enforces the hard 1:1 floor between total compute and drive cores
// (compute containers front every drive); capacity planners enforce the same floor. See
// clusterDriveComputeCoreRatio for the softer recommended-ratio advisory check.
//
// Planner-managed templates are out of scope: cores there are assigned by the planner, not by
// GetWekaContainerCores, so the numbers this check reads are not the ones the cluster runs on.
type clusterComputeDriveCoresFloor struct{}

func (clusterComputeDriveCoresFloor) ID() string {
	return "cluster_compute_drive_cores_floor"
}

func (clusterComputeDriveCoresFloor) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	if cluster.Spec.Dynamic == nil {
		return nil
	}

	// clusterCapacity sizes both sides through the planner: computeCores/driveCores left at 0 mean
	// "auto-derive" there (funcs_fd_planning.go), and the compute containers are built from
	// plan.ComputeCores/plan.ComputeLayout rather than the template. Reading GetWekaContainerCores'
	// static-template defaults (unset -> 1 core) would compare numbers the cluster never uses and reject
	// a plan the planner sizes correctly. Auto full drives is excluded by the count guard below.
	if allocator.IsPlannerManaged(cluster.Spec.Dynamic) {
		return nil
	}

	driveContainers := cluster.Spec.Dynamic.DriveContainers
	computeContainers := cluster.Spec.Dynamic.ComputeContainers
	if driveContainers <= 0 || computeContainers <= 0 {
		return nil
	}

	cores := allocator.GetWekaContainerCores(cluster.Spec.Dynamic)
	driveSide := driveContainers * cores.Drive
	computeSide := computeContainers * cores.Compute

	if computeSide >= driveSide {
		return nil
	}

	detail := fmt.Sprintf(
		"total compute cores (%d) is below total drive cores (%d). A cluster must have at least one "+
			"compute core per drive core; raise computeContainers or computeCores (or lower the drive "+
			"side). Capacity planners enforce the same floor and will report the plan infeasible.",
		computeSide, driveSide,
	)
	return field.ErrorList{
		field.Invalid(field.NewPath("spec", "dynamicTemplate"), computeSide, detail),
	}
}
