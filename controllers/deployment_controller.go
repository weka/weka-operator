// This controller manages Deployment reconciliations.
package controllers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type DeploymentReconciler struct {
	client.Client
}

func NewDeploymentReconciler(c client.Client) *DeploymentReconciler {
	return &DeploymentReconciler{c}
}

func (r *DeploymentReconciler) Reconcile(ctx context.Context, desired *appsv1.Deployment) (ctrl.Result, error) {
	key := client.ObjectKeyFromObject(desired)
	existing := &appsv1.Deployment{}
	if err := r.Get(ctx, key, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("failed to get deployment %s: %w", key, err)
		}
		if err := r.Create(ctx, desired); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create deployment %s: %w", key, err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	patch := client.MergeFrom(existing.DeepCopy())
	existing.Spec = desired.Spec
	for k, v := range desired.Annotations {
		existing.Annotations[k] = v
	}
	for k, v := range desired.Labels {
		existing.Labels[k] = v
	}

	return ctrl.Result{}, r.Patch(ctx, existing, patch)
}
