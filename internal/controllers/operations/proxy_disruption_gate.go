package operations

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/internal/services/ssdproxy"
)

// ClusterVerdict is the serializable outcome of the cross-cluster disruption gate for one
// dependent WekaCluster.
type ClusterVerdict struct {
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	ClusterGUID string `json:"clusterGUID"`
	Allowed     bool   `json:"allowed"`
	Reason      string `json:"reason,omitempty"`
}

// EvaluateNodeDisruption answers "may I restart the ssdproxy pod on node N right now, without any
// tenant losing data protection?" for every WekaCluster dependent on that node's proxy (see
// dev_doc/ssdproxy-rotation.md, "L2 — Cross-cluster disruption gate"). Fail-closed: proxy is
// caller-supplied rather than re-resolved, since a by-node lookup could miss a container whose
// Status hasn't caught up and read as "no dependents". One ClusterVerdict is returned per cluster.
func EvaluateNodeDisruption(ctx context.Context, mgr ctrl.Manager, execSvc exec.ExecService, node weka.NodeName, proxy *weka.WekaContainer) ([]ClusterVerdict, error) {
	return evaluateDependents(ctx, mgr, execSvc, node, proxy, "EvaluateNodeDisruption",
		func(ctx context.Context, e *clusterEvaluator, cluster *weka.WekaCluster) ClusterVerdict {
			return e.evaluateClusterWide(ctx, cluster)
		})
}

// VerifyNodeRecovered is the post-rotation counterpart to EvaluateNodeDisruption: it confirms per
// dependent cluster that node's drives are back to ACTIVE and the cluster is fully protected,
// reusing the same per-cluster health checks plus a node-scoped drive check. proxy has the same
// caller-supplied contract as EvaluateNodeDisruption's.
func VerifyNodeRecovered(ctx context.Context, mgr ctrl.Manager, execSvc exec.ExecService, node weka.NodeName, proxy *weka.WekaContainer) ([]ClusterVerdict, error) {
	return evaluateDependents(ctx, mgr, execSvc, node, proxy, "VerifyNodeRecovered",
		func(ctx context.Context, e *clusterEvaluator, cluster *weka.WekaCluster) ClusterVerdict {
			return e.evaluateNodeRecovery(ctx, cluster, node)
		})
}

// evaluateDependents is the shared body of EvaluateNodeDisruption and VerifyNodeRecovered: discover
// every cluster dependent on proxy, then apply perCluster to each in deterministic order. spanName
// names the caller's own trace span/log line.
func evaluateDependents(
	ctx context.Context,
	mgr ctrl.Manager,
	execSvc exec.ExecService,
	node weka.NodeName,
	proxy *weka.WekaContainer,
	spanName string,
	perCluster func(ctx context.Context, e *clusterEvaluator, cluster *weka.WekaCluster) ClusterVerdict,
) ([]ClusterVerdict, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, spanName, "node", node)
	defer logger.End()

	dependents, err := discoverDependentClusters(ctx, mgr, node, proxy)
	if err != nil {
		return nil, errors.Wrap(err, "failed to discover dependent clusters")
	}

	// One evaluator per call — see clusterEvaluator's doc for why its cache must not outlive it.
	evaluator := newClusterEvaluator(mgr, execSvc)
	verdicts := make([]ClusterVerdict, 0, len(dependents))
	for _, cluster := range sortedClusters(dependents) {
		verdicts = append(verdicts, perCluster(ctx, evaluator, cluster))
	}

	logger.Info("Evaluated dependent clusters", "node", node, "dependent_clusters", len(verdicts))
	return verdicts, nil
}

// AllAllowed reports whether every verdict is Allowed, plus every blocker's Reason joined into one
// actionable message.
func AllAllowed(verdicts []ClusterVerdict) (allowed bool, blockReason string) {
	var blockers []string
	for _, v := range verdicts {
		if !v.Allowed {
			blockers = append(blockers, v.Reason)
		}
	}
	if len(blockers) == 0 {
		return true, ""
	}
	return false, strings.Join(blockers, "; ")
}

