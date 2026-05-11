package admission

import (
	"context"
	"fmt"
	"reflect"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/validation"
)

type WekaClusterCustomValidator struct {
	Client client.Client
}

var _ webhook.CustomValidator = &WekaClusterCustomValidator{}

func RegisterWekaClusterWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&wekav1alpha1.WekaCluster{}).
		WithValidator(&WekaClusterCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

// VWC is built in manager.go::buildVWC. No +kubebuilder:webhook marker —
// controller-gen output isn't consumed and only creates drift risk.

func (v *WekaClusterCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil, fmt.Errorf("expected a WekaCluster object but got %T", obj)
	}
	warns, errs := v.run(ctx, cluster)
	return warns, errs.ToAggregate()
}

// ValidateUpdate short-circuits on unchanged spec. Load-bearing: without
// it, the operator's finalizer-removal Update would deadlock delete on a
// CR whose spec already violates an Error policy.
func (v *WekaClusterCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldCluster, ok := oldObj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil, fmt.Errorf("expected a WekaCluster object but got %T", oldObj)
	}
	newCluster, ok := newObj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil, fmt.Errorf("expected a WekaCluster object but got %T", newObj)
	}
	if reflect.DeepEqual(oldCluster.Spec, newCluster.Spec) {
		return nil, nil
	}

	warns, errs := v.run(ctx, newCluster)
	updateWarns, updateErrs := evaluateUpdate(
		ctx, v.Client, oldObj, newObj,
		validation.WekaClusterUpdate, wekaClusterUpdateDefaults,
		config.Config.AdmissionPolicies,
	)
	return append(warns, updateWarns...), append(errs, updateErrs...).ToAggregate()
}

func (v *WekaClusterCustomValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (v *WekaClusterCustomValidator) run(ctx context.Context, cluster *wekav1alpha1.WekaCluster) (admission.Warnings, field.ErrorList) {
	return evaluate(
		ctx,
		v.Client,
		cluster,
		validation.WekaCluster,
		wekaClusterDefaults,
		config.Config.AdmissionPolicies,
	)
}
