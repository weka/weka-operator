package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// containerCoresDecrease rejects WekaContainer updates that reduce any
// cores field. A decrease is any new < old (including unsetting an
// explicit value back to 0).
type containerCoresDecrease struct{}

func (containerCoresDecrease) ID() string { return "container_cores_decrease" }

func (containerCoresDecrease) ValidateUpdate(_ context.Context, _ client.Client, oldObj, newObj runtime.Object) field.ErrorList {
	oldC, ok := oldObj.(*wekav1alpha1.WekaContainer)
	if !ok {
		return nil
	}
	newC, ok := newObj.(*wekav1alpha1.WekaContainer)
	if !ok {
		return nil
	}

	type check struct {
		path     *field.Path
		old, new int
	}
	checks := []check{
		{field.NewPath("spec", "numCores"), oldC.Spec.NumCores, newC.Spec.NumCores},
		{field.NewPath("spec", "extraCores"), oldC.Spec.ExtraCores, newC.Spec.ExtraCores},
	}

	// DataServicesFeCores: when old config is set, compare against new (0 if new config is nil).
	if oldC.Spec.DataServicesConfig != nil {
		newFe := 0
		if newC.Spec.DataServicesConfig != nil {
			newFe = newC.Spec.DataServicesConfig.DataServicesFeCores
		}
		checks = append(checks, check{
			field.NewPath("spec", "dataServicesConfig", "dataServicesFeCores"),
			oldC.Spec.DataServicesConfig.DataServicesFeCores,
			newFe,
		})
	}

	var errs field.ErrorList
	for _, ch := range checks {
		if ch.new < ch.old {
			errs = append(errs, field.Forbidden(
				ch.path,
				fmt.Sprintf("decreasing %s from %d to %d is not allowed; "+
					"reducing cores can destabilize a running cluster",
					ch.path.String(), ch.old, ch.new),
			))
		}
	}
	return errs
}
