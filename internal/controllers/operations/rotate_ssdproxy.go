package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
)

const (
	rotateSsdProxyWaitDuration = 15 * time.Second

	// Two independent warning signals: "blocked" (Pending, gate refused — zero tenant impact) warns
	// later and repeats less often than "stuck" (InFlight, already disrupted).
	rotateSsdProxyBlockedWarnThreshold = 15 * time.Minute
	rotateSsdProxyBlockedWarnRepeat    = 30 * time.Minute
	rotateSsdProxyStuckWarnThreshold   = 5 * time.Minute
	rotateSsdProxyStuckWarnRepeat      = 10 * time.Minute
	// Must stay wider than rotateSsdProxyWaitDuration or a reconcile tick can step over the boundary.
	rotateSsdProxyParkedWarnWindow = 30 * time.Second

	rotateSsdProxyEventReasonStarted          = "SsdProxyRotationStarted"
	rotateSsdProxyEventReasonBlocked          = "SsdProxyRotationBlocked"
	rotateSsdProxyEventReasonStalled          = "SsdProxyRotationStalled"
	rotateSsdProxyEventReasonNodeComplete     = "SsdProxyRotationNodeComplete"
	rotateSsdProxyEventReasonCampaignComplete = "SsdProxyRotationCampaignComplete"
	rotateSsdProxyReadyInternalStatus         = "READY"
)

// RotateSsdProxyOperation rolls a new ssdproxy image across every targeted node, one at a time,
// gated on both ends by the cross-cluster disruption checks in proxy_disruption_gate.go. No Failed
// phase, no per-node timeout: a stuck node parks indefinitely rather than failing the campaign. See
// doc/operator/operations/rotate-ssdproxy.md for the full design.
type RotateSsdProxyOperation struct {
	mgr         ctrl.Manager
	client      client.Client
	kubeService kubernetes.KubeService
	execSvc     exec.ExecService
	payload     *weka.RotateSsdProxyPayload
	ownerRef    client.Object
	recorder    record.EventRecorder

	// gate defaults to EvaluateNodeDisruption; injectable for tests, since the real gate needs a live
	// Secret + HTTP call and is unreachable through controller-runtime fakes. See evaluateGate.
	gate func(ctx context.Context, mgr ctrl.Manager, execSvc exec.ExecService, node weka.NodeName, proxy *weka.WekaContainer) ([]ClusterVerdict, error)

	results RotateSsdProxyResult

	// progressCallback persists the current result without completing; required since this operation
	// parks rather than finishing on every non-terminal step.
	progressCallback lifecycle.StepFunc
	// successCallback writes the final result and marks the owner Done.
	successCallback lifecycle.StepFunc
	// failureCallback writes the current result and marks the owner Failed, for terminal errors.
	failureCallback lifecycle.StepFunc
}

// NewRotateSsdProxyOperation builds the rotate-ssdproxy operation. execSvc is required by the L2
// disruption gate to exec into a live container and fetch weka status.
func NewRotateSsdProxyOperation(
	mgr ctrl.Manager,
	execSvc exec.ExecService,
	payload *weka.RotateSsdProxyPayload,
	ownerRef client.Object,
	recorder record.EventRecorder,
	progressCallback lifecycle.StepFunc,
	successCallback lifecycle.StepFunc,
	failureCallback lifecycle.StepFunc,
) *RotateSsdProxyOperation {
	kclient := mgr.GetClient()
	if payload == nil {
		payload = &weka.RotateSsdProxyPayload{}
	}
	return &RotateSsdProxyOperation{
		mgr:              mgr,
		client:           kclient,
		kubeService:      kubernetes.NewKubeService(kclient),
		execSvc:          execSvc,
		payload:          payload,
		ownerRef:         ownerRef,
		recorder:         recorder,
		gate:             EvaluateNodeDisruption,
		progressCallback: progressCallback,
		successCallback:  successCallback,
		failureCallback:  failureCallback,
	}
}

func (o *RotateSsdProxyOperation) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "RotateSsdProxy",
		Run:  AsRunFunc(o),
	}
}

func (o *RotateSsdProxyOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		// Skip re-entering the state machine once the owner is terminal. Without this a Failed
		// campaign would re-enter Plan -> failTerminally every ~15s forever.
		&lifecycle.SimpleStep{
			Name:            "SkipIfTerminal",
			Run:             func(context.Context) error { return nil },
			Predicates:      lifecycle.Predicates{func() bool { return ownerDone(o.ownerRef) || ownerFailed(o.ownerRef) }},
			FinishOnSuccess: true,
		},
		&lifecycle.SimpleStep{Name: "Plan", Run: o.Plan},
		&lifecycle.SimpleStep{Name: "AdvanceOne", Run: o.AdvanceOne},
		&lifecycle.SimpleStep{
			Name:       "Finalize",
			Run:        o.finalize,
			Predicates: lifecycle.Predicates{func() bool { return o.successCallback != nil }},
		},
	}
}

