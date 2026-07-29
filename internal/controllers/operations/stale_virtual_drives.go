package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/internal/services/ssdproxy"
	"github.com/weka/weka-operator/pkg/util"
)

const (
	staleVidsDetectedEventReason = "StaleVirtualDrivesDetected"
	staleVidRemovedEventReason   = "StaleVirtualDriveRemoved"
	staleVidsWaitDuration        = 15 * time.Second
)

// StaleVirtualDrivesOperation scans every targeted ssdproxy's virtual drives (VIDs) and diffs
// them against the union of all live WekaContainer allocations to find stale VIDs. Detection
// always runs and reports; deletion is opt-in (payload.DeleteStaleVids) and double-gated by a
// two-cycle fingerprint-stability check plus a final unclaimed re-validation before removal.
//
// It is dispatched identically from the WekaManualOperation (one-shot) and WekaPolicy (periodic)
// controllers. The owner CR's status carries the JSON result across cycles, which the stability
// gate reads back as the previous fingerprint.
type StaleVirtualDrivesOperation struct {
	mgr         ctrl.Manager
	client      client.Client
	kubeService kubernetes.KubeService
	proxyClient *ssdproxy.Client
	payload     *weka.CleanStaleVirtualDrivesPayload
	ownerRef    client.Object
	recorder    record.EventRecorder

	results weka.StaleVirtualDrivesResult
	// vidToProxyUID maps a scanned virtual UUID to the UID of the exact ssdproxy container it was
	// observed on, so removal targets that same proxy (a node may briefly host more than one).
	vidToProxyUID map[string]string

	// progressCallback persists the current result to the owner status without completing the
	// operation. Set by the manual-op controller so a second confirming cycle can read back the
	// fingerprint; nil for the policy controller, where each Interval run is its own cycle.
	progressCallback lifecycle.StepFunc
	// successCallback writes the final result and marks the owner Done.
	successCallback lifecycle.StepFunc
}

// NewStaleVirtualDrivesOperation builds the shared stale-virtual-drives operation.
func NewStaleVirtualDrivesOperation(
	mgr ctrl.Manager,
	payload *weka.CleanStaleVirtualDrivesPayload,
	ownerRef client.Object,
	recorder record.EventRecorder,
	progressCallback lifecycle.StepFunc,
	successCallback lifecycle.StepFunc,
) *StaleVirtualDrivesOperation {
	kclient := mgr.GetClient()
	if payload == nil {
		payload = &weka.CleanStaleVirtualDrivesPayload{}
	}
	return &StaleVirtualDrivesOperation{
		mgr:              mgr,
		client:           kclient,
		kubeService:      kubernetes.NewKubeService(kclient),
		proxyClient:      ssdproxy.NewClient(kubernetes.NewKubeService(kclient)),
		payload:          payload,
		ownerRef:         ownerRef,
		recorder:         recorder,
		progressCallback: progressCallback,
		successCallback:  successCallback,
	}
}

func (o *StaleVirtualDrivesOperation) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "StaleVirtualDrives",
		Run:  AsRunFunc(o),
	}
}

func (o *StaleVirtualDrivesOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		// Once the owner is Done (manual op awaiting auto-delete), skip the fleet re-scan entirely.
		// The policy controller resets status to Running each Interval, so this never skips a due run.
		&lifecycle.SimpleStep{
			Name:            "SkipIfDone",
			Run:             func(context.Context) error { return nil },
			Predicates:      lifecycle.Predicates{o.ownerDone},
			FinishOnSuccess: true,
		},
		&lifecycle.SimpleStep{Name: "Scan", Run: o.Scan},
		&lifecycle.SimpleStep{Name: "EvaluateStability", Run: o.EvaluateStability},
		&lifecycle.SimpleStep{Name: "MaybeDelete", Run: o.MaybeDelete},
		&lifecycle.SimpleStep{
			Name:       "SuccessUpdate",
			Run:        o.successCallback,
			Predicates: lifecycle.Predicates{func() bool { return o.successCallback != nil }},
		},
	}
}

