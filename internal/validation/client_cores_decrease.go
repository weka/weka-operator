package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clientCoresDecrease rejects WekaClient updates that reduce spec.coresNum.
// A decrease is any new < old (including unsetting an explicit value back to 0).
type clientCoresDecrease struct{}

func (clientCoresDecrease) ID() string { return "client_cores_decrease" }

func (clientCoresDecrease) ValidateUpdate(_ context.Context, _ client.Client, oldObj, newObj runtime.Object) field.ErrorList {
	oldWC, ok := oldObj.(*wekav1alpha1.WekaClient)
	if !ok {
		return nil
	}
	newWC, ok := newObj.(*wekav1alpha1.WekaClient)
	if !ok {
		return nil
	}

	if newWC.Spec.CoresNumber < oldWC.Spec.CoresNumber {
		return field.ErrorList{
			field.Forbidden(
				field.NewPath("spec", "coresNum"),
				fmt.Sprintf("decreasing coresNum from %d to %d is not allowed",
					oldWC.Spec.CoresNumber, newWC.Spec.CoresNumber),
			),
		}
	}
	return nil
}