// finalize emits the campaign-completion event, then delegates to successCallback (owner-status
// write to Done). Reached exactly once: the next reconcile short-circuits at SkipIfTerminal.
func (o *RotateSsdProxyOperation) finalize(ctx context.Context) error {
	if o.recorder != nil {
		o.recorder.Eventf(o.ownerRef, corev1.EventTypeNormal, rotateSsdProxyEventReasonCampaignComplete,
			"ssdproxy rotation complete: %d nodes on image %s", countDoneOrSkipped(o.results.Nodes), o.results.TargetImage)
	}
	return o.successCallback(ctx)
}

func (o *RotateSsdProxyOperation) GetJsonResult() string {
	resultJSON, err := json.Marshal(o.results)
	if err != nil {
		return ""
	}
	return string(resultJSON)
}

// warn emits a Warning event on the owner if a recorder is set; no-op otherwise.
func (o *RotateSsdProxyOperation) warn(reason, format string, args ...any) {
	if o.recorder == nil {
		return
	}
	o.recorder.Eventf(o.ownerRef, corev1.EventTypeWarning, reason, format, args...)
}

// ---------------------------------------------------------------------------
// Plan
// ---------------------------------------------------------------------------

// Plan rehydrates prior campaign state first (every return path below persists o.results, and an
// empty o.results would wipe out node history), then resolves the target image, refuses a second
// concurrent campaign, and merges observed proxies into state. Never advances a node — AdvanceOne's job.
func (o *RotateSsdProxyOperation) Plan(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "Plan")
	defer logger.End()

	previous := o.previousResult()
	o.rehydrateFrom(previous)

	targetImage := resolveTargetImage(o.payload.TargetImage, config.Config.DriveSharing.SsdProxyImageOverride)
	if targetImage == "" {
		// The one terminal error: only editing the CR or the helm value resolves it, never waiting.
		return o.failTerminally(ctx, errors.New(
			"no target image: set spec.payload.rotateSsdProxyPayload.targetImage on the WekaManualOperation, "+
				"or the operator's driveSharing.ssdProxy.imageOverride Helm value (env SSD_PROXY_IMAGE_OVERRIDE)",
		))
	}

	// targetImage is immutable once a campaign has planned: with payload.targetImage empty it can
	// still change via the helm override, but a node already patched can't be safely re-targeted.
	if previous != nil && previous.TargetImage != "" && previous.TargetImage != targetImage {
		return o.failTerminally(ctx, fmt.Errorf(
			"targetImage is immutable once a campaign has planned (was %q, now %q); delete this "+
				"WekaManualOperation and create a new one to rotate to a different image",
			previous.TargetImage, targetImage))
	}
	o.results.TargetImage = targetImage

	// Transient failures (another campaign running, listing blip) park instead of failing the owner,
	// since onProgress never clears Failed once set.
	if err := o.refuseIfAnotherCampaignRunning(ctx); err != nil {
		return o.waitWithPersistedErr(ctx, err)
	}

	targets, err := resolveTargetProxies(ctx, o.kubeService, o.payload.NodeSelector)
	if err != nil {
		return o.waitWithPersistedErr(ctx, err)
	}

	var previousNodes []RotateSsdProxyNodeState
	if previous != nil {
		previousNodes = previous.Nodes
	}

	if len(o.payload.NodeSelector) > 0 && len(targets) == 0 {
		// A non-empty selector matching nothing is likely a typo, not a legitimate no-op (unlike an
		// empty selector) — park rather than fail, since the label may not exist yet.
		return o.waitWithPersistedErr(ctx, fmt.Errorf(
			"nodeSelector %v matched no nodes with an ssdproxy; check the node labels", o.payload.NodeSelector,
		))
	}

	nodes, dropped := mergeCampaignNodes(previousNodes, targets, targetImage)
	// dropped is already filtered to non-Pending nodes. Additionally event InFlight/Done drops — the
	// two phases where a silent drop would hide a still-disrupted or already-rotated node.
	for _, n := range dropped {
		logger.Info("Ssdproxy rotation: node dropped from campaign, proxy no longer targeted",
			"node", n.Node, "phase", n.Phase, "proxy", n.ProxyName)
		if n.Phase == RotateSsdProxyPhaseInFlight || n.Phase == RotateSsdProxyPhaseDone {
			o.warn(rotateSsdProxyEventReasonStalled,
				"Node %s (phase %s) dropped from the ssdproxy rotation campaign: its proxy is no longer targeted",
				n.Node, n.Phase)
		}
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Node < nodes[j].Node })

	o.results.Nodes = nodes
	o.results.Total = len(nodes)
	o.results.Done = countDoneOrSkipped(nodes)
	o.results.Err = ""
	// Mirrors clearing Err above: a resolved block must not leave a stale timestamp for a later,
	// unrelated block to inherit.
	o.results.BlockedSince = nil

	logger.Info("Planned ssdproxy rotation",
		"target_image", targetImage,
		"total", o.results.Total,
		"done_or_skipped", o.results.Done,
	)
	return nil
}

