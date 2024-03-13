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

type ContainerListReconciler struct {
	*ClientReconciler
	Executor Executor
}

func NewContainerListReconciler(c *ClientReconciler, executor Executor) *ContainerListReconciler {
	return &ContainerListReconciler{c, executor}
}

func (r *ContainerListReconciler) Reconcile(ctx context.Context, client *wekav1alpha1.Client) (ctrl.Result, error) {
	ctx, span := instrumentation.Tracer.Start(ctx, "reconcile_container_list")
	defer span.End()
	span.AddEvent("Reconsiling container list")
	_ = r.RecordEvent(v1.EventTypeNormal, "Reconciling", "Reconciling container list")
	stdout, stderr, err := r.Executor.Exec(ctx, []string{"/usr/bin/weka", "cluster", "container", "-J"})
	var podNotFound *PodNotFound
	if errors.As(err, &podNotFound) {
		span.SetStatus(codes.Error, "Pod not found")
		span.RecordError(err)
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}
	if err != nil {
		span.SetStatus(codes.Error, "Failed to get container list")
		span.RecordError(err)
		r.Logger.Error(err, "failed to get container list", "stderr", stderr.String())
		return ctrl.Result{}, errors.Wrap(err, "failed to get container list")
	}

	containerList := []wekav1alpha1.Container{}
	err = json.Unmarshal(stdout.Bytes(), &containerList)
	if err != nil {
		span.SetStatus(codes.Error, "Failed to parse container list")
		span.RecordError(err)
		return ctrl.Result{}, errors.Wrap(err, "failed to parse container list")
	}

	err = r.UpdateStatus(ctx, func(status *wekav1alpha1.ClientStatus) {
		status.ContainerList = containerList
	})
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to update client status")
	}

	span.AddEvent("Container list recorded")
	span.SetStatus(codes.Ok, "Container list recorded")
	_ = r.RecordEvent(v1.EventTypeNormal, "Reconciled", "Container list recorded")
	return ctrl.Result{}, nil
}
