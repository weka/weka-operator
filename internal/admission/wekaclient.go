package admission

import (
	"context"
	"fmt"
	"reflect"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/validation"
)

type WekaClientCustomValidator struct {
	Client client.Client
}

var _ webhook.CustomValidator = &WekaClientCustomValidator{}

func RegisterWekaClientWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&wekav1alpha1.WekaClient{}).
		WithValidator(&WekaClientCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

// Webhook config lives in manager.go::buildVWC. Path is GVK-derived by
// controller-runtime and asserted against the constant in manager.go.

func (v *WekaClientCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	wc, ok := obj.(*wekav1alpha1.WekaClient)
	if !ok {
		return nil, fmt.Errorf("expected a WekaClient object but got %T", obj)
	}
	return v.run(ctx, wc)
}

// ValidateUpdate short-circuits on unchanged spec — same finalizer-deadlock
// rationale as wekacluster.go's ValidateUpdate.
func (v *WekaClientCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldWC, ok := oldObj.(*wekav1alpha1.WekaClient)
	if !ok {
		return nil, fmt.Errorf("expected a WekaClient object but got %T", oldObj)
	}
	newWC, ok := newObj.(*wekav1alpha1.WekaClient)
	if !ok {
		return nil, fmt.Errorf("expected a WekaClient object but got %T", newObj)
	}
	if reflect.DeepEqual(oldWC.Spec, newWC.Spec) {
		return nil, nil
	}
	return v.run(ctx, newWC)
}

func (v *WekaClientCustomValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (v *WekaClientCustomValidator) run(ctx context.Context, wc *wekav1alpha1.WekaClient) (admission.Warnings, error) {
	return evaluate(
		ctx,
		v.Client,
		wc,
		validation.WekaClient,
		wekaClientDefaults,
		config.Config.AdmissionPolicies,
	)
}