// rehydrateFrom restores prior campaign state into o.results before Plan's resolution runs, so
// every one of Plan's early-return paths persists real history instead of a zeroed result.
func (o *RotateSsdProxyOperation) rehydrateFrom(previous *RotateSsdProxyResult) {
	if previous == nil {
		return
	}
	o.results.TargetImage = previous.TargetImage
	o.results.Nodes = previous.Nodes
	o.results.Total = previous.Total
	o.results.Done = countDoneOrSkipped(previous.Nodes)
	// Load-bearing: without this, waitWithPersistedErr would re-stamp BlockedSince every reconcile.
	o.results.BlockedSince = previous.BlockedSince
}

// resolveTargetImage picks the explicit payload image if set, else the operator-wide override.
// Empty when neither is set, which Plan turns into a non-retryable error.
func resolveTargetImage(payloadImage, overrideImage string) string {
	if payloadImage != "" {
		return payloadImage
	}
	return overrideImage
}

// refuseIfAnotherCampaignRunning enforces cross-campaign exclusion: at most one non-terminal
// rotate-ssdproxy WekaManualOperation may exist. Refuses both rather than picking a winner — see
// doc/operator/operations/rotate-ssdproxy.md for why.
func (o *RotateSsdProxyOperation) refuseIfAnotherCampaignRunning(ctx context.Context) error {
	selfOwner, ok := o.ownerRef.(*weka.WekaManualOperation)
	if !ok {
		// rotate-ssdproxy is WekaManualOperation-only by design (see the CRD's action enum); unreachable.
		return nil
	}

	// Uncached read: a second campaign created moments ago must never be missed due to cache lag,
	// mirroring buildClaimedSet/buildLiveClusterGUIDs in stale_virtual_drives.go.
	var list weka.WekaManualOperationList
	if err := o.mgr.GetAPIReader().List(ctx, &list); err != nil {
		return errors.Wrap(err, "failed to list WekaManualOperations for cross-campaign exclusion")
	}

	for i := range list.Items {
		other := &list.Items[i]
		if other.UID == selfOwner.UID {
			continue
		}
		if other.Spec.Action != weka.WekaManualOperationActionRotateSsdProxy {
			continue
		}
		// A terminal (Done/Failed) or deleting campaign is safely ignored; everything else is a
		// contender, including status "" — a campaign created moments ago, before its first status write.
		if ownerDone(other) || ownerFailed(other) || other.DeletionTimestamp != nil {
			continue
		}
		return fmt.Errorf(
			"another rotate-ssdproxy operation (%s/%s) exists and has not finished; only one may run "+
				"at a time — delete one of them to continue",
			other.Namespace, other.Name,
		)
	}
	return nil
}

// mergeCampaignNodes reconciles prior per-node state against the freshly observed proxy set: new
// nodes start Pending, existing ones keep their phase, and a proxy already on target that we never
// patched is marked Skipped. Nodes that progressed past Pending before their proxy vanished are
// returned in dropped so Plan can log/event them.
func mergeCampaignNodes(previous []RotateSsdProxyNodeState, targets []targetProxy, targetImage string) (nodes, dropped []RotateSsdProxyNodeState) {
	priorByNode := make(map[weka.NodeName]RotateSsdProxyNodeState, len(previous))
	for _, n := range previous {
		priorByNode[n.Node] = n
	}
	targetedNode := make(map[weka.NodeName]bool, len(targets))

	nodes = make([]RotateSsdProxyNodeState, 0, len(targets))
	for i := range targets {
		t := &targets[i]
		targetedNode[t.node] = true
		state, hadPrior := priorByNode[t.node]
		if !hadPrior {
			state = RotateSsdProxyNodeState{
				Node:  t.node,
				Phase: RotateSsdProxyPhasePending,
			}
		}
		state.ProxyName = t.container.Name
		state.Image = t.container.Spec.Image

		// PreviousImage discriminates: "" means never patched by us (already on target -> Skipped);
		// set means we patched it and spec.image reflects that from the instant the patch lands, so
		// leave the phase alone here or InFlight -> Done would be short-circuited before verification.
		if t.container.Spec.Image == targetImage && state.PreviousImage == "" {
			state.Phase = RotateSsdProxyPhaseSkipped
			state.Reason = ""
			state.BlockedSince = nil
		} else if state.Phase == RotateSsdProxyPhaseSkipped {
			// Skipped is a claim about the current image, unlike Done/InFlight; revert if it drifted off target.
			state.Phase = RotateSsdProxyPhasePending
		}

		nodes = append(nodes, state)
	}

	// Walk previous (not the map) so dropped is deterministically ordered.
	for _, n := range previous {
		if targetedNode[n.Node] || n.Phase == RotateSsdProxyPhasePending {
			continue
		}
		dropped = append(dropped, n)
	}

	return nodes, dropped
}

