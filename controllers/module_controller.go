// This is a separate controller for the Kernel Module sub-resource.
package controllers

import (
	"context"
	"fmt"

	kmmv1beta1 "github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ModuleReconciler struct {
	client.Client
}

func NewModuleReconciler(c client.Client) *ModuleReconciler {
	return &ModuleReconciler{c}
}

func (r *ModuleReconciler) Reconcile(ctx context.Context, desired *kmmv1beta1.Module) error {
	key := client.ObjectKeyFromObject(desired)
	existing := &kmmv1beta1.Module{}
	if err := r.Get(ctx, key, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get module %s: %w", key, err)
		}
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create module %s: %w", key, err)
		}
		return nil
	}

	patch := client.MergeFrom(existing.DeepCopy())
	existing.Spec = desired.Spec
	for k, v := range desired.Annotations {
		existing.Annotations[k] = v
	}
	for k, v := range desired.Labels {
		existing.Labels[k] = v
	}

	return r.Patch(ctx, existing, patch)
}
