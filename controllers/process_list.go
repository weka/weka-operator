package controllers

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pkg/errors"
	wekav1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
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
	// Client generates a key at startup and puts it in a well known location
	// In order to read this file, we need to use Exec to run cat on the container and then read STDOUT
	stdout, stderr, err := r.Executor.Exec(ctx, []string{"/usr/bin/weka", "local", "ps", "-J"})
	var pageNotFound *PodNotFound
	if errors.As(err, &pageNotFound) {
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

	client.Status.ProcessList = processList
	err = r.Status().Update(ctx, client)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to update client status")
	}

	r.RecordEvent(v1.EventTypeNormal, "Reconciled", "Process list recorded")
	return ctrl.Result{}, nil
}

func (r *ProcessListReconciler) recorder(client *wekav1alpha1.Client) *ClientRecorder {
	return NewClientRecorder(client, r.Recorder)
}