func countDoneOrSkipped(nodes []RotateSsdProxyNodeState) int {
	n := 0
	for _, node := range nodes {
		if node.Phase == RotateSsdProxyPhaseDone || node.Phase == RotateSsdProxyPhaseSkipped {
			n++
		}
	}
	return n
}

// previousResult parses the previous run's JSON result from the owner status. Best-effort: absent
// or garbage decodes to nil, same as decodePreviousOwnerResult's contract everywhere else.
func (o *RotateSsdProxyOperation) previousResult() *RotateSsdProxyResult {
	return decodePreviousOwnerResult[RotateSsdProxyResult](o.ownerRef)
}

// ---------------------------------------------------------------------------
// AdvanceOne
// ---------------------------------------------------------------------------

// AdvanceOne is the rotation state machine. At most one node is touched per reconcile:
//   - If a node is InFlight, verify completion from live state; done -> Done, else park.
//   - Else if paused, persist and wait without picking a new node.
//   - Else pick the first Pending node, run the gate, and either patch + mark InFlight, or park.
//
// Parking returns a WaitErrorWithDuration — "not yet done, requeue" — never a Failed phase.
func (o *RotateSsdProxyOperation) AdvanceOne(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "AdvanceOne")
	defer logger.End()

	if o.results.Err != "" {
		// Defensive: every Plan error path returns a WaitError, stopping the engine before AdvanceOne.
		// Kept so a future path that records Err without returning cannot silently start rotating nodes.
		return errors.New(o.results.Err)
	}

	if idx := indexOfPhase(o.results.Nodes, RotateSsdProxyPhaseInFlight); idx >= 0 {
		return o.advanceInFlight(ctx, idx)
	}

	if o.payload.Paused {
		o.results.CurrentNode = ""
		if err := o.persist(ctx); err != nil {
			return err
		}
		return lifecycle.NewWaitErrorWithDuration(errors.New("rotation paused"), rotateSsdProxyWaitDuration)
	}

	// Nodes are kept sorted by node name by Plan, so "first Pending" is deterministic across resumes.
	idx := indexOfPhase(o.results.Nodes, RotateSsdProxyPhasePending)
	if idx < 0 {
		// Nothing Pending and nothing InFlight: every node is Done or Skipped.
		o.results.CurrentNode = ""
		logger.Info("Ssdproxy rotation complete", "total", o.results.Total, "done", o.results.Done)
		return nil
	}
	return o.advancePending(ctx, idx)
}

func indexOfPhase(nodes []RotateSsdProxyNodeState, phase string) int {
	for i := range nodes {
		if nodes[i].Phase == phase {
			return i
		}
	}
	return -1
}

