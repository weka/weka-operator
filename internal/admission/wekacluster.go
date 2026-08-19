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

type WekaClusterCustomValidator struct {
	Client client.Client
}

var _ admission.Validator[*wekav1alpha1.WekaCluster] = &WekaClusterCustomValidator{}

func RegisterWekaClusterWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &wekav1alpha1.WekaCluster{}).
		WithValidator(&WekaClusterCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

// VWC is built in manager.go::buildVWC. No +kubebuilder:webhook marker —
// controller-gen output isn't consumed and only creates drift risk.

func (v *WekaClusterCustomValidator) ValidateCreate(ctx context.Context, cluster *wekav1alpha1.WekaCluster) (admission.Warnings, error) {
	warns, errs := v.run(ctx, cluster)
	return warns, errs.ToAggregate()
}

// ValidateUpdate short-circuits on unchanged spec. Load-bearing: without
// it, the operator's finalizer-removal Update would deadlock delete on a
// CR whose spec already violates an Error policy.
func (v *WekaClusterCustomValidator) ValidateUpdate(ctx context.Context, oldCluster, newCluster *wekav1alpha1.WekaCluster) (admission.Warnings, error) {
	if reflect.DeepEqual(oldCluster.Spec, newCluster.Spec) {
		return nil, nil
	}

	warns, errs := v.run(ctx, newCluster)
	updateWarns, updateErrs := evaluateUpdate(
		ctx, v.Client, oldCluster, newCluster,
		validation.WekaClusterUpdate, wekaClusterUpdateDefaults,
		config.Config.AdmissionPolicies,
	)
	return append(warns, updateWarns...), append(errs, updateErrs...).ToAggregate()
}

func (v *WekaClusterCustomValidator) ValidateDelete(_ context.Context, _ *wekav1alpha1.WekaCluster) (admission.Warnings, error) {
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