// discoverDependentClusters unions clusters found via node-locality (source A) and proxy-side
// virtual-drive ownership (source B), keyed by cluster UID; neither alone suffices, so a failure in
// either fails the whole call closed. Lists are read uncached so a fresh allocation isn't missed.
func discoverDependentClusters(ctx context.Context, mgr ctrl.Manager, node weka.NodeName, proxy *weka.WekaContainer) (map[types.UID]*weka.WekaCluster, error) {
	clusterList := &weka.WekaClusterList{}
	if err := mgr.GetAPIReader().List(ctx, clusterList); err != nil {
		return nil, errors.Wrap(err, "failed to list WekaClusters (uncached)")
	}
	byUID := make(map[types.UID]*weka.WekaCluster, len(clusterList.Items))
	byGUID := make(map[string]*weka.WekaCluster, len(clusterList.Items))
	for i := range clusterList.Items {
		c := &clusterList.Items[i]
		byUID[c.UID] = c
		if c.Status.ClusterID != "" {
			byGUID[c.Status.ClusterID] = c
		}
	}

	dependents := map[types.UID]*weka.WekaCluster{}

	// Source A: node-locality, filtered in memory rather than via the status.nodeAffinity field
	// index — the index only covers Status.NodeAffinity, missing a dependent whose Spec is set but
	// Status hasn't caught up (mirrors resolveTargetProxies in stale_virtual_drives.go).
	containerList := &weka.WekaContainerList{}
	if err := mgr.GetAPIReader().List(ctx, containerList); err != nil {
		return nil, errors.Wrap(err, "failed to list WekaContainers (uncached)")
	}
	for i := range containerList.Items {
		c := &containerList.Items[i]
		if !c.UsesDriveSharing() || c.GetNodeAffinity() != node {
			continue
		}
		parentUID := c.GetParentClusterId()
		if parentUID == "" {
			continue
		}
		if cluster, ok := byUID[types.UID(parentUID)]; ok {
			dependents[cluster.UID] = cluster
		}
		// A dangling owner reference (the owning cluster no longer exists) has nothing left for
		// this gate to protect; that is the stale-virtual-drive cleanup path's job, not this one's.
	}

	// Source B: proxy-side truth. Catches a tenant holding virtual drives on this exact proxy whose
	// drive WekaContainer is momentarily absent from source A.
	kubeService := kubernetes.NewKubeService(mgr.GetClient())
	proxyClient := ssdproxy.NewClient(kubeService)
	token, err := proxyClient.GetNodeAgentToken(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get node agent token")
	}
	agentPod, err := proxyClient.GetNodeAgentPod(ctx, node)
	if err != nil {
		// The proxy's node-agent is unreachable, so we cannot see which tenants depend on it —
		// exactly the blindness this gate exists to prevent — so fail the whole call, not just source B.
		return nil, errors.Wrap(err, "failed to reach node agent for ssdproxy virtual-drive listing")
	}
	vids, err := proxyClient.ListVirtualDrives(ctx, agentPod, token, string(proxy.GetUID()))
	if err != nil {
		return nil, errors.Wrap(err, "failed to list virtual drives on ssdproxy")
	}
	for _, vid := range vids {
		if cluster, ok := byGUID[vid.ClusterGUID]; ok {
			dependents[cluster.UID] = cluster
		}
	}

	return dependents, nil
}

