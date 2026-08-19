package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clusterCoresDecrease rejects WekaCluster updates that reduce any cores
// field in spec.dynamicTemplate to a smaller EXPLICIT (positive) value. Unsetting a
// field — new == 0 for plain ints, or nil for nullable *int — means "revert
// to operator-derived sizing" and is ALLOWED (e.g. when migrating from
// containerCapacity to clusterCapacity, where the planner derives cores).
// Only an explicit positive decrease (e.g. 4 -> 2) is blocked, since that can
// destabilize a running cluster.
type clusterCoresDecrease struct{}

func (clusterCoresDecrease) ID() string { return "cluster_cores_decrease" }

func (clusterCoresDecrease) ValidateUpdate(_ context.Context, _ client.Client, oldObj, newObj runtime.Object) field.ErrorList {
	oldCluster, ok := oldObj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	newCluster, ok := newObj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	if oldCluster.Spec.Dynamic == nil {
		return nil // nothing to compare against
	}
	o := oldCluster.Spec.Dynamic
	// When new.Dynamic is nil treat all cores as 0 (unset) — i.e. revert to
	// operator-derived sizing, which is allowed by the new==0 rule below.
	var emptyDynamic wekav1alpha1.WekaClusterTemplate
	n := newCluster.Spec.Dynamic
	if n == nil {
		n = &emptyDynamic
	}

	type check struct {
		fieldName string
		old, new  int
	}
	checks := []check{
		{"computeCores", o.ComputeCores, n.ComputeCores},
		{"driveCores", o.DriveCores, n.DriveCores},
		{"s3Cores", o.S3Cores, n.S3Cores},
		{"envoyCores", o.EnvoyCores, n.EnvoyCores},
		{"nfsCores", o.NfsCores, n.NfsCores},
		{"smbwCores", o.SmbwCores, n.SmbwCores},
		{"dataServicesCores", o.DataServicesCores, n.DataServicesCores},
	}

	var errs field.ErrorList
	for _, ch := range checks {
		// new == 0 means the field is unset (revert to operator-derived
		// sizing) — allowed. Only block an explicit positive decrease.
		if ch.new != 0 && ch.new < ch.old {
			errs = append(errs, field.Forbidden(
				field.NewPath("spec", "dynamicTemplate", ch.fieldName),
				fmt.Sprintf("decreasing %s from %d to %d is not allowed; "+
					"reducing cores can destabilize a running cluster",
					ch.fieldName, ch.old, ch.new),
			))
		}
	}

	// Nullable *int: a nil new value means unset (revert to operator-derived)
	// and is allowed. Only block an explicit smaller value.
	if o.DataServicesFeCores != nil && n.DataServicesFeCores != nil &&
		*n.DataServicesFeCores < *o.DataServicesFeCores {
		errs = append(errs, field.Forbidden(
			field.NewPath("spec", "dynamicTemplate", "dataServicesFeCores"),
			fmt.Sprintf("decreasing dataServicesFeCores from %d to %d is not allowed; "+
				"reducing cores can destabilize a running cluster",
				*o.DataServicesFeCores, *n.DataServicesFeCores),
		))
	}

	return errs
}