// advanceInFlight verifies the node at nodes[idx] (which must be InFlight) from live cluster/pod
// state and either completes it or parks with a specific, refreshed reason.
func (o *RotateSsdProxyOperation) advanceInFlight(ctx context.Context, idx int) error {
	node := &o.results.Nodes[idx]
	o.results.CurrentNode = string(node.Node)
	logger := instrumentation.CurrentSpanLogger(ctx)

	proxy, pod, err := o.liveProxyAndPod(ctx, node.ProxyName)
	if err != nil {
		return o.parkOnErr(ctx, node, "fetch live proxy/pod state", err)
	}

	// Recovers the crash window between advancePending's persist and patch: an InFlight node whose
	// proxy never got patched. Re-gates (applyTargetImage), not a bare patch — time has passed.
	if proxy.Spec.Image != o.results.TargetImage {
		blocked, applyErr := o.applyTargetImage(ctx, node, proxy)
		if applyErr != nil {
			return o.parkOnErr(ctx, node, "re-apply target image", applyErr)
		}
		if blocked {
			return o.parkNode(ctx, node)
		}
		if persistErr := o.persist(ctx); persistErr != nil {
			return persistErr
		}
		return lifecycle.NewWaitErrorWithDuration(
			errors.New("re-applied target image, waiting for pod restart"), rotateSsdProxyWaitDuration)
	}

	// pod == nil is the normal rotation window (see liveProxyAndPod). Must precede verifyNodeComplete,
	// which dereferences pod unconditionally. parkNode, not parkOnErr — not a failure.
	if pod == nil {
		node.Reason = "pod deleted, waiting for recreation"
		return o.parkNode(ctx, node)
	}

	verdicts, blockReason, err := o.verifyNodeComplete(ctx, proxy, pod, o.results.TargetImage, node.Node)
	if err != nil {
		return o.parkOnErr(ctx, node, "verify rotation completion", err)
	}
	if blockReason != "" {
		node.Reason = blockReason
		o.results.Blocked = verdicts
		return o.parkNode(ctx, node)
	}

	node.Phase = RotateSsdProxyPhaseDone
	node.Reason = ""
	o.results.Blocked = nil
	o.results.CurrentNode = ""
	o.results.Done = countDoneOrSkipped(o.results.Nodes)
	logger.Info("Ssdproxy rotation completed for node", "node", node.Node)

	// Fires exactly once per node: this branch is only reached on the transition into Done.
	if o.recorder != nil {
		o.recorder.Eventf(o.ownerRef, corev1.EventTypeNormal, rotateSsdProxyEventReasonNodeComplete,
			"Rotated ssdproxy on node %s (%d/%d nodes complete)", node.Node, o.results.Done, o.results.Total)
	}

	if err := o.persist(ctx); err != nil {
		return err
	}
	// WaitError.Err must never be nil: Error() calls w.Err.Error() unconditionally.
	return lifecycle.NewWaitErrorWithDuration(errors.New("node completed, advancing to next node"), rotateSsdProxyWaitDuration)
}

func (o *RotateSsdProxyOperation) liveProxy(ctx context.Context, proxyName string) (*weka.WekaContainer, error) {
	operatorNamespace, err := util.GetPodNamespace()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get operator namespace")
	}
	proxy := &weka.WekaContainer{}
	if err := o.client.Get(ctx, client.ObjectKey{Name: proxyName, Namespace: operatorNamespace}, proxy); err != nil {
		return nil, errors.Wrap(err, "failed to get proxy container")
	}
	return proxy, nil
}

// liveProxyAndPod additionally fetches proxyName's pod (pod name == container name, same namespace).
// A nil pod with a nil error is expected: wekacontainer deletes the pod on an image mismatch and
// waits to observe its absence before recreating it, so it briefly doesn't exist mid-rotation.
// Callers must nil-check before use (GetWekaPodContainer panics on nil).
func (o *RotateSsdProxyOperation) liveProxyAndPod(ctx context.Context, proxyName string) (*weka.WekaContainer, *corev1.Pod, error) {
	proxy, err := o.liveProxy(ctx, proxyName)
	if err != nil {
		return nil, nil, err
	}

	pod := &corev1.Pod{}
	if err := o.client.Get(ctx, client.ObjectKey{Name: proxyName, Namespace: proxy.Namespace}, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return proxy, nil, nil
		}
		return nil, nil, errors.Wrap(err, "failed to get proxy pod")
	}

	return proxy, pod, nil
}

// verifyNodeComplete is the completion predicate for an InFlight node: pod image, proxy status, and
// internal status must all match target, then the post-rotation L2 gate must find recovery too.
// err is only for infrastructure failures ("cannot tell yet"); a normal not-ready result is blockReason.
func (o *RotateSsdProxyOperation) verifyNodeComplete(ctx context.Context, proxy *weka.WekaContainer, pod *corev1.Pod, targetImage string, node weka.NodeName) ([]ClusterVerdict, string, error) {
	ready, reason, err := podAndProxyReady(proxy, pod, targetImage)
	if err != nil {
		return nil, "", err
	}
	if !ready {
		return nil, reason, nil
	}

	verdicts, err := VerifyNodeRecovered(ctx, o.mgr, o.execSvc, node, proxy)
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to verify node recovery")
	}
	allowed, blockReason := AllAllowed(verdicts)
	if !allowed {
		return verdicts, blockReason, nil
	}
	return nil, "", nil
}

