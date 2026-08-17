package validation

import (
	"context"
	"fmt"
	"math"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// clusterDriveComputeCoreRatio warns when the drive:compute core ratio exceeds the recommended maximum
// for the cluster's mode (globalconfig.Config.CapacityPlanner.{ComputeToTlcDriveCoreRatio,
// FullDrivesComputeToDriveCoreRatio}) — past that, compute-bound work saturates the front end while
// drives sit idle. Cores are resolved via allocator.GetWekaContainerCores(), matching the reconciler, so
// auto-derived drive cores count too — except under clusterCapacity/auto-full-drives, where the planner
// assigns both sides and the template's numbers are not the ones the cluster runs on. Skips cases
// already below clusterComputeDriveCoresFloor's hard 1:1 floor, which owns those exclusively.
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

	config := cluster.Spec.Dynamic
	// Same exclusion as clusterComputeDriveCoresFloor: under clusterCapacity the planner assigns both
	// sides, so GetWekaContainerCores' template defaults describe a cluster that never exists.
	if allocator.IsPlannerManaged(config) {
		return nil
	}

	driveContainers := config.DriveContainers
	computeContainers := config.ComputeContainers
	if driveContainers <= 0 || computeContainers <= 0 {
		return nil
	}

	cores := allocator.GetWekaContainerCores(config)
	driveSide := driveContainers * cores.Drive
	computeSide := computeContainers * cores.Compute

	// clusterComputeDriveCoresFloor already owns and reports this case exclusively.
	if computeSide < driveSide {
		return nil
	}

	// Auto-full-drives mode is not reachable here: it requires both counts unset, and both being set
	// is what makes this function run at all.
	exclusiveFullDrives := config.NumDrives > 0 && config.DriveCapacity == 0
	var ratio float64
	if exclusiveFullDrives {
		ratio = globalconfig.Config.CapacityPlanner.FullDrivesComputeToDriveCoreRatio
	} else {
		ratio = globalconfig.Config.CapacityPlanner.ComputeToTlcDriveCoreRatio
	}
	if ratio <= 0 {
		return nil
	}

	required := int(math.Ceil(ratio * float64(driveSide)))
	if computeSide >= required {
		return nil
	}

	n, m := reduceRatio(driveSide, computeSide)
	actualRatio := fmt.Sprintf("%d:%d", n, m)
	detail := fmt.Sprintf(
		"drive:compute core ratio is below the recommended 1:%g (total drive cores: %d, total compute "+
			"cores: %d, actual ratio: %s). Adjust driveContainers/driveCores or computeContainers/computeCores "+
			"to restore a ratio closer to recommended.",
		ratio, driveSide, computeSide, actualRatio,
	)
	return field.ErrorList{
		field.Invalid(
			field.NewPath("spec", "dynamicTemplate"),
			actualRatio,
			detail,
		),
	}
}

// reduceRatio divides both sides by their gcd so the message reads
// `1:2` rather than `6:12`.
func reduceRatio(a, b int) (reducedA, reducedB int) {
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
