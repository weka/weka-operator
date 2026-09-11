package validation

import (
	"context"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clientExtraVolumes validates spec.extraVolumes and spec.extraVolumeMounts.
// See validateExtraVolumes for the rule body, shared with clusterExtraVolumes.
type clientExtraVolumes struct{}

func (clientExtraVolumes) ID() string {
	return "client_extra_volumes"
}

func (clientExtraVolumes) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	wc, ok := obj.(*wekav1alpha1.WekaClient)
	if !ok {
		return nil
	}
	return validateExtraVolumes(
		wc.Spec.ExtraVolumes,
		wc.Spec.ExtraVolumeMounts,
		field.NewPath("spec"),
	)
}