// podAndProxyReady is the pure pod/proxy half of the completion predicate, split out from
// verifyNodeComplete (which also calls the L2 gate) so it is table-testable without any exec dependency.
func podAndProxyReady(proxy *weka.WekaContainer, pod *corev1.Pod, targetImage string) (ready bool, blockReason string, err error) {
	wekaContainer, err := resources.GetWekaPodContainer(pod)
	if err != nil {
		return false, "", errors.Wrap(err, "failed to find weka container in pod")
	}
	if wekaContainer.Image != targetImage {
		return false, fmt.Sprintf("pod not yet recreated on target image (running %q, want %q)", wekaContainer.Image, targetImage), nil
	}
	if proxy.Status.Status != weka.Running {
		return false, fmt.Sprintf("proxy status is %q, want %q", proxy.Status.Status, weka.Running), nil
	}
	if proxy.Status.InternalStatus != rotateSsdProxyReadyInternalStatus {
		return false, fmt.Sprintf("proxy internal status is %q, want %q", proxy.Status.InternalStatus, rotateSsdProxyReadyInternalStatus), nil
	}
	return true, "", nil
}

// advancePending runs the pre-restart L2 gate for the node at nodes[idx] (which must be Pending)
// and either patches its proxy image and marks it InFlight, or parks it with the blocking verdicts.
func (o *RotateSsdProxyOperation) advancePending(ctx context.Context, idx int) error {
	node := &o.results.Nodes[idx]
	o.results.CurrentNode = string(node.Node)
	logger := instrumentation.CurrentSpanLogger(ctx)

	// Re-read rather than reusing Plan's snapshot: the patch below needs a current resourceVersion.
	proxy, err := o.liveProxy(ctx, node.ProxyName)
	if err != nil {
		return o.parkOnErr(ctx, node, "get proxy container", err)
	}

	blocked, err := o.evaluateGate(ctx, node, proxy)
	if err != nil {
		return o.parkOnErr(ctx, node, "evaluate disruption gate", err)
	}
	if blocked {
		return o.parkNode(ctx, node)
	}

	if node.PreviousImage == "" {
		node.PreviousImage = proxy.Spec.Image
	}
	now := metav1.Now()
	node.StartedAt = &now
	// Cleared here (not left alone): the stuck-timer must measure from StartedAt with no block residue.
	node.BlockedSince = nil
	node.Phase = RotateSsdProxyPhaseInFlight
	node.Reason = ""
	o.results.Blocked = nil

	// Intent before action: persist Phase=InFlight before the patch below, so a crash between the two
	// leaves a resumable InFlight node instead of a Pending one that loses PreviousImage/StartedAt.
	if err := o.persist(ctx); err != nil {
		return err
	}
	if err := o.patchProxyImage(ctx, proxy, o.results.TargetImage); err != nil {
		// Already persisted as InFlight, so this doesn't strand the node — advanceInFlight re-patches next reconcile.
		return o.parkOnErr(ctx, node, "patch proxy image", err)
	}

	if o.recorder != nil {
		o.recorder.Eventf(o.ownerRef, corev1.EventTypeNormal, rotateSsdProxyEventReasonStarted,
			"Started ssdproxy rotation on node %s (%d/%d nodes complete): %s -> %s",
			node.Node, o.results.Done, o.results.Total, node.PreviousImage, o.results.TargetImage)
	}
	logger.Info("Started ssdproxy rotation on node", "node", node.Node, "previous_image", node.PreviousImage, "target_image", o.results.TargetImage)

	// WaitError.Err must never be nil: Error() calls w.Err.Error() unconditionally.
	return lifecycle.NewWaitErrorWithDuration(
		errors.New("rotation started, waiting for pod restart and health verification"), rotateSsdProxyWaitDuration)
}

// evaluateGate runs the pre-restart gate and records the outcome (node.Reason/o.results.Blocked).
// Shared by advancePending and applyTargetImage so both patch attempts gate identically.
func (o *RotateSsdProxyOperation) evaluateGate(ctx context.Context, node *RotateSsdProxyNodeState, proxy *weka.WekaContainer) (blocked bool, err error) {
	verdicts, err := o.gate(ctx, o.mgr, o.execSvc, node.Node, proxy)
	if err != nil {
		return false, err
	}
	allowed, blockReason := AllAllowed(verdicts)
	if !allowed {
		node.Reason = blockReason
		o.results.Blocked = verdicts
		return true, nil
	}
	o.results.Blocked = nil
	// Cleared alongside Blocked so a stale Reason never outlives the condition that produced it.
	node.Reason = ""
	return false, nil
}

// applyTargetImage re-gates (evaluateGate) and, if allowed, patches proxy to the target image.
// Used only by advanceInFlight's recovery branch — a re-patch is just as disruptive as the first.
func (o *RotateSsdProxyOperation) applyTargetImage(ctx context.Context, node *RotateSsdProxyNodeState, proxy *weka.WekaContainer) (blocked bool, err error) {
	blocked, err = o.evaluateGate(ctx, node, proxy)
	if err != nil || blocked {
		return blocked, err
	}
	if err := o.patchProxyImage(ctx, proxy, o.results.TargetImage); err != nil {
		return false, err
	}
	return false, nil
}