// ownerDone reports whether the owner CR is already in the Done state.
func (o *StaleVirtualDrivesOperation) ownerDone() bool {
	switch owner := o.ownerRef.(type) {
	case *weka.WekaManualOperation:
		return owner.Status.Status == "Done"
	case *weka.WekaPolicy:
		return owner.Status.Status == "Done"
	default:
		return false
	}
}

func (o *StaleVirtualDrivesOperation) GetJsonResult() string {
	resultJSON, err := json.Marshal(o.results)
	if err != nil {
		return ""
	}
	return string(resultJSON)
}

// targetProxy pairs an ssdproxy container with its resolved node name.
type targetProxy struct {
	container weka.WekaContainer
	node      weka.NodeName
}

// resolveTargetProxies lists ssdproxy containers in the operator namespace, optionally filtered
// to nodes matching payload.NodeSelector.
func (o *StaleVirtualDrivesOperation) resolveTargetProxies(ctx context.Context) ([]targetProxy, error) {
	operatorNamespace, err := util.GetPodNamespace()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get operator namespace")
	}

	// ssdproxy containers are shared across clusters on a node and live in the operator namespace.
	proxies, err := o.kubeService.GetWekaContainersSimple(ctx, operatorNamespace, "", map[string]string{
		domain.WekaLabelMode: weka.WekaContainerModeSSDProxy,
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list ssdproxy containers")
	}

	// When a NodeSelector is given, restrict to its matching nodes (by node name).
	var nodeFilter map[string]bool
	if len(o.payload.NodeSelector) > 0 {
		nodes, err := o.kubeService.GetNodes(ctx, o.payload.NodeSelector)
		if err != nil {
			return nil, errors.Wrap(err, "failed to list nodes for NodeSelector")
		}
		nodeFilter = make(map[string]bool, len(nodes))
		for i := range nodes {
			nodeFilter[nodes[i].Name] = true
		}
	}

	targets := make([]targetProxy, 0, len(proxies))
	for i := range proxies {
		node := proxies[i].GetNodeAffinity()
		if node == "" {
			continue
		}
		if nodeFilter != nil && !nodeFilter[string(node)] {
			continue
		}
		targets = append(targets, targetProxy{container: proxies[i], node: node})
	}
	return targets, nil
}

// buildClaimedSet returns the set of VirtualUUIDs claimed by any WekaContainer in any state,
// read with the direct (uncached) API reader so a just-written allocation is never missed.
func (o *StaleVirtualDrivesOperation) buildClaimedSet(ctx context.Context) (map[string]bool, error) {
	containerList := &weka.WekaContainerList{}
	if err := o.mgr.GetAPIReader().List(ctx, containerList); err != nil {
		return nil, errors.Wrap(err, "failed to list WekaContainers (uncached)")
	}
	claimed := map[string]bool{}
	for i := range containerList.Items {
		alloc := containerList.Items[i].Status.Allocations
		if alloc == nil {
			continue
		}
		for _, vid := range alloc.GetVirtualDrivesUuids() {
			claimed[vid] = true
		}
	}
	return claimed, nil
}

// buildLiveClusterGUIDs returns the set of cluster GUIDs of all existing WekaCluster CRs
// (including terminating ones — a deleting cluster still owns its VIDs).
func (o *StaleVirtualDrivesOperation) buildLiveClusterGUIDs(ctx context.Context) (map[string]bool, error) {
	clusterList := &weka.WekaClusterList{}
	if err := o.mgr.GetAPIReader().List(ctx, clusterList); err != nil {
		return nil, errors.Wrap(err, "failed to list WekaClusters (uncached)")
	}
	guids := map[string]bool{}
	for i := range clusterList.Items {
		if guid := clusterList.Items[i].Status.ClusterID; guid != "" {
			guids[guid] = true
		}
	}
	return guids, nil
}

