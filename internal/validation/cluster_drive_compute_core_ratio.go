package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clusterDriveComputeCoreRatio warns when the drive:compute core ratio
// exceeds the recommended maximum of 1:2 — i.e. drive cores >
// compute cores / 2. Compute does more CPU-bound work per I/O than
// drive (filesystem, RAID, client multiplexing); past 1:2 the cluster
// saturates the front end under load while drives sit idle.
//
// Cores are evaluated using max(*Cores, 1) to mirror the operator's
// allocator.GetWekaContainerCores() default (zero → 1), so the webhook
// sees the same effective ratio the reconciler will commit to. Skipped
// when either side has zero containers.
type clusterDriveComputeCoreRatio struct{}

func (clusterDriveComputeCoreRatio) ID() string {
	return "cluster_drive_compute_core_ratio"
}

func (clusterDriveComputeCoreRatio) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	if cluster.Spec.Dynamic == nil {
		return nil
	}

	driveContainers := cluster.Spec.Dynamic.DriveContainers
	computeContainers := cluster.Spec.Dynamic.ComputeContainers
	if driveContainers <= 0 || computeContainers <= 0 {
		return nil
	}

	driveCores := coresOrOne(cluster.Spec.Dynamic.DriveCores)
	computeCores := coresOrOne(cluster.Spec.Dynamic.ComputeCores)
	driveSide := driveContainers * driveCores
	computeSide := computeContainers * computeCores

	if 2*driveSide <= computeSide {
		return nil
	}

	n, m := reduceRatio(driveSide, computeSide)
	ratio := fmt.Sprintf("%d:%d", n, m)
	detail := fmt.Sprintf(
		"drive:compute core ratio exceeds the recommended maximum of 1:2 "+
			"(drive containers: %d, compute cores: %d, ratio: %s). "+
			"Adjust driveContainers or compute core count to restore a "+
			"valid ratio.",
		driveContainers, computeCores, ratio,
	)
	return field.ErrorList{
		field.Invalid(
			field.NewPath("spec", "dynamicTemplate"),
			ratio,
			detail,
		),
	}
}

// coresOrOne mirrors util.GetNonZeroOrDefault(_, 1) used by the operator's
// allocator.GetWekaContainerCores() — a 0 spec value becomes 1 at
// reconcile time.
func coresOrOne(cores int) int {
	if cores <= 0 {
		return 1
	}
	return cores
}

// reduceRatio divides both sides by their gcd so the message reads
// `1:2` rather than `6:12`.
func reduceRatio(a, b int) (int, int) {
	g := gcd(a, b)
	if g == 0 {
		return a, b
	}
	return a / g, b / g
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
