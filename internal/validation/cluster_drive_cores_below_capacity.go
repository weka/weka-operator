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

// clusterDriveCoresBelowCapacity warns when an explicit driveCores is below what the configured drive
// capacity (containerCapacity, or numDrives+driveCapacity) requires — getDriveCores no longer clamps it
// up, so this surfaces the shortfall at admission instead of failing later via
// DriveCapacityResourceShortfall on add-drive. Warn-only.
//
// Silent in auto-full-drives mode by construction: DerivedDriveCores has no capacity basis there (no
// containerCapacity, and numDrives without driveCapacity is a drive COUNT, not capacity), so it
// returns ok=false and this returns nil. That is deliberate — {numDrives: 4, driveCores: 3} is a
// blessed configuration in that mode (all four drives claimed, run on three cores), not a shortfall.
// See clusterAutoFullDrivesPinExceedsNodeDrives for the pins that ARE checked there.
type clusterDriveCoresBelowCapacity struct{}

func (clusterDriveCoresBelowCapacity) ID() string {
	return "cluster_drive_cores_below_capacity"
}

func (clusterDriveCoresBelowCapacity) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	if cluster.Spec.Dynamic == nil {
		return nil
	}

	config := cluster.Spec.Dynamic
	if config.DriveCores <= 0 {
		return nil
	}

	derived, ok := allocator.DerivedDriveCores(config)
	if !ok {
		return nil
	}
	if config.DriveCores >= derived {
		return nil
	}

	detail := fmt.Sprintf(
		"spec.dynamicTemplate.driveCores (%d) is below the %d core(s) that the configured drive "+
			"capacity requires. The operator will honor the explicit value as set, but drive adds "+
			"will be deferred with DriveCapacityResourceShortfall until driveCores is raised to at "+
			"least %d or the configured capacity is reduced.",
		config.DriveCores, derived, derived,
	)
	return field.ErrorList{
		field.Invalid(
			field.NewPath("spec", "dynamicTemplate", "driveCores"),
			config.DriveCores,
			detail,
		),
	}
}
