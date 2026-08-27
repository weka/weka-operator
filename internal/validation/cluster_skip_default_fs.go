package validation

import (
	"context"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

// clusterSkipDefaultFs warns when spec.overrides.skipDefaultFilesystemCreation is set while
// a feature that hardcodes the `default` filesystem is still enabled. Skipping is
// a deliberate opt-out, so each unmet dependency is reported separately rather
// than blocking the whole spec.
//
// The flag only stops the operator from *creating* `default`; a user may still
// provision it themselves. Messages therefore describe what the operator stops
// doing, not what the cluster is assumed to contain.
type clusterSkipDefaultFs struct{}

func (clusterSkipDefaultFs) ID() string {
	return "cluster_skip_default_fs"
}

func (clusterSkipDefaultFs) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	skip := cluster.Spec.GetOverrides().SkipDefaultFilesystemCreation
	if skip == nil || !*skip {
		return nil
	}

	path := field.NewPath("spec", "overrides", "skipDefaultFilesystemCreation")
	var errs field.ErrorList

	if configuresTelemetryExports(cluster) {
		errs = append(errs, field.Invalid(path, true,
			"telemetry exports are configured, and the operator applies audit with "+
				"`weka audit cluster enable` followed by `weka audit fs enable default`. "+
				"Cluster-level audit is still enabled and exported, but the filesystem-level "+
				"call is skipped, so audit events for the `default` filesystem will be missing. "+
				"Remove spec.telemetry.exports, do not skip the default filesystem, or enable "+
				"filesystem audit yourself if you provision `default` outside the operator.",
		))
	}

	// CSI is deployed per WekaClient, so this depends on operator config rather
	// than on the WekaCluster spec.
	if globalconfig.Config.Csi.Enabled && !globalconfig.Config.Csi.StorageClassCreationDisabled {
		errs = append(errs, field.Invalid(path, true,
			"the operator creates CSI storage classes pointing at the `default` filesystem, which it "+
				"will not create. Volume provisioning through them fails unless you provision "+
				"`default` yourself. "+
				"Disable CSI (CSI_INSTALLATION_ENABLED=false), or disable storage class creation "+
				"(CSI_STORAGE_CLASS_CREATION_DISABLED=true) and supply your own storage class.",
		))
	}

	return errs
}

// configuresTelemetryExports mirrors the EnsureTelemetry gate: any configured
// export triggers the audit calls. The controller does not look at
// export.Sources, so neither does this check.
func configuresTelemetryExports(cluster *wekav1alpha1.WekaCluster) bool {
	return cluster.Spec.Telemetry != nil && len(cluster.Spec.Telemetry.Exports) > 0
}
