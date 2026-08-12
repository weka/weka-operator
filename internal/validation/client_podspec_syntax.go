package validation

import (
	"context"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clientPodspecSyntax rejects WekaClients whose scheduling-related fields
// would produce pods the API server rejects at create time (invalid
// toleration keys/enums, label syntax). Pure spec math; see
// podspec_syntax.go for the shared checks.
type clientPodspecSyntax struct{}

func (clientPodspecSyntax) ID() string { return "client_podspec_syntax" }

func (clientPodspecSyntax) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	wc, ok := obj.(*wekav1alpha1.WekaClient)
	if !ok {
		return nil
	}
	spec := field.NewPath("spec")
	var errs field.ErrorList

	errs = append(errs, validateSimpleTolerations(spec.Child("tolerations"), wc.Spec.Tolerations)...)
	errs = append(errs, validateRawTolerations(spec.Child("rawTolerations"), wc.Spec.RawTolerations)...)
	errs = append(errs, validateLabelMap(spec.Child("nodeSelector"), wc.Spec.NodeSelector)...)

	if csi := wc.Spec.CsiConfig; csi != nil && csi.Advanced != nil {
		adv := spec.Child("csiConfig", "advanced")
		errs = append(errs, validateLabelMap(adv.Child("nodeLabels"), csi.Advanced.NodeLabels)...)
		errs = append(errs, validateLabelMap(adv.Child("controllerLabels"), csi.Advanced.ControllerLabels)...)
		errs = append(errs, validateRawTolerations(adv.Child("nodeTolerations"), csi.Advanced.NodeTolerations)...)
		errs = append(errs, validateRawTolerations(adv.Child("controllerTolerations"), csi.Advanced.ControllerTolerations)...)
	}
	return errs
}
