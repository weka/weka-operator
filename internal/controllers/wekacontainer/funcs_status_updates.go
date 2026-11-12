package wekacontainer

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/pkg/util/channelerr"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// lastLocalPsRead maps a container UID to the time.Time of its last successful weka local ps. An operator restart
// only delays Unknown: the first channel failure seeds a missing entry.
var lastLocalPsRead sync.Map

// wekaContainerRunning reads the pod's container status, not its phase: the phase stays Running during
// CrashLoopBackOff.
func wekaContainerRunning(pod *v1.Pod) bool {
	for i := range pod.Status.ContainerStatuses {
		if s := &pod.Status.ContainerStatuses[i]; s.Name == consts.WekaContainerName {
			return s.State.Running != nil
		}
	}
	return false
}

// isChannelFailure excludes a pod whose weka container is not running: client-go reports "container not found"
// on exec with the same plain-string shape as an API server 500, but there the channel worked and the target is
// gone. Without a pod nothing was exec'd, so a channel error there (e.g. a 500 on pod create) is the API server's.
func (r *containerReconcilerLoop) isChannelFailure(err error) bool {
	return channelerr.Is(err) && (r.pod == nil || wekaContainerRunning(r.pod))
}

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
			wrapErr := fmt.Errorf("failed to update container status: %w", err)
			return wrapErr
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

	if r.isChannelFailure(err) {
		if !r.container.IsWekaContainer() {
			return nil
		}
		v, _ := lastLocalPsRead.LoadOrStore(r.container.GetUID(), time.Now())
		since := v.(time.Time)
		timeout := services.GetSettings(ctx).ContainerHealth.UnreachableTimeout
		if time.Since(since) < timeout || !r.IsStatusOverwritableByLocal() || r.container.Status.Status == runStatusUnknown {
			return nil
		}
		if statusErr := r.updateContainerStatusIfNotEquals(ctx, runStatusUnknown); statusErr != nil {
			return statusErr
		}
		msg := fmt.Sprintf("operator cannot reach the container over the API server/exec channel for longer than %s since the last successful weka local ps (step %s): %v", timeout, stepName, err)
		_ = r.RecordEvent(v1.EventTypeWarning, "ContainerUnreachable", consts.ActionMonitorContainerHealth, msg) //nolint:errcheck // error return value intentionally not checked
		return nil
	}

	reason := fmt.Sprintf("%sError", stepName)
	_ = r.RecordEventThrottled(v1.EventTypeWarning, reason, consts.ActionReconcile, err.Error(), time.Minute) //nolint:errcheck // error return value intentionally not checked

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
	_ = r.RecordEvent(v1.EventTypeWarning, "DrivesAddingError", consts.ActionManageDrives, err.Error()) //nolint:errcheck // error return value intentionally not checked

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
