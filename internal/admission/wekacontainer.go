package admission

import (
	"context"
	"reflect"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/validation"
)

type WekaContainerCustomValidator struct {
	Client client.Client
}

var _ admission.Validator[*wekav1alpha1.WekaContainer] = &WekaContainerCustomValidator{}

func RegisterWekaContainerWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &wekav1alpha1.WekaContainer{}).
		WithValidator(&WekaContainerCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

// Webhook config lives in manager.go::buildVWC. Path is GVK-derived by
// controller-runtime and asserted against the constant in manager_test.go.

func (v *WekaContainerCustomValidator) ValidateCreate(_ context.Context, _ *wekav1alpha1.WekaContainer) (admission.Warnings, error) {
	return nil, nil
}

// ValidateUpdate short-circuits on unchanged spec — same finalizer-deadlock
// rationale as wekacluster.go's ValidateUpdate.
func (v *WekaContainerCustomValidator) ValidateUpdate(ctx context.Context, oldC, newC *wekav1alpha1.WekaContainer) (admission.Warnings, error) {
	if reflect.DeepEqual(oldC.Spec, newC.Spec) {
		return nil, nil
	}

	warns, errs := evaluateUpdate(
		ctx, v.Client, oldC, newC,
		validation.WekaContainerUpdate, wekaContainerUpdateDefaults,
		config.Config.AdmissionPolicies,
	)
	return warns, errs.ToAggregate()
}

func (v *WekaContainerCustomValidator) ValidateDelete(_ context.Context, _ *wekav1alpha1.WekaContainer) (admission.Warnings, error) {
	return nil, nil
}
