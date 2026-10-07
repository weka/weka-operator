package wekacontainer

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func (r *containerReconcilerLoop) inRotationScope() bool {
	return r.container.GetParentClusterId() != "" && r.container.IsBackend()
}

func (r *containerReconcilerLoop) detectsPodOutdated() bool {
	return r.container.IsSSDProxyContainer() || r.inRotationScope()
}

func (r *containerReconcilerLoop) podSnapshotDiff() (diff []string, stamped bool, err error) {
	stamp, ok := r.pod.GetAnnotations()[consts.PodSpecAnnotation]
	if !ok {
		return nil, false, nil
	}
	diff, err = diffPodSpecSnapshot(stamp, snapshotPodSpec(r.container), config.Config.EnablePodConfigCodeVersionRotation)
	return diff, true, err
}

func (r *containerReconcilerLoop) checkPodOutdated(ctx context.Context) error {
	if _, ok := r.pod.GetAnnotations()[consts.PodSpecAnnotation]; !ok {
		return r.adoptPodSnapshot(ctx)
	}
	if r.container.Status.PodOutdated || !r.detectsPodOutdated() {
		return nil
	}
	diff, _, err := r.podSnapshotDiff()
	if err != nil || len(diff) == 0 {
		return err
	}
	r.container.Status.PodOutdated = true
	if err := r.Status().Update(ctx, r.container); err != nil {
		return err
	}
	return r.RecordEvent(v1.EventTypeWarning, "PodOutdated", consts.ActionUpgrade, "pod does not match the spec: "+strings.Join(diff, ", "))
}

// Pods created before the stamp existed are trusted to match the spec: stamping them with the
// current values is what keeps an operator upgrade from flagging every pod at once.
func (r *containerReconcilerLoop) adoptPodSnapshot(ctx context.Context) error {
	snapshotJSON, err := json.Marshal(snapshotPodSpec(r.container))
	if err != nil {
		return errors.Wrap(err, "failed to marshal pod spec snapshot")
	}
	patch := client.MergeFrom(r.pod.DeepCopy())
	if r.pod.Annotations == nil {
		r.pod.Annotations = map[string]string{}
	}
	r.pod.Annotations[consts.PodSpecAnnotation] = string(snapshotJSON)
	return errors.Wrap(r.Patch(ctx, r.pod, patch), "failed to adopt pod spec snapshot")
}

func (r *containerReconcilerLoop) clearPodOutdated(ctx context.Context) error {
	diff, stamped, err := r.podSnapshotDiff()
	if err != nil || !stamped || len(diff) != 0 {
		return err
	}
	if err := r.waitPodReady(ctx); err != nil {
		return err
	}
	delete(r.container.Status.Timestamps, string(weka.TimestampIoProcessesUp))
	r.container.Status.PodOutdated = false
	return r.Status().Update(ctx, r.container)
}

func (r *containerReconcilerLoop) podRotationApproved() bool {
	return r.container.Spec.RotatePod && r.container.Status.PodOutdated && r.inRotationScope()
}

func (r *containerReconcilerLoop) rotateOutdatedPod(ctx context.Context) error {
	diff, stamped, err := r.podSnapshotDiff()
	if err != nil || !stamped || len(diff) == 0 {
		return err
	}
	if err := r.deletePod(ctx, r.pod); err != nil {
		return err
	}
	return lifecycle.NewWaitError(errors.New("pod deleted to apply: " + strings.Join(diff, ", ")))
}
