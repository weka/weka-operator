// This is a separate controller for the Kernel Module sub-resource.
package controllers

import (
	"context"
	"fmt"
	"time"

	kmmv1beta1 "github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	wekav1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
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
	r.RecordEvent(v1.EventTypeNormal, "Reconciling", fmt.Sprintf("Reconciling module %s", r.Desired.Name))

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

	// Assume that if the module reports that the number of available nodes
	// matches the desired number of nodes, then the module is ready.

	availableNodes := existing.Status.ModuleLoader.AvailableNumber
	desiredNodes := existing.Status.ModuleLoader.DesiredNumber
	if availableNodes != desiredNodes {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil // r.Patch(ctx, existing, patch)
	}

	r.RecordEvent(v1.EventTypeNormal, "ModuleReady", fmt.Sprintf("Module is ready: %s", r.Desired.Name))
	return ctrl.Result{}, nil
}
