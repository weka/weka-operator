package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// clusterNumDrivesBelowRequiredCores rejects a numDrives+driveCapacity template whose configured
// capacity needs more drive cores than numDrives allows. In this mode each drive core needs at least
// one virtual drive, so CEL (wekacluster_types.go) caps driveCores at numDrives — meaning a per-drive
// driveCapacity above the per-core TLC capacity is unreachable at EVERY legal driveCores, not just at
// the one the operator picks.
//
// Nothing else catches it. getDriveCores clamps the derived count to numDrives, so the container is
// built with too few cores for the capacity it is told to hold and drive adds are deferred with
// DriveCapacityResourceShortfall forever; clusterDriveCoresBelowCapacity only fires on an explicit
// driveCores and compares against that same clamped figure, so it stays silent here.
//
// The check reduces exactly to driveCapacity <= TlcCapacityPerCoreGiB (required = ceil(numDrives ×
// driveCapacity / perCore) is <= numDrives iff driveCapacity <= perCore), which is why raising
// numDrives is NOT offered as a remedy: it scales capacity and requirement together and never closes
// the gap.
type clusterNumDrivesBelowRequiredCores struct{}

func (clusterNumDrivesBelowRequiredCores) ID() string {
	return "cluster_num_drives_below_required_cores"
}

func (clusterNumDrivesBelowRequiredCores) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	config := cluster.Spec.Dynamic
	if config == nil {
		return nil
	}
	// Planner-managed templates size their own cores, so the numDrives ceiling this rule reasons about
	// does not apply. CEL already makes clusterCapacity mutually exclusive with numDrives/driveCapacity,
	// so this is unreachable through the API — but the guard keeps the rule honest for callers that
	// evaluate a template directly, and matches how every other core-sizing rule scopes itself.
	if allocator.IsPlannerManaged(config) {
		return nil
	}
	// The only remaining mode that pins a drive count and a per-drive capacity independently.
	// containerCapacity sets a whole-container figure with no drive count to bound the cores.
	if config.NumDrives <= 0 || config.DriveCapacity <= 0 {
		return nil
	}

	required, ok := allocator.RequiredDriveCoresForTemplate(config)
	if !ok || required <= config.NumDrives {
		return nil
	}

	perCoreGiB := globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB
	detail := fmt.Sprintf(
		"spec.dynamicTemplate.driveCapacity (%d GiB per drive × numDrives %d = %d GiB) needs %d drive "+
			"core(s), but numDrives caps driveCores at %d — weka requires at least one drive per drive "+
			"core in this mode, so the capacity is unreachable at every legal driveCores and drive adds "+
			"would be deferred with DriveCapacityResourceShortfall indefinitely. Raising numDrives does "+
			"not help: it raises total capacity by the same factor. Lower driveCapacity to at most %d "+
			"GiB (the per-core TLC capacity), switch to spec.dynamicTemplate.containerCapacity so the "+
			"operator sizes cores from the total instead of per drive, or raise the "+
			"CLUSTER_CAPACITY_TLC_CAPACITY_PER_CORE_GIB Helm value (currently %d).",
		config.DriveCapacity, config.NumDrives, config.DriveCapacity*config.NumDrives,
		required, config.NumDrives, perCoreGiB, perCoreGiB,
	)
	return field.ErrorList{
		field.Invalid(
			field.NewPath("spec", "dynamicTemplate", "driveCapacity"),
			config.DriveCapacity,
			detail,
		),
	}
}
