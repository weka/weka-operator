// This controller manages Deployment reconciliations.
package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type AgentReconciler struct {
	client.Client
	Logger logr.Logger
}

func NewAgentReconciler(c client.Client, logger logr.Logger) *AgentReconciler {
	return &AgentReconciler{c, logger}
}

func (r *AgentReconciler) Reconcile(ctx context.Context, desired *appsv1.DaemonSet) (ctrl.Result, error) {
	key := client.ObjectKeyFromObject(desired)
	existing := &appsv1.DaemonSet{}
	if err := r.Get(ctx, key, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("failed to get deployment %s: %w", key, err)
		}
		if err := r.Create(ctx, desired); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create deployment %s: %w", key, err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if !r.isAgentAvailable(existing) {
		r.Logger.Info("deployment is not available yet, requeuing")
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

// isAgentAvailable should check the status of the agent, but it does not
// appear that daemonsets support conditions.  Instead, just return true.
func (r *AgentReconciler) isAgentAvailable(deployment *appsv1.DaemonSet) bool {
	numberReady := deployment.Status.NumberReady
	r.Logger.Info("Number of nodes ready", "numberReady", numberReady)
	r.Logger.Info("Number of nodes desired", "desired", deployment.Status.DesiredNumberScheduled)

	return deployment.Status.NumberReady == deployment.Status.DesiredNumberScheduled
}
