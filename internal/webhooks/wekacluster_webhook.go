package webhooks

import (
	"context"
	"fmt"

	"github.com/weka/go-weka-observability/instrumentation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// WekaClusterCustomValidator implements the CustomValidator interface for WekaCluster.
type WekaClusterCustomValidator struct{}

var _ webhook.CustomValidator = &WekaClusterCustomValidator{}

// SetupWekaClusterWebhookWithManager registers the webhook with the manager.
func SetupWekaClusterWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&wekav1alpha1.WekaCluster{}).
		WithValidator(&WekaClusterCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-weka-weka-io-v1alpha1-wekacluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=weka.weka.io,resources=wekaclusters,verbs=create;update,versions=v1alpha1,name=vwekacluster.weka.io,admissionReviewVersions=v1

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type WekaCluster.
func (v *WekaClusterCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	wekacluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil, fmt.Errorf("expected a WekaCluster object but got %T", obj)
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "WekaClusterWebhook.ValidateCreate",
		"name", wekacluster.GetName(), "namespace", wekacluster.GetNamespace())
	defer end()

	logger.Info("validating create")

	return validateWekaCluster(wekacluster)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type WekaCluster.
func (v *WekaClusterCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	wekacluster, ok := newObj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil, fmt.Errorf("expected a WekaCluster object but got %T", newObj)
	}

	_, logger, end := instrumentation.GetLogSpan(ctx, "WekaClusterWebhook.ValidateUpdate",
		"name", wekacluster.GetName(), "namespace", wekacluster.GetNamespace())
	defer end()

	logger.Info("validating update")

	return validateWekaCluster(wekacluster)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type WekaCluster.
func (v *WekaClusterCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	// No validation needed for delete operations
	return nil, nil
}

// validateWekaCluster performs validation on WekaCluster objects.
func validateWekaCluster(wekacluster *wekav1alpha1.WekaCluster) (admission.Warnings, error) {
	var warnings admission.Warnings
	fldPath := field.NewPath("spec").Child("dynamicTemplate")

	// Validate basic drive configuration (CEL-equivalent rules)
	if errs := validateDriveConfiguration(wekacluster.Spec.Dynamic, fldPath); len(errs) > 0 {
		return warnings, fmt.Errorf("validation failed: %s", errs.ToAggregate().Error())
	}

	// Validate advanced capacity/ratio constraints
	if errs := validateCapacityWithDriveTypesRatio(wekacluster.Spec.Dynamic, fldPath); len(errs) > 0 {
		return warnings, fmt.Errorf("validation failed: %s", errs.ToAggregate().Error())
	}

	return warnings, nil
}

// validateDriveConfiguration validates the basic drive configuration fields in WekaConfig.
// This function implements the same validation rules as the CEL validation on the WekaConfig struct.
func validateDriveConfiguration(cfg *wekav1alpha1.WekaConfig, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if cfg == nil {
		return allErrs
	}

	// Rule 1: numDrives and containerCapacity are mutually exclusive
	if cfg.NumDrives > 0 && cfg.ContainerCapacity > 0 {
		allErrs = append(allErrs, field.Invalid(
			fldPath.Child("numDrives"),
			cfg.NumDrives,
			"numDrives and containerCapacity are mutually exclusive; use numDrives with driveCapacity for TLC-only mode, or containerCapacity with driveTypesRatio for mixed drive types",
		))
	}

	// Rule 2: driveCapacity and driveTypesRatio are mutually exclusive
	if cfg.DriveCapacity > 0 && cfg.DriveTypesRatio != nil {
		allErrs = append(allErrs, field.Invalid(
			fldPath.Child("driveCapacity"),
			cfg.DriveCapacity,
			"driveCapacity and driveTypesRatio are mutually exclusive; use driveCapacity for TLC-only mode, or containerCapacity with driveTypesRatio for mixed drive types",
		))
	}

	// Rule 3: numDrives >= driveCores when using driveCapacity (TLC-only mode)
	if cfg.DriveCapacity > 0 && cfg.NumDrives > 0 && cfg.NumDrives < cfg.DriveCores {
		allErrs = append(allErrs, field.Invalid(
			fldPath.Child("numDrives"),
			cfg.NumDrives,
			"numDrives must be >= driveCores when using driveCapacity (TLC-only mode); each drive core requires at least one virtual drive",
		))
	}

	// Rule 4: containerCapacity must be > 0 when specified
	if cfg.ContainerCapacity < 0 {
		allErrs = append(allErrs, field.Invalid(
			fldPath.Child("containerCapacity"),
			cfg.ContainerCapacity,
			"containerCapacity must be greater than 0 when specified",
		))
	}

	// Rule 5: driveCapacity must be > 0 when specified
	if cfg.DriveCapacity < 0 {
		allErrs = append(allErrs, field.Invalid(
			fldPath.Child("driveCapacity"),
			cfg.DriveCapacity,
			"driveCapacity must be greater than 0 when specified",
		))
	}

	// Rule 6: driveTypesRatio requires at least one non-zero value (tlc or qlc)
	if cfg.DriveTypesRatio != nil {
		if cfg.DriveTypesRatio.Tlc <= 0 && cfg.DriveTypesRatio.Qlc <= 0 {
			allErrs = append(allErrs, field.Invalid(
				fldPath.Child("driveTypesRatio"),
				cfg.DriveTypesRatio,
				"at least one of driveTypesRatio.tlc or driveTypesRatio.qlc must be greater than 0",
			))
		}
	}

	return allErrs
}

// validateCapacityWithDriveTypesRatio validates that containerCapacity with driveTypesRatio
// provides sufficient capacity for each drive type when enforceMinDrivesPerTypePerCore is enabled.
//
// When enforceMinDrivesPerTypePerCore is true:
// - Each active drive type (TLC/QLC with ratio > 0) needs at least driveCores virtual drives
// - Each virtual drive needs at least MinChunkSizeGiB (384 GiB)
// - Therefore, minimum capacity per active type = driveCores * MinChunkSizeGiB
func validateCapacityWithDriveTypesRatio(cfg *wekav1alpha1.WekaConfig, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if cfg == nil {
		return allErrs
	}

	// This validation only applies when using containerCapacity with driveTypesRatio
	if cfg.ContainerCapacity <= 0 || cfg.DriveTypesRatio == nil {
		return allErrs
	}

	// Skip if enforceMinDrivesPerTypePerCore is disabled (read from global config)
	if !config.Config.DriveSharing.EnforceMinDrivesPerTypePerCore {
		return allErrs
	}

	// Use driveCores from config, defaulting to 1 if not set (matches operator defaulting logic)
	driveCores := cfg.DriveCores
	if driveCores <= 0 {
		driveCores = 1
	}

	tlcPart := cfg.DriveTypesRatio.Tlc
	qlcPart := cfg.DriveTypesRatio.Qlc
	totalParts := tlcPart + qlcPart

	if totalParts <= 0 {
		// Already validated by basic rules
		return allErrs
	}

	// Calculate capacity split based on ratio
	tlcCapacity, qlcCapacity := wekav1alpha1.GetTlcQlcCapacity(
		cfg.ContainerCapacity,
		cfg.DriveTypesRatio,
	)

	// Minimum capacity per type when enforceMinDrivesPerTypePerCore=true: driveCores * MinChunkSizeGiB
	minCapacityWhenPerTypePerCoreEnforced := driveCores * allocator.MinChunkSizeGiB

	// Minimum containerCapacity to satisfy both types = totalParts * minCapacityWhenPerTypePerCoreEnforced
	minContainerCapacity := totalParts * minCapacityWhenPerTypePerCoreEnforced

	// Validate TLC capacity if TLC is active (part > 0)
	if tlcPart > 0 && tlcCapacity < minCapacityWhenPerTypePerCoreEnforced {
		allErrs = append(allErrs, field.Invalid(
			fldPath.Child("containerCapacity"),
			cfg.ContainerCapacity,
			fmt.Sprintf(
				"insufficient TLC capacity: with %d drive cores and enforceMinDrivesPerTypePerCore=true, "+
					"TLC needs at least %d GiB (minimum %d drives × %d GiB each), but ratio %d:%d allocates only %d GiB to TLC. "+
					"Either increase containerCapacity to at least %d GiB, adjust driveTypesRatio to allocate more to TLC, "+
					"or set enforceMinDrivesPerTypePerCore=false in Helm values",
				driveCores,
				minCapacityWhenPerTypePerCoreEnforced,
				driveCores,
				allocator.MinChunkSizeGiB,
				tlcPart, qlcPart,
				tlcCapacity,
				minContainerCapacity,
			),
		))
	}

	// Validate QLC capacity if QLC is active (part > 0)
	if qlcPart > 0 && qlcCapacity < minCapacityWhenPerTypePerCoreEnforced {
		allErrs = append(allErrs, field.Invalid(
			fldPath.Child("containerCapacity"),
			cfg.ContainerCapacity,
			fmt.Sprintf(
				"insufficient QLC capacity: with %d drive cores and enforceMinDrivesPerTypePerCore=true, "+
					"QLC needs at least %d GiB (minimum %d drives × %d GiB each), but ratio %d:%d allocates only %d GiB to QLC. "+
					"Either increase containerCapacity to at least %d GiB, adjust driveTypesRatio to allocate more to QLC, "+
					"or set enforceMinDrivesPerTypePerCore=false in Helm values",
				driveCores,
				minCapacityWhenPerTypePerCoreEnforced,
				driveCores,
				allocator.MinChunkSizeGiB,
				tlcPart, qlcPart,
				qlcCapacity,
				minContainerCapacity,
			),
		))
	}

	return allErrs
}
