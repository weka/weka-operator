package controllers

import (
	"context"
	"time"

	"github.com/pkg/errors"
	wekav1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
	"github.com/weka/weka-operator/internal/weka_api"
	v1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

type ProcessListReconciler struct {
	*ClientReconciler
}

func NewProcessListReconciler(c *ClientReconciler) *ProcessListReconciler {
	return &ProcessListReconciler{c}
}

func (r *ProcessListReconciler) Reconcile(ctx context.Context, client *wekav1alpha1.Client) (ctrl.Result, error) {
	r.RecordEvent(v1.EventTypeNormal, "Reconciling", "Reconciling process list")

	apiClient, err := weka_api.NewWekaRestApiClient(client.Spec.BackendIP, &r.ApiKey.AccessToken)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "Unable to reconcile process list")
	}

	processList, err := apiClient.GetProcessList(ctx)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "Unable to reconcile process list")
	}
	err = r.UpdateStatus(ctx, func(status *wekav1alpha1.ClientStatus) {
		status.ProcessList = processList
	})
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to update client status")
	}

	// If process list reports that the client is not ready, then requeue
	for _, process := range processList {
		if process.InternalStatus.State != "READY" {
			r.RecordEvent(v1.EventTypeNormal, "Reconciled", "Processes not yet ready")
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}
	}

	r.RecordEvent(v1.EventTypeNormal, "Reconciled", "Process list recorded")
	return ctrl.Result{}, nil
}

func (r *ProcessListReconciler) recorder(client *wekav1alpha1.Client) *ClientRecorder {
	return NewClientRecorder(client, r.Recorder)
}