// Scan enumerates VIDs on all target proxies, diffs against the claimed set, categorizes the
// stale ones, computes the fingerprint, logs per-VID WARNs, and emits a detection event.
func (o *StaleVirtualDrivesOperation) Scan(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "Scan")
	defer logger.End()

	targets, err := o.resolveTargetProxies(ctx)
	if err != nil {
		o.results.Err = err.Error()
		return err
	}

	claimed, err := o.buildClaimedSet(ctx)
	if err != nil {
		o.results.Err = err.Error()
		return err
	}
	liveGUIDs, err := o.buildLiveClusterGUIDs(ctx)
	if err != nil {
		o.results.Err = err.Error()
		return err
	}

	token, err := o.proxyClient.GetNodeAgentToken(ctx)
	if err != nil {
		o.results.Err = err.Error()
		return errors.Wrap(err, "failed to get node agent token")
	}

	scannedNodes := map[string]bool{}
	o.vidToProxyUID = map[string]string{}
	var scanned []scannedVID
	var scanErrs []string

	for i := range targets {
		target := &targets[i]
		proxyUID := string(target.container.GetUID())
		agentPod, err := o.proxyClient.GetNodeAgentPod(ctx, target.node)
		if err != nil {
			scanErrs = append(scanErrs, fmt.Sprintf("node %s: %v", target.node, err))
			continue
		}
		vids, err := o.proxyClient.ListVirtualDrives(ctx, agentPod, token, proxyUID)
		if err != nil {
			scanErrs = append(scanErrs, fmt.Sprintf("node %s: %v", target.node, err))
			continue
		}
		scannedNodes[string(target.node)] = true
		for _, vid := range vids {
			scanned = append(scanned, scannedVID{node: string(target.node), vd: vid})
			// Remember the exact proxy this VID was seen on, for targeted removal.
			o.vidToProxyUID[vid.VirtualUUID] = proxyUID
		}
	}

	stale := computeStaleVids(scanned, claimed, liveGUIDs, o.payload.OnlyNonExistingClusters)
	for _, info := range stale {
		logger.Warn("Stale virtual drive detected",
			"virtual_uuid", info.VirtualUUID,
			"owner_cluster_guid", info.OwnerClusterGUID,
			"category", info.Category,
			"physical_uuid", info.PhysicalUUID,
			"size_gb", info.SizeGB,
			"node", info.Node,
		)
	}

	var totalGB int
	for _, s := range stale {
		totalGB += s.SizeGB
	}

	o.results.ScannedNodes = len(scannedNodes)
	o.results.StaleVids = stale
	o.results.StaleCount = len(stale)
	o.results.StaleTiB = float64(totalGB) / 1024.0
	o.results.Fingerprint = fingerprintStaleVids(stale)
	o.results.Deleted = nil
	o.results.DeletionEligible = false

	if len(scanErrs) > 0 {
		// A partial view must never drive deletion — record the error and let the gate stay closed.
		o.results.Err = "scan errors: " + strings.Join(scanErrs, "; ")
		logger.Error(nil, "Stale-VID scan had errors; deletion will be skipped this run", "errors", o.results.Err)
	}

	if o.results.StaleCount > 0 && o.recorder != nil {
		o.recorder.Eventf(o.ownerRef, corev1.EventTypeWarning, staleVidsDetectedEventReason,
			"Detected %d stale virtual drive(s) (%.2f TiB) across %d node(s); owner cluster GUIDs: %s",
			o.results.StaleCount, o.results.StaleTiB, o.results.ScannedNodes, strings.Join(distinctOwnerGUIDs(stale), ", "))
	}

	logger.Info("Stale virtual drive scan complete",
		"scanned_nodes", o.results.ScannedNodes,
		"stale_count", o.results.StaleCount,
		"stale_tib", o.results.StaleTiB,
		"fingerprint", o.results.Fingerprint,
	)
	return nil
}

