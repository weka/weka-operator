package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

	driveSide, computeSide, ok := templateCoreSides(cluster.Spec.Dynamic)
	if !ok {
		return nil
	}

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
