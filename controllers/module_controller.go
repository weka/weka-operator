// This is a separate controller for the Kernel Module sub-resource.
package controllers

import (
	"context"
	"fmt"

	kmmv1beta1 "github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	wekav1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	runtimeClient "sigs.k8s.io/controller-runtime/pkg/client"
)

type ModuleReconciler struct {
	*ClientReconciler
	Desired          *kmmv1beta1.Module
	RootResourceName types.NamespacedName
}

func NewModuleReconciler(c *ClientReconciler, desired *kmmv1beta1.Module, root types.NamespacedName) *ModuleReconciler {
	return &ModuleReconciler{c, desired, root}
}

func (r *ModuleReconciler) Reconcile(ctx context.Context, client *wekav1alpha1.Client) (ctrl.Result, error) {
	if r.Desired == nil {
		return ctrl.Result{}, fmt.Errorf("Desired state is nil, must specify ModuleReconciler.Desired")
	}

	key := runtimeClient.ObjectKeyFromObject(r.Desired)
	existing := &kmmv1beta1.Module{}
	if err := r.Get(ctx, key, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("failed to get module %s: %w", key, err)
		}
		if err := r.Create(ctx, r.Desired); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create module %s: %w", key, err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	patch := runtimeClient.MergeFrom(existing.DeepCopy())
	existing.Spec = r.Desired.Spec
	for k, v := range r.Desired.Annotations {
		existing.Annotations[k] = v
	}
	for k, v := range r.Desired.Labels {
		existing.Labels[k] = v
	}

	return ctrl.Result{}, r.Patch(ctx, existing, patch)
}
