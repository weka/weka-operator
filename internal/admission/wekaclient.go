package admission

import (
	"context"
	"reflect"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/validation"
)

type WekaClientCustomValidator struct {
	Client client.Client
}

var _ admission.Validator[*wekav1alpha1.WekaClient] = &WekaClientCustomValidator{}

func RegisterWekaClientWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &wekav1alpha1.WekaClient{}).
		WithValidator(&WekaClientCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

// Webhook config lives in manager.go::buildVWC. Path is GVK-derived by
// controller-runtime and asserted against the constant in manager.go.

func (v *WekaClientCustomValidator) ValidateCreate(ctx context.Context, wc *wekav1alpha1.WekaClient) (admission.Warnings, error) {
	warns, errs := v.run(ctx, wc)
	return warns, errs.ToAggregate()
}

// ValidateUpdate short-circuits on unchanged spec — same finalizer-deadlock
// rationale as wekacluster.go's ValidateUpdate.
func (v *WekaClientCustomValidator) ValidateUpdate(ctx context.Context, oldWC, newWC *wekav1alpha1.WekaClient) (admission.Warnings, error) {
	if reflect.DeepEqual(oldWC.Spec, newWC.Spec) {
		return nil, nil
	}

	warns, errs := v.run(ctx, newWC)
	updateWarns, updateErrs := evaluateUpdate(
		ctx, v.Client, oldWC, newWC,
		validation.WekaClientUpdate, wekaClientUpdateDefaults,
		config.Config.AdmissionPolicies,
	)
	return append(warns, updateWarns...), append(errs, updateErrs...).ToAggregate()
}

func (v *WekaClientCustomValidator) ValidateDelete(_ context.Context, _ *wekav1alpha1.WekaClient) (admission.Warnings, error) {
	return nil, nil
}

func (v *WekaClientCustomValidator) run(ctx context.Context, wc *wekav1alpha1.WekaClient) (admission.Warnings, field.ErrorList) {
	return evaluate(
		ctx,
		v.Client,
		wc,
		validation.WekaClient,
		wekaClientDefaults,
		config.Config.AdmissionPolicies,
	)
}
