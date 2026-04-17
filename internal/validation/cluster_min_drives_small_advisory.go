package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clusterMinDrivesSmallAdvisory warns when minNumDrives is set on a
// "small" cluster (driveContainers ≤ 10 OR numDrives ≤ 2). Mutually
// exclusive with clusterMinDrivesLargeFloor by construction. Skipped
// when either count is 0 (operator-derived).
type clusterMinDrivesSmallAdvisory struct{}

func (clusterMinDrivesSmallAdvisory) ID() string {
	return "cluster_min_drives_small_advisory"
}

func (clusterMinDrivesSmallAdvisory) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
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
	if driveContainers <= 0 || numDrives <= 0 {
		return nil
	}
	if driveContainers > 10 && numDrives > 2 {
		return nil // large cluster — clusterMinDrivesLargeFloor handles this
	}
	detail := fmt.Sprintf(
		"spec.startIoConditions.minNumDrives (%d) is set on a small cluster "+
			"(driveContainers=%d, numDrives=%d). For clusters of this size, "+
			"minNumDrives is usually unnecessary and may cause unexpected "+
			"behavior. Consider omitting the field.",
		minNumDrives, driveContainers, numDrives,
	)
	return field.ErrorList{
		field.Invalid(
			field.NewPath("spec", "startIoConditions", "minNumDrives"),
			minNumDrives,
			detail,
		),
	}
}
