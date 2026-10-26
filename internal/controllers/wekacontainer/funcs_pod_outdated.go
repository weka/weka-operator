package wekacontainer

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/pkg/util"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

var clearPodOutdatedThrottleKey = "clearPodOutdated"

func containerThrottlePartition(name string) string {
	return "container/" + name
}

func podConfigHash() string {
	raw := config.Config.PodConfigVersion
	if config.Config.EnablePodConfigCodeVersionRotation {
		raw += "|" + consts.PodConfigCodeVersion
	}
	return util.GetHash(raw, 12)
}

func setPodConfigHash(pod *v1.Pod) {
	metav1.SetMetaDataAnnotation(&pod.ObjectMeta, consts.PodConfigHashAnnotation, podConfigHash())
}

func (r *containerReconcilerLoop) inRotationScope() bool {
	return r.container.GetParentClusterId() != "" && r.container.IsBackend()
}

func (r *containerReconcilerLoop) detectsPodOutdated() bool {
	return r.container.IsSSDProxyContainer() || r.inRotationScope()
}

func (r *containerReconcilerLoop) podOutdatedDiff(ctx context.Context) ([]string, error) {
	c := r.container.DeepCopy()
	node := c.GetNodeAffinity()
	if node == "" {
		return nil, errors.New("container is not bound to a node")
	}
	liveC, err := resources.GetWekaPodContainer(r.pod)
	if err != nil {
		return nil, err
	}
	if _, err = r.prepareContainerForPod(ctx, c, node, c.Spec.Image); err != nil {
		return nil, err
	}
	nodeInfo, err := r.GetNodeInfo(ctx, node)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get node info")
	}
	if nodeInfo == nil {
		return nil, errors.New("node info is not available")
	}
	expected, err := r.buildPod(ctx, c, nodeInfo, liveC.Image)
	if err != nil {
		return nil, err
	}
	return resources.DiffPods(r.pod, expected, slices.Collect(maps.Keys(c.Spec.AdditionalSecrets)), !resources.FullPcpusOnlyKnown(nodeInfo)), nil
}

func (r *containerReconcilerLoop) podDiffOrLog(ctx context.Context) ([]string, bool) {
	diff, err := r.podOutdatedDiff(ctx)
	if err != nil {
		instrumentation.CurrentSpanLogger(ctx).Error(err, "Failed to compare the pod with the expected pod")
		return nil, false
	}
	return diff, true
}

func (r *containerReconcilerLoop) checkPodOutdated(ctx context.Context) error {
	if _, ok := r.pod.GetAnnotations()[consts.PodConfigHashAnnotation]; !ok {
		if err := r.adoptPodConfigHash(ctx); err != nil {
			return err
		}
	}
	if r.container.Status.PodOutdated {
		return nil
	}
	diff, ok := r.podDiffOrLog(ctx)
	if !ok {
		return nil
	}
	return r.markPodOutdated(ctx, diff)
}

func (r *containerReconcilerLoop) markPodOutdated(ctx context.Context, diff []string) error {
	if len(diff) == 0 {
		return nil
	}
	r.container.Status.PodOutdated = true
	if err := r.Status().Update(ctx, r.container); err != nil {
		return err
	}
	instrumentation.CurrentSpanLogger(ctx).Info("Pod does not match the operator's pod", "diff", diff)
	return r.RecordEvent(v1.EventTypeWarning, "PodOutdated", consts.ActionUpgrade, "pod does not match the operator's pod: "+strings.Join(diff, ", "))
}

// A pod built before the hash annotation existed gets the current hash, so a missing annotation alone never
// flags it.
func (r *containerReconcilerLoop) adoptPodConfigHash(ctx context.Context) error {
	patch := client.MergeFrom(r.pod.DeepCopy())
	setPodConfigHash(r.pod)
	return errors.Wrap(r.Patch(ctx, r.pod, patch), "failed to adopt pod config hash")
}

func (r *containerReconcilerLoop) clearPodOutdated(ctx context.Context) error {
	diff, ok := r.podDiffOrLog(ctx)
	if !ok {
		return nil
	}
	return r.clearOnMatch(ctx, diff)
}

func (r *containerReconcilerLoop) clearOnMatch(ctx context.Context, diff []string) error {
	if len(diff) != 0 {
		return nil
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
	diff, ok := r.podDiffOrLog(ctx)
	if !ok {
		return nil
	}
	return r.rotateOnDiff(ctx, diff)
}

func (r *containerReconcilerLoop) rotateOnDiff(ctx context.Context, diff []string) error {
	if len(diff) == 0 {
		return nil
	}
	if err := r.deletePod(ctx, r.pod); err != nil {
		return err
	}
	r.ThrottlingMap.WithPartition(containerThrottlePartition(r.container.Name)).Reset(clearPodOutdatedThrottleKey)
	return lifecycle.NewWaitError(errors.New("pod deleted to apply: " + strings.Join(diff, ", ")))
}