// patchProxyImage merge-patches only spec.image on the proxy CR (upgrade.go:55-83's RawPatch
// mechanics). podConfigHash is deliberately omitted: the ownerless ssdproxy self-derives it from
// spec.image, and setting it here would break that self-derivation on future rotations.
func (o *RotateSsdProxyOperation) patchProxyImage(ctx context.Context, proxy *weka.WekaContainer, targetImage string) error {
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"image": targetImage,
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return errors.Wrap(err, "failed to marshal image patch")
	}
	if err := o.client.Patch(ctx, proxy, client.RawPatch(types.MergePatchType, patchBytes)); err != nil {
		return errors.Wrapf(err, "failed to patch proxy %s", proxy.Name)
	}
	return nil
}

// parkNode persists the current result and requeues via WaitErrorWithDuration, never marking
// anything Failed. Stamps BlockedSince the first time a still-Pending node parks (never refreshed,
// so elapsed keeps one stable origin) and emits a throttled Warning event. Observability only.
func (o *RotateSsdProxyOperation) parkNode(ctx context.Context, node *RotateSsdProxyNodeState) error {
	if node.Phase == RotateSsdProxyPhasePending {
		if node.BlockedSince == nil {
			now := metav1.Now()
			node.BlockedSince = &now
		}
		o.maybeWarnParked(node, node.BlockedSince, blockedWarnSignal)
	} else {
		o.maybeWarnParked(node, node.StartedAt, stuckWarnSignal)
	}
	if err := o.persist(ctx); err != nil {
		return err
	}
	return lifecycle.NewWaitErrorWithDuration(errors.New(node.Reason), rotateSsdProxyWaitDuration)
}

// parkedWarnSignal describes one of the two throttled parked-node warning signals. The two differ
// only in which timestamp they measure from, how urgent they are, and what they say — so they are a
// table, not two code paths.
type parkedWarnSignal struct {
	eventReason string
	// description completes "Node X has been <description> for 5m: <reason>".
	description string
	threshold   time.Duration
	repeat      time.Duration
}

var (
	// blockedWarnSignal: node parked while Pending — zero tenant impact, so it warns later and repeats less often.
	blockedWarnSignal = parkedWarnSignal{
		eventReason: rotateSsdProxyEventReasonBlocked,
		description: "blocked at the pre-restart gate",
		threshold:   rotateSsdProxyBlockedWarnThreshold,
		repeat:      rotateSsdProxyBlockedWarnRepeat,
	}
	// stuckWarnSignal: node already patched (InFlight) — active tenant impact, warns sooner and repeats more often.
	stuckWarnSignal = parkedWarnSignal{
		eventReason: rotateSsdProxyEventReasonStalled,
		description: "stuck in-flight",
		threshold:   rotateSsdProxyStuckWarnThreshold,
		repeat:      rotateSsdProxyStuckWarnRepeat,
	}
	// campaignParkedWarnSignal: Plan parked with no node to attach to; reuses blockedWarnSignal's thresholds.
	campaignParkedWarnSignal = parkedWarnSignal{
		eventReason: rotateSsdProxyEventReasonBlocked,
		description: "blocked before any node could be targeted",
		threshold:   rotateSsdProxyBlockedWarnThreshold,
		repeat:      rotateSsdProxyBlockedWarnRepeat,
	}
	// campaignParkedInFlightWarnSignal: same, but catches a node already InFlight; uses stuckWarnSignal's thresholds.
	campaignParkedInFlightWarnSignal = parkedWarnSignal{
		eventReason: rotateSsdProxyEventReasonStalled,
		description: "patched but unverified while the campaign is blocked",
		threshold:   rotateSsdProxyStuckWarnThreshold,
		repeat:      rotateSsdProxyStuckWarnRepeat,
	}
)

// shouldWarn reports whether the Warning event should fire this cycle, purely from elapsed time
// (no persisted "last warned at"): fires once elapsed crosses threshold, then every repeat after.
func (s parkedWarnSignal) shouldWarn(elapsed time.Duration) bool {
	if elapsed < s.threshold {
		return false
	}
	return (elapsed-s.threshold)%s.repeat < rotateSsdProxyParkedWarnWindow
}

// parkOnErr records "failed to <action>: <err>" as the node's reason and parks — every
// infrastructure failure in the state machine funnels through here rather than failing the campaign.
func (o *RotateSsdProxyOperation) parkOnErr(ctx context.Context, node *RotateSsdProxyNodeState, action string, err error) error {
	node.Reason = fmt.Sprintf("failed to %s: %v", action, err)
	return o.parkNode(ctx, node)
}