// EvaluateStability compares the current fingerprint to the previous run's (read from the owner
// status) and marks the result deletion-eligible when they match and the set is non-empty.
func (o *StaleVirtualDrivesOperation) EvaluateStability(ctx context.Context) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "EvaluateStability")
	defer logger.End()

	if o.results.Err != "" {
		return nil // partial scan — gate stays closed
	}

	previous := o.previousResult()
	var previousFingerprint string
	if previous != nil {
		previousFingerprint = previous.Fingerprint
	}
	o.results.DeletionEligible = deletionEligible(o.results.StaleCount, o.results.Fingerprint, previousFingerprint)

	logger.Info("Evaluated stale-VID stability gate",
		"current_fingerprint", o.results.Fingerprint,
		"deletion_eligible", o.results.DeletionEligible,
	)
	return nil
}

// MaybeDelete removes stale VIDs only when deletion is enabled, the set is stable across cycles,
// and each VID is still unclaimed at removal time (final uncached re-validation).
func (o *StaleVirtualDrivesOperation) MaybeDelete(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "MaybeDelete")
	defer logger.End()

	if !o.payload.DeleteStaleVids || o.results.StaleCount == 0 || o.results.Err != "" {
		return nil // report-only, nothing to delete, or partial scan
	}

	if !o.results.DeletionEligible {
		// Not yet observed identically twice. For the manual op, persist the current result and
		// requeue so a second cycle can confirm; for the policy, the next Interval run re-confirms.
		if o.progressCallback != nil {
			if err := o.progressCallback(ctx); err != nil {
				return errors.Wrap(err, "failed to persist intermediate stale-VID result")
			}
			return lifecycle.NewWaitErrorWithDuration(
				errors.New("stale virtual drive set not yet stable; re-confirming next cycle"),
				staleVidsWaitDuration,
			)
		}
		return nil
	}

	// Final re-validation: fresh uncached claimed set immediately before removal.
	claimed, err := o.buildClaimedSet(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to re-validate claimed set before removal")
	}

	token, err := o.proxyClient.GetNodeAgentToken(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get node agent token for removal")
	}

	// Resolve a node-agent pod per node once.
	agentPods := map[string]*corev1.Pod{}
	var deleted []string
	var removalErrs []string

	for _, s := range o.results.StaleVids {
		l := logger
		if claimed[s.VirtualUUID] {
			l.Info("Stale VID became claimed since scan; sparing it", "virtual_uuid", s.VirtualUUID)
			continue
		}

		// Target the exact proxy this VID was scanned on (set in Scan).
		proxyUID := o.vidToProxyUID[s.VirtualUUID]
		if proxyUID == "" {
			removalErrs = append(removalErrs, fmt.Sprintf("%s: no scanned proxy UID", s.VirtualUUID))
			continue
		}

		agentPod, ok := agentPods[s.Node]
		if !ok {
			agentPod, err = o.proxyClient.GetNodeAgentPod(ctx, weka.NodeName(s.Node))
			if err != nil {
				removalErrs = append(removalErrs, fmt.Sprintf("%s: node agent pod: %v", s.VirtualUUID, err))
				continue
			}
			agentPods[s.Node] = agentPod
		}

		if err := o.proxyClient.RemoveVirtualDrive(ctx, agentPod, token, proxyUID, s.VirtualUUID); err != nil {
			removalErrs = append(removalErrs, fmt.Sprintf("%s: %v", s.VirtualUUID, err))
			continue
		}

		deleted = append(deleted, s.VirtualUUID)
		l.Warn("Removed stale virtual drive",
			"virtual_uuid", s.VirtualUUID,
			"owner_cluster_guid", s.OwnerClusterGUID,
			"node", s.Node,
			"physical_uuid", s.PhysicalUUID,
		)
		if o.recorder != nil {
			o.recorder.Eventf(o.ownerRef, corev1.EventTypeWarning, staleVidRemovedEventReason,
				"Removed stale virtual drive %s (owner cluster %s, category %s) on node %s",
				s.VirtualUUID, s.OwnerClusterGUID, s.Category, s.Node)
		}
	}

	o.results.Deleted = deleted

	// Per-VID removal failures are recorded (and surfaced via Status + events) but are NOT fatal to
	// the operation: the chain proceeds to SuccessUpdate so the partial Deleted set is persisted.
	// Remaining stale VIDs are re-detected and retried on the next scan.
	if len(removalErrs) > 0 {
		o.results.Err = "removal errors: " + strings.Join(removalErrs, "; ")
		logger.Error(nil, "Some stale virtual drives failed to remove", "errors", o.results.Err)
	}
	return nil
}

