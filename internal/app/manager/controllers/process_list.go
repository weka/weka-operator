package controllers

import (
	"context"
	"encoding/json"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"time"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

type ProcessListReconciler struct {
	*ClientReconciler
	Executor Executor
}

func NewProcessListReconciler(c *ClientReconciler, executor Executor) *ProcessListReconciler {
	return &ProcessListReconciler{c, executor}
}

func (r *ProcessListReconciler) Reconcile(ctx context.Context, client *wekav1alpha1.Client) (ctrl.Result, error) {
	r.RecordEvent(v1.EventTypeNormal, "Reconciling", "Reconciling process list")
	stdout, stderr, err := r.Executor.Exec(ctx, []string{"/usr/bin/weka", "local", "ps", "-J"})
	var podNotFound *PodNotFound
	if errors.As(err, &podNotFound) {
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}
	if err != nil {
		r.Logger.Error(err, "failed to get process list", "stderr", stderr.String())
		return ctrl.Result{}, errors.Wrap(err, "failed to get process list")
	}

	processList := []wekav1alpha1.Process{}
	json.Unmarshal(stdout.Bytes(), &processList)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to parse process list")
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