// maybeWarnParked emits signal's throttled Warning event if the node has been parked (measured from
// since) long enough to warrant it. Pure observability — must never influence control flow.
func (o *RotateSsdProxyOperation) maybeWarnParked(node *RotateSsdProxyNodeState, since *metav1.Time, signal parkedWarnSignal) {
	if since == nil {
		return
	}
	elapsed := time.Since(since.Time)
	if !signal.shouldWarn(elapsed) {
		return
	}
	// Rounded to minutes, not seconds: two consecutive ticks can land in the same warn window and
	// each independently decide to warn — matching messages let the event recorder aggregate them
	// into one event instead of two. Don't narrow the window instead; a delayed tick could miss it.
	o.warn(signal.eventReason, "Node %s has been %s for %s: %s", node.Node, signal.description, elapsed.Round(time.Minute), node.Reason)
}

// maybeWarnParkedCampaign is maybeWarnParked's campaign-scoped sibling, keyed on the campaign's own
// BlockedSince since a campaign-scope park has no node to attach to.
func (o *RotateSsdProxyOperation) maybeWarnParkedCampaign(reason string) {
	if o.results.BlockedSince == nil {
		return
	}
	elapsed := time.Since(o.results.BlockedSince.Time)

	// A campaign-scope park can catch a node already InFlight — patched but unverified — which is
	// active impact like a stuck node, so it uses the stuck thresholds and names the node.
	if idx := indexOfPhase(o.results.Nodes, RotateSsdProxyPhaseInFlight); idx >= 0 {
		if !campaignParkedInFlightWarnSignal.shouldWarn(elapsed) {
			return
		}
		o.warn(campaignParkedInFlightWarnSignal.eventReason, "Node %s has been %s for %s: %s",
			o.results.Nodes[idx].Node, campaignParkedInFlightWarnSignal.description, elapsed.Round(time.Minute), reason)
		return
	}

	if !campaignParkedWarnSignal.shouldWarn(elapsed) {
		return
	}
	// Rounded to minutes for the event-aggregation reason maybeWarnParked explains.
	o.warn(campaignParkedWarnSignal.eventReason, "Ssdproxy rotation campaign has been %s for %s: %s",
		campaignParkedWarnSignal.description, elapsed.Round(time.Minute), reason)
}

// failTerminally records err, marks the owner Failed, and returns a WaitError so the engine
// requeues instead of hot-looping. Reserved for errors that cannot clear on their own.
func (o *RotateSsdProxyOperation) failTerminally(ctx context.Context, err error) error {
	o.results.Err = err.Error()
	if o.failureCallback != nil && !ownerFailed(o.ownerRef) {
		// Guarded so a permanently-misconfigured campaign rewrites its status once, not every ~15s tick.
		// Plan runs before AdvanceOne, so a node can still be InFlight here; warn since this is the
		// only record of which node was left unverified.
		if idx := indexOfPhase(o.results.Nodes, RotateSsdProxyPhaseInFlight); idx >= 0 {
			node := o.results.Nodes[idx]
			// node.Image is what it was patched to as of the last Plan, not necessarily what the pod runs.
			o.warn(rotateSsdProxyEventReasonStalled,
				"Node %s was left in-flight and unverified when the campaign failed terminally: patched to "+
					"image %s (was %s), pod state never confirmed: %s",
				node.Node, node.Image, node.PreviousImage, err.Error())
		}
		o.failureCallback(ctx) //nolint:errcheck // callback error is informational; returning primary error
	}
	return lifecycle.NewWaitErrorWithDuration(err, rotateSsdProxyWaitDuration)
}

// waitWithPersistedErr records a transient Plan failure and requeues, leaving the owner Running (a
// resolvable condition, not Failed). Stamps BlockedSince on first park (same one-origin rationale as
// parkNode's) and emits the throttled campaign-scoped Warning.
func (o *RotateSsdProxyOperation) waitWithPersistedErr(ctx context.Context, err error) error {
	o.results.Err = err.Error()
	if o.results.BlockedSince == nil {
		now := metav1.Now()
		o.results.BlockedSince = &now
	}
	o.maybeWarnParkedCampaign(err.Error())
	if perr := o.persist(ctx); perr != nil {
		return perr
	}
	return lifecycle.NewWaitErrorWithDuration(err, rotateSsdProxyWaitDuration)
}

// persist writes the current result to the owner status via progressCallback. Callers are expected
// to always supply one — this operation parks rather than finishing in one pass.
func (o *RotateSsdProxyOperation) persist(ctx context.Context) error {
	if o.progressCallback == nil {
		return nil
	}
	if err := o.progressCallback(ctx); err != nil {
		return errors.Wrap(err, "failed to persist rotation progress")
	}
	return nil
}