// previousResult parses the previous run's JSON result from the owner status.
func (o *StaleVirtualDrivesOperation) previousResult() *weka.StaleVirtualDrivesResult {
	return decodePreviousOwnerResult[weka.StaleVirtualDrivesResult](o.ownerRef)
}

// scannedVID is a virtual drive observed on a proxy together with its node.
type scannedVID struct {
	node string
	vd   ssdproxy.VirtualDrive
}

// computeStaleVids is the pure core of the scan: every scanned VID not in the claimed set is
// stale, categorized dead_cluster (no live WekaCluster owns its GUID) vs live_cluster_unclaimed.
// onlyNonExisting keeps only the dead_cluster subset. Output is sorted by (node, virtualUuid).
func computeStaleVids(scanned []scannedVID, claimed, liveGUIDs map[string]bool, onlyNonExisting bool) []weka.StaleVirtualDriveInfo {
	var stale []weka.StaleVirtualDriveInfo
	for _, s := range scanned {
		if claimed[s.vd.VirtualUUID] {
			continue // in use by a live container allocation
		}
		category := weka.StaleVidCategoryLiveClusterUnclaimed
		if !liveGUIDs[s.vd.ClusterGUID] {
			category = weka.StaleVidCategoryDeadCluster
		}
		if onlyNonExisting && category != weka.StaleVidCategoryDeadCluster {
			continue
		}
		stale = append(stale, weka.StaleVirtualDriveInfo{
			Node:             s.node,
			PhysicalUUID:     s.vd.PhysicalUUID,
			VirtualUUID:      s.vd.VirtualUUID,
			OwnerClusterGUID: s.vd.ClusterGUID,
			SizeGB:           s.vd.SizeGB,
			Category:         category,
		})
	}
	sort.Slice(stale, func(i, j int) bool {
		if stale[i].Node != stale[j].Node {
			return stale[i].Node < stale[j].Node
		}
		return stale[i].VirtualUUID < stale[j].VirtualUUID
	})
	return stale
}

// deletionEligible gates removal: only when the stale set is non-empty and its fingerprint
// matches the previous cycle's (observed identically twice).
func deletionEligible(staleCount int, currentFingerprint, previousFingerprint string) bool {
	return staleCount > 0 && currentFingerprint != "" && currentFingerprint == previousFingerprint
}

func fingerprintStaleVids(stale []weka.StaleVirtualDriveInfo) string {
	if len(stale) == 0 {
		return ""
	}
	h := sha256.New()
	for _, s := range stale {
		// Include the owner GUID so any change of ownership defers deletion for another two cycles.
		h.Write([]byte(s.Node))
		h.Write([]byte("|"))
		h.Write([]byte(s.VirtualUUID))
		h.Write([]byte("|"))
		h.Write([]byte(s.OwnerClusterGUID))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func distinctOwnerGUIDs(stale []weka.StaleVirtualDriveInfo) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range stale {
		if !seen[s.OwnerClusterGUID] {
			seen[s.OwnerClusterGUID] = true
			out = append(out, s.OwnerClusterGUID)
		}
	}
	sort.Strings(out)
	return out
}
