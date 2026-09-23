package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/services/discovery"
	util2 "github.com/weka/weka-operator/pkg/util"
)

const (
	// Bounds how long we wait for a weka-kernelize-* container with no result (image pull failure,
	// unschedulable node, etc). Without it the proxy pod would be blocked forever, since
	// deleteStuckAdhocContainer would just recreate the same stuck container.
	kernelizeStaleTimeout = 5 * time.Minute
	// Set once the result has been handled, so passes that retry proxy pod creation reuse it
	// instead of running kernelize again.
	annotationKernelizeProcessedAt = "weka.io/kernelize-processed-at"
	// Set once the proxy pod exists: the result then predates any later pod and must not be reused.
	annotationKernelizeConsumedAt = "weka.io/kernelize-consumed-at"
	// How long a consumed container is kept for inspection. Deleting it well after the proxy pod
	// was created also keeps its pod teardown from racing the proxy pod's sandbox creation.
	kernelizeRetention = 5 * time.Minute
)

// KernelizeResult mirrors the JSON written by weka_runtime.py's kernelize_drives(). Raw is the
// tool's -J output, kept for debugging only.
type KernelizeResult struct {
	Err              string          `json:"err,omitempty"`
	Candidates       int             `json:"candidates"`
	FilteredOutInUse int             `json:"filtered_out_in_use"`
	Recovered        int             `json:"recovered"`
	Failed           int             `json:"failed"`
	Raw              json.RawMessage `json:"raw,omitempty"`
}

// KernelizeOperation runs `weka-sign-drive kernelize` on the proxy's node in a short-lived,
// hostPID ad-hoc WekaContainer, before the proxy pod itself is created. See funcs_proxy.go's
// runKernelizeBeforeProxyPod for the trigger.
type KernelizeOperation struct {
	client         client.Client
	scheme         *runtime.Scheme
	recorder       events.EventRecorder
	owner          *weka.WekaContainer // the ssdproxy container
	nodeName       weka.NodeName
	image          string
	pullSecret     string
	serviceAccount string
	tolerations    []corev1.Toleration
	container      *weka.WekaContainer
	result         KernelizeResult
}

func NewKernelizeOperation(mgr ctrl.Manager, recorder events.EventRecorder, owner *weka.WekaContainer, nodeName weka.NodeName) *KernelizeOperation {
	details := owner.ToOwnerDetails()
	return &KernelizeOperation{
		client:         mgr.GetClient(),
		scheme:         mgr.GetScheme(),
		recorder:       recorder,
		owner:          owner,
		nodeName:       nodeName,
		image:          config.Config.SignDrivesImage,
		pullSecret:     details.ImagePullSecret,
		serviceAccount: details.ServiceAccountName,
		tolerations:    details.Tolerations,
	}
}

func (o *KernelizeOperation) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "Kernelize",
		Run:  AsRunFunc(o),
	}
}

func (o *KernelizeOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{Name: "GetContainer", Run: o.GetContainer},
		&lifecycle.SimpleStep{Name: "EnsureContainer", Run: o.EnsureContainer, Predicates: lifecycle.Predicates{lifecycle.IsNotFunc(o.HasContainer)}},
		&lifecycle.SimpleStep{Name: "PollResults", Run: o.PollResults},
		&lifecycle.SimpleStep{Name: "ProcessResult", Run: o.ProcessResult, Predicates: lifecycle.Predicates{o.HasContainer, lifecycle.IsNotFunc(o.isProcessed)}},
	}
}

func kernelizeContainerName(nodeName weka.NodeName) string {
	return fmt.Sprintf("weka-kernelize-%s", nodeName)
}

func (o *KernelizeOperation) isProcessed() bool {
	_, ok := o.container.GetAnnotations()[annotationKernelizeProcessedAt]
	return ok
}

func (o *KernelizeOperation) GetContainer(ctx context.Context) error {
	ref := weka.ObjectReference{Name: kernelizeContainerName(o.nodeName), Namespace: o.owner.Namespace}
	existing, err := discovery.GetContainerByName(ctx, o.client, ref)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// The name is per node, so a container left by a previous proxy on this node (not yet
	// garbage-collected) must be gone before we run: its result predates the current proxy.
	if existing.DeletionTimestamp != nil {
		return lifecycle.NewWaitErrorWithDuration(errors.New("previous kernelize container is still terminating"), 5*time.Second)
	}
	if _, consumed := existing.GetAnnotations()[annotationKernelizeConsumedAt]; consumed || !metav1.IsControlledBy(existing, o.owner) {
		if err := o.client.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return lifecycle.NewWaitErrorWithDuration(errors.New("deleted kernelize container of a previous proxy or proxy pod"), 5*time.Second)
	}

	o.container = existing
	return nil
}

func (o *KernelizeOperation) HasContainer() bool { return o.container != nil }

