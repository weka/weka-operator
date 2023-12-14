// This controller manages Deployment reconciliations.
package controllers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type DeploymentReconciler struct {
	client.Client
}

func NewDeploymentReconciler(c client.Client) *DeploymentReconciler {
	return &DeploymentReconciler{c}
}

func (r *DeploymentReconciler) Reconcile(ctx context.Context, desired *appsv1.Deployment) error {
	key := client.ObjectKeyFromObject(desired)
	existing := &appsv1.Deployment{}
	if err := r.Get(ctx, key, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get deployment %s: %w", key, err)
		}
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create deployment %s: %w", key, err)
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
