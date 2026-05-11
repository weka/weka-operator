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

type WekaContainerCustomValidator struct {
	Client client.Client
}

var _ webhook.CustomValidator = &WekaContainerCustomValidator{}

func RegisterWekaContainerWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&wekav1alpha1.WekaContainer{}).
		WithValidator(&WekaContainerCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

// Webhook config lives in manager.go::buildVWC. Path is GVK-derived by
// controller-runtime and asserted against the constant in manager_test.go.

func (v *WekaContainerCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	if _, ok := obj.(*wekav1alpha1.WekaContainer); !ok {
		return nil, fmt.Errorf("expected a WekaContainer object but got %T", obj)
	}
	return nil, nil
}

// ValidateUpdate short-circuits on unchanged spec — same finalizer-deadlock
// rationale as wekacluster.go's ValidateUpdate.
func (v *WekaContainerCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldC, ok := oldObj.(*wekav1alpha1.WekaContainer)
	if !ok {
		return nil, fmt.Errorf("expected a WekaContainer object but got %T", oldObj)
	}
	newC, ok := newObj.(*wekav1alpha1.WekaContainer)
	if !ok {
		return nil, fmt.Errorf("expected a WekaContainer object but got %T", newObj)
	}
	if reflect.DeepEqual(oldC.Spec, newC.Spec) {
		return nil, nil
	}

	warns, errs := evaluateUpdate(
		ctx, v.Client, oldObj, newObj,
		validation.WekaContainerUpdate, wekaContainerUpdateDefaults,
		config.Config.AdmissionPolicies,
	)
	return warns, errs.ToAggregate()
}

func (v *WekaContainerCustomValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