func (o *KernelizeOperation) EnsureContainer(ctx context.Context) error {
	if o.image == "" {
		err := fmt.Errorf("SIGN_DRIVES_IMAGE is not configured, cannot run kernelize")
		util2.RecordEvent(o.recorder, o.owner, corev1.EventTypeWarning, "KernelizeFailed", consts.ActionKernelize, err.Error())
		return err
	}

	labels := util2.MergeMaps(o.owner.GetLabels(), factory.RequiredAnyWekaContainerLabels(weka.WekaContainerModeAdhocOp))

	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kernelizeContainerName(o.nodeName),
			Namespace: o.owner.Namespace,
			Labels:    labels,
		},
		Spec: weka.WekaContainerSpec{
			Mode:            weka.WekaContainerModeAdhocOp,
			NodeAffinity:    o.nodeName,
			Image:           o.image,
			ImagePullSecret: o.pullSecret,
			Instructions:    &weka.Instructions{Type: weka.InstructionTypeKernelize},
			Tolerations:     o.tolerations,
			// kernelize scans /proc/*/fd to find NVMe devices other tenants have open; without the
			// host PID namespace it only sees its own pod's PIDs and would rebind their live devices.
			HostPID:            true,
			ServiceAccountName: o.serviceAccount,
		},
	}

	if err := ctrl.SetControllerReference(o.owner, container, o.scheme); err != nil {
		return err
	}

	if err := o.client.Create(ctx, container); err != nil {
		return err
	}

	o.container = container
	return nil
}

func (o *KernelizeOperation) PollResults(ctx context.Context) error {
	if o.container.Status.ExecutionResult != nil {
		return nil
	}

	if time.Since(o.container.CreationTimestamp.Time) < kernelizeStaleTimeout {
		return lifecycle.NewWaitErrorWithDuration(errors.New("kernelize container execution result is not ready"), 5*time.Second)
	}

	// Stale: no result within timeout (image pull failure, unschedulable node, etc). Never block the
	// proxy pod for this; warn and clean up so the next pass can retry from scratch.
	o.result = KernelizeResult{Err: "kernelize container produced no result within timeout"}
	o.recordFailure(ctx)
	if err := o.client.Delete(ctx, o.container); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	o.container = nil
	return nil
}

func (o *KernelizeOperation) ProcessResult(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	r := &o.result
	if err := json.Unmarshal([]byte(*o.container.Status.ExecutionResult), r); err != nil {
		*r = KernelizeResult{Err: fmt.Sprintf("failed to unmarshal kernelize result: %s", err)}
	}
	logger.Info("kernelize result", "node", o.nodeName, "result", *r)

	if r.Err == "" && r.Failed > 0 {
		r.Err = fmt.Sprintf("%d of %d candidate(s) failed to kernelize", r.Failed, r.Candidates)
	}
	if r.Err != "" {
		o.recordFailure(ctx)
	} else {
		util2.RecordEvent(o.recorder, o.owner, corev1.EventTypeNormal, "Kernelized", consts.ActionKernelize,
			fmt.Sprintf("kernelized node %s: %d candidate(s), %d filtered out as in-use, %d recovered, %d failed",
				o.nodeName, r.Candidates, r.FilteredOutInUse, r.Recovered, r.Failed))
	}
	return setKernelizeAnnotation(ctx, o.client, o.container, annotationKernelizeProcessedAt)
}

func setKernelizeAnnotation(ctx context.Context, c client.Client, container *weka.WekaContainer, key string) error {
	patch := client.MergeFrom(container.DeepCopy())
	annotations := container.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[key] = time.Now().UTC().Format(time.RFC3339)
	container.SetAnnotations(annotations)
	return c.Patch(ctx, container, patch)
}

// FinalizeKernelize runs once the proxy pod exists: it marks the proxy's kernelize container as
// consumed, so a later pod (re)creation runs kernelize afresh, and deletes it after kernelizeRetention.
func FinalizeKernelize(ctx context.Context, c client.Client, owner *weka.WekaContainer, nodeName weka.NodeName) error {
	ref := weka.ObjectReference{Name: kernelizeContainerName(nodeName), Namespace: owner.Namespace}
	container, err := discovery.GetContainerByName(ctx, c, ref)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if container.DeletionTimestamp != nil || !metav1.IsControlledBy(container, owner) {
		return nil
	}

	consumedAt, ok := container.GetAnnotations()[annotationKernelizeConsumedAt]
	if !ok {
		return setKernelizeAnnotation(ctx, c, container, annotationKernelizeConsumedAt)
	}
	t, err := time.Parse(time.RFC3339, consumedAt)
	if err != nil {
		return errors.Wrapf(err, "invalid %s annotation on %s", annotationKernelizeConsumedAt, container.Name)
	}
	if time.Since(t) < kernelizeRetention {
		return nil
	}
	if err := c.Delete(ctx, container); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (o *KernelizeOperation) recordFailure(ctx context.Context) {
	logger := instrumentation.CurrentSpanLogger(ctx)
	logger.Warn("kernelize failed, proxy pod creation continues regardless", "node", o.nodeName, "err", o.result.Err)
	util2.RecordEvent(o.recorder, o.owner, corev1.EventTypeWarning, "KernelizeFailed", consts.ActionKernelize,
		fmt.Sprintf("kernelize on node %s failed: %s", o.nodeName, o.result.Err))
}

func (o *KernelizeOperation) GetJsonResult() string {
	resultJSON, _ := json.Marshal(o.result) //nolint:errcheck // marshal of known-serializable struct; error not possible
	return string(resultJSON)
}
