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

type ContainerListReconciler struct {
	*ClientReconciler
	Executor Executor
}

func NewContainerListReconciler(c *ClientReconciler, executor Executor) *ContainerListReconciler {
	return &ContainerListReconciler{c, executor}
}

func (r *ContainerListReconciler) Reconcile(ctx context.Context, client *wekav1alpha1.WekaClient) (ctrl.Result, error) {
	r.RecordEvent(v1.EventTypeNormal, "Reconciling", "Reconciling container list")
	stdout, stderr, err := r.Executor.Exec(ctx, []string{"/usr/bin/weka", "cluster", "container", "-J"})
	var podNotFound *PodNotFound
	if errors.As(err, &podNotFound) {
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}
	if err != nil {
		r.Logger.Error(err, "failed to get container list", "stderr", stderr.String())
		return ctrl.Result{}, errors.Wrap(err, "failed to get container list")
	}

	containerList := []wekav1alpha1.Container{}
	json.Unmarshal(stdout.Bytes(), &containerList)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to parse container list")
	}

	err = r.UpdateStatus(ctx, func(status *wekav1alpha1.ClientStatus) {
		status.ContainerList = containerList
	})
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to update client status")
	}

	r.RecordEvent(v1.EventTypeNormal, "Reconciled", "Container list recorded")
	return ctrl.Result{}, nil
}
