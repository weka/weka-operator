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
// field in spec.dynamic. A decrease is any new < old (including unsetting
// an explicit value back to 0).
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
	// When new.Dynamic is nil treat all cores as 0 — removing the block
	// is equivalent to decreasing everything to operator defaults.
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
		if ch.new < ch.old {
			errs = append(errs, field.Forbidden(
				field.NewPath("spec", "dynamic", ch.fieldName),
				fmt.Sprintf("decreasing %s from %d to %d is not allowed; "+
					"reducing cores can destabilize a running cluster",
					ch.fieldName, ch.old, ch.new),
			))
		}
	}

	// Nullable *int: when old is set, compare against new (0 if new is nil).
	if o.DataServicesFeCores != nil {
		newFe := 0
		if n.DataServicesFeCores != nil {
			newFe = *n.DataServicesFeCores
		}
		if newFe < *o.DataServicesFeCores {
			errs = append(errs, field.Forbidden(
				field.NewPath("spec", "dynamic", "dataServicesFeCores"),
				fmt.Sprintf("decreasing dataServicesFeCores from %d to %d is not allowed; "+
					"reducing cores can destabilize a running cluster",
					*o.DataServicesFeCores, newFe),
			))
		}
	}

	return errs
}
