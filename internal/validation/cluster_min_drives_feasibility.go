package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clusterMinDrivesFeasibility rejects WekaClusters whose
// spec.startIoConditions.minNumDrives exceeds driveContainers × numDrives.
// The IO-start condition would never be satisfied — WaitForDrivesAdd()
// polls forever. Skipped when driveContainers or numDrives is 0
// (operator-derived; webhook can't predict the eventual total).
type clusterMinDrivesFeasibility struct{}

func (clusterMinDrivesFeasibility) ID() string {
	return "cluster_min_drives_feasibility"
}

func (clusterMinDrivesFeasibility) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
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
	total := driveContainers * numDrives
	if minNumDrives <= total {
		return nil
	}

	detail := fmt.Sprintf(
		"spec.startIoConditions.minNumDrives (%d) exceeds total drive capacity "+
			"(%d × %d = %d). The cluster will never satisfy the IO-start condition. "+
			"Reduce minNumDrives or increase driveContainers / numDrives.",
		minNumDrives, driveContainers, numDrives, total,
	)
	return field.ErrorList{
		field.Invalid(
			field.NewPath("spec", "startIoConditions", "minNumDrives"),
			minNumDrives,
			detail,
		),
	}
}