// sortedClusters returns the map's values in deterministic (namespace, name) order so persisted
// status.result doesn't reorder from run to run for no reason.
func sortedClusters(m map[types.UID]*weka.WekaCluster) []*weka.WekaCluster {
	out := make([]*weka.WekaCluster, 0, len(m))
	for _, c := range m {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// clusterStatusEntry is the cached outcome of fetching one cluster's weka status: a working
// WekaService (reusable for ListDrives/ListContainerDrives) plus the status, or the fetch error.
type clusterStatusEntry struct {
	wekaService services.WekaService
	status      services.WekaStatusResponse
	// containers is the cluster's full container list fetched to pick an exec target. Retained so
	// nodeDrives can filter it in memory rather than re-Listing the same containers per pass.
	containers []*weka.WekaContainer
	err        error
}

// clusterEvaluator caches the cluster-wide GetWekaStatus fetch per cluster UID for one
// EvaluateNodeDisruption/VerifyNodeRecovered pass, avoiding a wasteful re-exec per node. Never
// reuse across calls — construct a fresh one per pass, or a stale protection reading slips through.
type clusterEvaluator struct {
	mgr     ctrl.Manager
	execSvc exec.ExecService
	cache   map[types.UID]*clusterStatusEntry
}

func newClusterEvaluator(mgr ctrl.Manager, execSvc exec.ExecService) *clusterEvaluator {
	return &clusterEvaluator{mgr: mgr, execSvc: execSvc, cache: map[types.UID]*clusterStatusEntry{}}
}

// statusFor returns (and caches) the weka-status fetch for cluster within this evaluator's pass.
func (e *clusterEvaluator) statusFor(ctx context.Context, cluster *weka.WekaCluster) *clusterStatusEntry {
	if entry, ok := e.cache[cluster.UID]; ok {
		return entry
	}
	entry := e.fetchStatus(ctx, cluster)
	e.cache[cluster.UID] = entry
	return entry
}

// fetchStatus is the single-cluster upgrade gate from funcs_upgrade.go, generalized to any
// dependent cluster rather than the reconciling one.
func (e *clusterEvaluator) fetchStatus(ctx context.Context, cluster *weka.WekaCluster) *clusterStatusEntry {
	// Uncached: a lagging cache can yield candidates == 0 downstream, marking a node verified having
	// checked nothing. Must be NoFieldIndex: the ownerReferences.uid field index only exists on the
	// informer cache, so GetAPIReader() would reject the query and block every node forever.
	containers, err := discovery.GetClusterContainersNoFieldIndex(ctx, e.mgr.GetAPIReader(), cluster, "")
	if err != nil {
		return &clusterStatusEntry{err: errors.Wrap(err, "failed to list cluster containers")}
	}

	// SelectOperationalContainers, not SelectActiveContainer: SelectActiveContainer falls back to a
	// shuffled arbitrary container when none is operational, so a non-nil return would prove
	// nothing about the cluster's health. An empty result here is itself a blocking condition.
	target := discovery.SelectOperationalContainers(containers, 1, nil)
	if len(target) == 0 {
		return &clusterStatusEntry{err: errors.New("no operational container to query")}
	}

	timeout := 30 * time.Second
	wekaService := services.NewWekaServiceWithTimeout(e.execSvc, target[0], &timeout)
	status, err := wekaService.GetWekaStatus(ctx)
	if err != nil {
		// This guard is required, not optional: WekaStatusRebuild.IsFullyProtected() returns true
		// for an empty ProtectionState slice, so a failed or partial fetch would otherwise read as
		// perfectly healthy. This is the single most important fail-closed check in this file.
		return &clusterStatusEntry{err: errors.Wrap(err, "failed to fetch weka status")}
	}
	return &clusterStatusEntry{wekaService: wekaService, status: status, containers: containers}
}

// evaluateClusterWide builds the disruption-gate verdict for one dependent cluster: cluster-wide
// protection, status, moving-data, drive-count, and container-threshold checks. evaluateNodeRecovery
// layers a node-scoped drive check on top of this.
func (e *clusterEvaluator) evaluateClusterWide(ctx context.Context, cluster *weka.WekaCluster) ClusterVerdict {
	verdict := ClusterVerdict{Namespace: cluster.Namespace, Name: cluster.Name, ClusterGUID: cluster.Status.ClusterID}

	entry := e.statusFor(ctx, cluster)
	if entry.err != nil {
		verdict.Reason = fmt.Sprintf("%s: %v", cluster.Name, entry.err)
		return verdict
	}

	// Only fetch per-drive detail when the aggregate count already indicates a problem, since
	// ListDrives is an extra exec call.
	var drives []weka.Drive
	if entry.status.Drives.Active != entry.status.Drives.Total {
		var err error
		drives, err = entry.wekaService.ListDrives(ctx, services.DriveListOptions{})
		if err != nil {
			verdict.Reason = fmt.Sprintf("%s: failed to list drives: %v", cluster.Name, err)
			return verdict
		}
	}

	expectedDrive, expectedCompute := expectedContainers(entry.containers)
	ok, reason := evaluateClusterHealth(&entry.status, drives, expectedDrive, expectedCompute,
		config.Config.Upgrade.DriveThresholdPercent, config.Config.Upgrade.ComputeThresholdPercent)
	verdict.Allowed = ok
	if !ok {
		verdict.Reason = fmt.Sprintf("%s: %s", cluster.Name, reason)
	}
	return verdict
}

// evaluateNodeRecovery builds the post-rotation verdict for one dependent cluster: everything
// evaluateClusterWide checks, plus a node-scoped drive check (that cluster's drives on this
// specific node are back to ACTIVE) in place of relying solely on the cluster-wide drive count.
func (e *clusterEvaluator) evaluateNodeRecovery(ctx context.Context, cluster *weka.WekaCluster, node weka.NodeName) ClusterVerdict {
	verdict := e.evaluateClusterWide(ctx, cluster)

	entry := e.statusFor(ctx, cluster)
	if entry.err != nil {
		return verdict // already reflected in verdict.Reason; no exec target to check further
	}

	nodeDrives, candidates, skipped, err := e.nodeDrives(ctx, entry, node)
	var nodeReason string
	if err != nil {
		nodeReason = fmt.Sprintf("failed to verify drives on node %s: %v", node, err)
	} else {
		nodeReason = nodeDriveVerificationReason(candidates, skipped, nodeDrives, node)
	}

	if nodeReason != "" {
		verdict.Allowed = false
		if verdict.Reason == "" {
			verdict.Reason = fmt.Sprintf("%s: %s", cluster.Name, nodeReason)
		} else {
			verdict.Reason += "; " + nodeReason
		}
	}
	return verdict
}

// nodeDrives resolves cluster's drives on node: drive WekaContainers node-affine to node, mapped to
// live weka-side drives via ListContainerDrives(ClusterContainerID). candidates counts every
// container expected on this node regardless of rejoin state; skipped counts the subset with
// ClusterContainerID == nil (not yet rejoined); skipped <= candidates always holds.
func (e *clusterEvaluator) nodeDrives(ctx context.Context, entry *clusterStatusEntry, node weka.NodeName) (drives []weka.Drive, candidates, skipped int, err error) {
	for _, c := range entry.containers {
		if c.Labels[domain.WekaLabelMode] != weka.WekaContainerModeDrive {
			continue
		}
		if c.GetNodeAffinity() != node {
			continue
		}
		candidates++
		if c.Status.ClusterContainerID == nil {
			// No cluster-side identity yet — typically still rejoining after the very restart this
			// gate is verifying. Counted, not silently dropped.
			skipped++
			continue
		}
		containerDrives, listErr := entry.wekaService.ListContainerDrives(ctx, *c.Status.ClusterContainerID)
		if listErr != nil {
			return nil, 0, 0, errors.Wrapf(listErr, "container %d", *c.Status.ClusterContainerID)
		}
		drives = append(drives, containerDrives...)
	}
	return drives, candidates, skipped, nil
}

// expectedContainers counts the drive and compute WekaContainers the operator believes this cluster
// should have, for use as a threshold denominator. Weka's own reported Total drops when a container
// vanishes from the cluster, which would let a shrunken cluster satisfy the threshold on the
// survivors alone.
func expectedContainers(containers []*weka.WekaContainer) (drive, compute int) {
	for _, c := range containers {
		switch c.Labels[domain.WekaLabelMode] {
		case weka.WekaContainerModeDrive:
			drive++
		case weka.WekaContainerModeCompute:
			compute++
		}
	}
	return drive, compute
}

func countActiveDrives(drives []weka.Drive) int {
	n := 0
	for _, d := range drives {
		if d.Status == services.DriveStatusActive {
			n++
		}
	}
	return n
}

// Pure decision helpers below: plain values in, bool/reason string out, no I/O — table-testable.

// evaluateClusterHealth is the pure combinator over one cluster's weka-status snapshot: every
// sub-check must pass for the cluster to be Allowed. drives is only consulted by the drive-count
// check and may be nil when status.Drives.Active == status.Drives.Total (nothing to explain).
// expectedDrive/expectedCompute come from expectedContainers and back the threshold denominators.
func evaluateClusterHealth(status *services.WekaStatusResponse, drives []weka.Drive, expectedDrive, expectedCompute, driveThresholdPercent, computeThresholdPercent int) (healthy bool, blockReason string) {
	var reasons []string
	for _, r := range []string{
		rebuildProtectionReason(status.Rebuild),
		rebuildMovingDataReason(status.Rebuild),
		clusterStatusReason(status.Status),
		driveHealthReason(status.Drives.Active, status.Drives.Total, drives),
		containerThresholdReason("drive", status.Containers.Drives, expectedDrive, driveThresholdPercent),
		containerThresholdReason("compute", status.Containers.Computes, expectedCompute, computeThresholdPercent),
	} {
		if r != "" {
			reasons = append(reasons, r)
		}
	}
	if len(reasons) == 0 {
		return true, ""
	}
	return false, strings.Join(reasons, "; ")
}

// rebuildProtectionReason reports "" when the cluster is fully protected, else a reason naming the
// worst (highest-percent, at-least-one-failure) protection state.
func rebuildProtectionReason(rebuild services.WekaStatusRebuild) string {
	if rebuild.IsFullyProtected() {
		return ""
	}
	return fmt.Sprintf("rebuild not fully protected (%s)", protectionStateSummary(rebuild.ProtectionState))
}

// rebuildMovingDataReason reports "" unless the cluster is actively rebuilding/moving data, which
// is disqualifying on its own even if IsFullyProtected() happens to still read true.
func rebuildMovingDataReason(rebuild services.WekaStatusRebuild) string {
	if !rebuild.MovingData {
		return ""
	}
	return "rebuild is moving data"
}

func protectionStateSummary(states []services.ProtectionState) string {
	var worst *services.ProtectionState
	for i := range states {
		if states[i].NumFailures <= 0 {
			continue
		}
		if worst == nil || states[i].Percent > worst.Percent {
			worst = &states[i]
		}
	}
	if worst == nil {
		return "unknown protection state"
	}
	return fmt.Sprintf("%d failures @ %.1f%%", worst.NumFailures, worst.Percent)
}

// clusterStatusReason reports "" when status is one of services.HealthyClusterStatuses (the same
// set the single-cluster upgrade gate accepts), else a reason naming the actual status.
func clusterStatusReason(status string) string {
	if slices.Contains(services.HealthyClusterStatuses, status) {
		return ""
	}
	return fmt.Sprintf("weka status is %q (want OK or REDISTRIBUTING)", status)
}

// nodeDriveVerificationReason reports "" only once node's drives are confidently observed ACTIVE:
// skipped > 0 means a container hasn't rejoined, and candidates > 0 with no drives is a discovery
// mismatch. candidates == 0 deliberately stays ALLOWED, not blocked: a live cluster can hold leaked
// virtual drives on this proxy indefinitely, and only StaleVirtualDrivesOperation cleans those up on
// its own schedule — blocking here would hang rotation forever waiting for that cleanup.
func nodeDriveVerificationReason(candidates, skipped int, drives []weka.Drive, node weka.NodeName) string {
	if skipped > 0 {
		return fmt.Sprintf("cannot verify drives on node %s yet: %d drive container(s) not yet joined to the cluster", node, skipped)
	}
	if candidates > 0 && len(drives) == 0 {
		return fmt.Sprintf("cannot verify drives on node %s yet: expected drive containers but observed none", node)
	}
	if r := driveHealthReason(countActiveDrives(drives), len(drives), drives); r != "" {
		return fmt.Sprintf("node %s: %s", node, r)
	}
	return ""
}

// driveHealthReason reports "" when active == total, else a reason naming the count and offending
// serials by status. Deliberately an allow-list (== ACTIVE), not a deny-list: weka's status set is
// open, and a deny-list would score an unforeseen bad status as healthy.
func driveHealthReason(active, total int, drives []weka.Drive) string {
	if active == total {
		return ""
	}
	detail := unhealthyDriveDetail(drives)
	if detail == "" {
		detail = "no drive detail available"
	}
	return fmt.Sprintf("%d drives not ACTIVE (%s)", total-active, detail)
}

// maxUnhealthyDriveSerialsPerStatus bounds unhealthyDriveDetail's output, which reaches
// status.result and event messages — uncapped, a big unhealthy cluster would hit event truncation.
const maxUnhealthyDriveSerialsPerStatus = 10

// unhealthyDriveDetail groups every non-ACTIVE drive by its status and returns e.g.
// "INACTIVE: sn-1234, sn-5678, sn-9012; PHASING_IN: sn-4321", sorted for stable output. Each
// status's serial list is capped at maxUnhealthyDriveSerialsPerStatus, appending "… and N more".
func unhealthyDriveDetail(drives []weka.Drive) string {
	byStatus := map[string][]string{}
	for _, d := range drives {
		if d.Status == services.DriveStatusActive {
			continue
		}
		serial := d.SerialNumber
		if serial == "" {
			serial = d.Uuid
		}
		byStatus[d.Status] = append(byStatus[d.Status], serial)
	}
	if len(byStatus) == 0 {
		return ""
	}
	statuses := make([]string, 0, len(byStatus))
	for s := range byStatus {
		statuses = append(statuses, s)
	}
	sort.Strings(statuses)

	parts := make([]string, 0, len(statuses))
	for _, s := range statuses {
		serials := byStatus[s]
		sort.Strings(serials)
		shown := serials
		var suffix string
		if len(serials) > maxUnhealthyDriveSerialsPerStatus {
			shown = serials[:maxUnhealthyDriveSerialsPerStatus]
			suffix = fmt.Sprintf(", … and %d more", len(serials)-maxUnhealthyDriveSerialsPerStatus)
		}
		parts = append(parts, fmt.Sprintf("%s: %s%s", s, strings.Join(shown, ", "), suffix))
	}
	return strings.Join(parts, "; ")
}

// containerThresholdReason reports "" when counter meets thresholdPercent (via services.MeetsThreshold),
// else a reason naming the observed percentage. kind is "drive" or "compute", used in the reason text.
//
// The denominator is the larger of what weka reports and what the operator expects, so neither a
// container that vanished from the cluster (weka's Total drops) nor a stale operator-side list can
// shrink the bar. Zero on both sides means there is nothing of that kind to threshold against.
func containerThresholdReason(kind string, counter services.WekaStatusObjectCounter, expectedTotal, thresholdPercent int) string {
	total := max(counter.Total, expectedTotal)
	if total == 0 {
		return ""
	}
	if services.MeetsThreshold(counter.Active, total, thresholdPercent) {
		return ""
	}
	pct := float64(counter.Active) / float64(total) * 100
	return fmt.Sprintf("only %.0f%% of %s containers active (threshold %d%%)", pct, kind, thresholdPercent)
}
