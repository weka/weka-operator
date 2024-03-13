package controllers

import (
	"context"
	"encoding/json"
	"github.com/weka/weka-operator/instrumentation"
	"go.opentelemetry.io/otel/codes"
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
	ctx, span := instrumentation.Tracer.Start(ctx, "reconcile_process_list")
	defer span.End()
	span.AddEvent("Reconsiling process list")
	_ = r.RecordEvent(v1.EventTypeNormal, "Reconciling", "Reconciling process list")
	stdout, stderr, err := r.Executor.Exec(ctx, []string{"/usr/bin/weka", "local", "ps", "-J"})
	var podNotFound *PodNotFound
	if errors.As(err, &podNotFound) {
		span.SetStatus(codes.Error, "Pod not found")
		span.RecordError(err)
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}
	if err != nil {
		span.SetStatus(codes.Error, "Failed to get process list")
		span.RecordError(err)
		r.Logger.Error(err, "failed to get process list", "stderr", stderr.String())
		return ctrl.Result{}, errors.Wrap(err, "failed to get process list")
	}

	processList := []wekav1alpha1.Process{}
	err = json.Unmarshal(stdout.Bytes(), &processList)
	if err != nil {
		span.SetStatus(codes.Error, "Failed to parse process list")
		span.RecordError(err)
		return ctrl.Result{}, errors.Wrap(err, "failed to parse process list")
	}

	err = r.UpdateStatus(ctx, func(status *wekav1alpha1.ClientStatus) {
		status.ProcessList = processList
	})
	if err != nil {
		span.SetStatus(codes.Error, "Failed to update client status")
		span.RecordError(err)
		return ctrl.Result{}, errors.Wrap(err, "failed to update client status")
	}

	// If process list reports that the client is not ready, then requeue
	for _, process := range processList {
		if process.InternalStatus.State != "READY" {
			span.AddEvent("Processes not yet ready")
			_ = r.RecordEvent(v1.EventTypeNormal, "Reconciled", "Processes not yet ready")
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}
	}

	_ = r.RecordEvent(v1.EventTypeNormal, "Reconciled", "Process list recorded")
	span.SetStatus(codes.Ok, "Process list recorded")
	span.AddEvent("Process list recorded")
	return ctrl.Result{}, nil
}

func (r *ProcessListReconciler) recorder(client *wekav1alpha1.Client) *ClientRecorder {
	return NewClientRecorder(client, r.Recorder)
}
