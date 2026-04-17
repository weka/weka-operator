package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clusterMinDrivesLargeFloor warns on large clusters
// (driveContainers > 10 AND numDrives > 2) when minNumDrives is below
// ceil(0.9 × driveContainers × numDrives), letting IO start after
// significant drive loss.
type clusterMinDrivesLargeFloor struct{}

func (clusterMinDrivesLargeFloor) ID() string {
	return "cluster_min_drives_large_floor"
}

func (clusterMinDrivesLargeFloor) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	if cluster.Spec.Dynamic == nil {
		return nil
	}
	minNumDrives := cluster.Spec.GetStartIoConditions().MinNumDrives
	if minNumDrives <= 0 {
		return nil
	}
	driveContainers := cluster.Spec.Dynamic.DriveContainers
	numDrives := cluster.Spec.Dynamic.NumDrives
	if driveContainers <= 10 || numDrives <= 2 {
		return nil
	}
	total := driveContainers * numDrives
	// floor is the integer-arithmetic equivalent of ceil(total * 9/10).
	floor := (9*total + 9) / 10
	if minNumDrives >= floor {
		return nil
	}
	detail := fmt.Sprintf(
		"spec.startIoConditions.minNumDrives (%d) is below the recommended 90%% floor "+
			"for large clusters (ceil(0.9 × %d × %d) = %d). The cluster will start IO "+
			"after losing more drives than recommended. Increase minNumDrives to at "+
			"least %d.",
		minNumDrives, driveContainers, numDrives, floor, floor,
	)
	return field.ErrorList{
		field.Invalid(
			field.NewPath("spec", "startIoConditions", "minNumDrives"),
			minNumDrives,
			detail,
		),
	}
}
