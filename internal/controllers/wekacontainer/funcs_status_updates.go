package wekacontainer

import (
	"context"
	"fmt"
	"slices"
	"time"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func (r *containerReconcilerLoop) IsStatusOverwritableByLocal() bool {
	// we do not want to overwrite this statuses, as they proxy some higher-level state
	if slices.Contains(
		[]weka.ContainerStatus{
			weka.Completed,
			weka.Deleting,
			weka.Destroying,
		},
		r.container.Status.Status,
	) {
		return false
	}
	return true
}

func (r *containerReconcilerLoop) updateContainerStatusIfNotEquals(ctx context.Context, newStatus weka.ContainerStatus) error {
	if r.container.Status.Status != newStatus {
		r.container.Status.Status = newStatus
		err := r.Status().Update(ctx, r.container)
		if err != nil {
			err := fmt.Errorf("failed to update container status: %w", err)
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) updateStatusWaitForDrivers(ctx context.Context) error {
	return r.updateContainerStatusIfNotEquals(ctx, weka.WaitForDrivers)
}

func (r *containerReconcilerLoop) setErrorStatus(ctx context.Context, stepName string, err error) error {
	// ignore "the object has been modified" errors
	if apierrors.IsConflict(err) {
		return nil
	}

	reason := fmt.Sprintf("%sError", stepName)
	r.RecordEventThrottled(v1.EventTypeWarning, reason, err.Error(), time.Minute)

	if !r.IsStatusOverwritableByLocal() {
		return nil
	}

	if r.container.Status.Status == weka.Error || r.container.Status.Status == weka.Unhealthy {
		return nil
	}
	r.container.Status.Status = weka.Error
	return r.Status().Update(ctx, r.container)
}

func (r *containerReconcilerLoop) setDrivesErrorStatus(ctx context.Context, _ string, err error) error {
	r.RecordEvent(v1.EventTypeWarning, "DrivesAddingError", err.Error())

	if r.container.Status.Status == weka.DrivesAdding {
		return nil
	}
	r.container.Status.Status = weka.DrivesAdding
	return r.Status().Update(ctx, r.container)
}

func (r *containerReconcilerLoop) clearStatus(ctx context.Context) error {
	r.container.Status = weka.WekaContainerStatus{}
	return r.Status().Update(ctx, r.container)
}
