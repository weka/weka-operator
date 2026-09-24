package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/internal/services/ssdproxy"
	"github.com/weka/weka-operator/pkg/util"
)

const (
	migrateDriveSharingWaitDuration = 15 * time.Second
	// Window in which a failed child sign operation stays inspectable before it is deleted and retried.
	migrateDriveSharingSignRetryBackoff = time.Minute

	migrateDriveSharingEventReasonStarted              = "DriveSharingMigrationStarted"
	migrateDriveSharingEventReasonBlocked              = "DriveSharingMigrationBlocked"
	migrateDriveSharingEventReasonStalled              = "DriveSharingMigrationStalled"
	migrateDriveSharingEventReasonNodeComplete         = "DriveSharingMigrationNodeComplete"
	migrateDriveSharingEventReasonCampaignComplete     = "DriveSharingMigrationCampaignComplete"
	migrateDriveSharingEventReasonReplacementElsewhere = "DriveSharingMigrationReplacementElsewhere"

	gibBytes = int64(1) << 30
	// Kubernetes object names are capped at 63 characters; a hash replaces the tail of a longer one.
	maxChildOpNameLength = 63
)

// Parked-node warning signals, reusing rotate-ssdproxy's thresholds and throttling: "blocked"
// (Pending, nothing disrupted yet) warns later and repeats less often than "stalled" (InFlight,
// a drive container is already out of the cluster).
var (
	migrateBlockedWarnSignal = parkedWarnSignal{
		eventReason: migrateDriveSharingEventReasonBlocked,
		description: "blocked at the pre-drain gate",
		threshold:   rotateSsdProxyBlockedWarnThreshold,
		repeat:      rotateSsdProxyBlockedWarnRepeat,
	}
	migrateStalledWarnSignal = parkedWarnSignal{
		eventReason: migrateDriveSharingEventReasonStalled,
		description: "stalled in-flight",
		threshold:   rotateSsdProxyStuckWarnThreshold,
		repeat:      rotateSsdProxyStuckWarnRepeat,
	}
	migrateCampaignParkedWarnSignal = parkedWarnSignal{
		eventReason: migrateDriveSharingEventReasonBlocked,
		description: "blocked before any drive container could be targeted",
		threshold:   rotateSsdProxyBlockedWarnThreshold,
		repeat:      rotateSsdProxyBlockedWarnRepeat,
	}
)

// MigrateToDriveSharingOperation converts one WekaCluster's exclusive (full-drives) drive
// containers to drive sharing, one container at a time: gate, drain, re-sign the freed drives for
// the proxy, restart the proxy if it cannot see them, wait for the replacement to rejoin. Like
// rotate-ssdproxy it parks rather than failing, and its whole state lives in status.result so a
// restarted operator resumes mid-campaign. See doc/operator/operations/migrate-to-drive-sharing.md.
type MigrateToDriveSharingOperation struct {
	mgr         ctrl.Manager
	client      client.Client
	kubeService kubernetes.KubeService
	execSvc     exec.ExecService
	payload     *weka.MigrateToDriveSharingPayload
	ownerRef    client.Object
	recorder    events.EventRecorder

	// cluster is resolved by Plan and consumed by AdvanceOne/finalize.
	cluster *weka.WekaCluster

	// evaluator caches the weka-status fetch for this reconcile pass. The operation object is
	// rebuilt every reconcile, which is exactly the "one pass" lifetime clusterEvaluator requires.
	evaluator *clusterEvaluator

	// Injectable seams. Every one of them execs into a live container or calls the node agent over
	// HTTP, none of which is reachable through controller-runtime fakes; tests replace them.
	gate            func(ctx context.Context, cluster *weka.WekaCluster, node weka.NodeName) ClusterVerdict
	wekaStatus      func(ctx context.Context, cluster *weka.WekaCluster) (services.WekaStatusResponse, error)
	containerDrives func(ctx context.Context, cluster *weka.WekaCluster, containerID int) ([]weka.Drive, error)
	proxyGate       func(ctx context.Context, mgr ctrl.Manager, execSvc exec.ExecService, node weka.NodeName, proxy *weka.WekaContainer) ([]ClusterVerdict, error)
	proxyRecovered  func(ctx context.Context, mgr ctrl.Manager, execSvc exec.ExecService, node weka.NodeName, proxy *weka.WekaContainer) ([]ClusterVerdict, error)
	proxyDrives     func(ctx context.Context, node weka.NodeName, proxy *weka.WekaContainer) ([]ssdproxy.PhysicalDrive, error)

	results MigrateToDriveSharingResult

	// progressCallback persists the current result without completing; required since this
	// operation parks rather than finishing on every non-terminal step.
	progressCallback lifecycle.StepFunc
	successCallback  lifecycle.StepFunc
	failureCallback  lifecycle.StepFunc
}

// NewMigrateToDriveSharingOperation builds the migrate-to-drive-sharing operation. execSvc is
// required to exec into a live container for the cluster health and capacity readings.
func NewMigrateToDriveSharingOperation(
	mgr ctrl.Manager,
	execSvc exec.ExecService,
	payload *weka.MigrateToDriveSharingPayload,
	ownerRef client.Object,
	recorder events.EventRecorder,
	progressCallback lifecycle.StepFunc,
	successCallback lifecycle.StepFunc,
	failureCallback lifecycle.StepFunc,
) *MigrateToDriveSharingOperation {
	kclient := mgr.GetClient()
	if payload == nil {
		payload = &weka.MigrateToDriveSharingPayload{}
	}
	o := &MigrateToDriveSharingOperation{
		mgr:              mgr,
		client:           kclient,
		kubeService:      kubernetes.NewKubeService(kclient),
		execSvc:          execSvc,
		payload:          payload,
		ownerRef:         ownerRef,
		recorder:         recorder,
		proxyGate:        EvaluateNodeDisruption,
		proxyRecovered:   VerifyNodeRecovered,
		progressCallback: progressCallback,
		successCallback:  successCallback,
		failureCallback:  failureCallback,
	}
	o.gate = o.defaultGate
	o.wekaStatus = o.defaultWekaStatus
	o.containerDrives = o.defaultContainerDrives
	o.proxyDrives = o.defaultProxyDrives
	return o
}

func (o *MigrateToDriveSharingOperation) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "MigrateToDriveSharing",
		Run:  AsRunFunc(o),
	}
}

func (o *MigrateToDriveSharingOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		// Skip re-entering the state machine once the owner is terminal, so a failed campaign does
		// not re-run Plan -> failTerminally every reconcile forever.
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

func (o *MigrateToDriveSharingOperation) GetJsonResult() string {
	resultJSON, err := json.Marshal(o.results)
	if err != nil {
		return ""
	}
	return string(resultJSON)
}

// finalize emits the campaign-completion event on both the operation and the cluster, drops the
// migration annotation (the validator exception is no longer needed), then marks the owner Done.
func (o *MigrateToDriveSharingOperation) finalize(ctx context.Context) error {
	message := fmt.Sprintf("drive-sharing migration complete for cluster %s: %d of %d drive containers",
		o.results.Cluster, o.results.Done, o.results.Total)
	o.recordOnOpAndCluster(corev1.EventTypeNormal, migrateDriveSharingEventReasonCampaignComplete, message)

	// The annotation is removed before Done is written: a terminal operation is never reconciled
	// again, so a removal attempted after Done would not be retried. A crash between the two writes
	// is safe — requireMigrationSpec lets an all-Done campaign re-plan without the annotation.
	if err := o.removeMigrationAnnotation(ctx); err != nil {
		return err
	}
	return o.successCallback(ctx)
}

// removeMigrationAnnotation clears weka.io/sizing-mode-migration from the target cluster. A JSON
// merge patch with a null value is the only way to delete a single annotation without reading and
// rewriting the whole map.
func (o *MigrateToDriveSharingOperation) removeMigrationAnnotation(ctx context.Context) error {
	if o.cluster == nil || o.cluster.Annotations[consts.AnnotationSizingModeMigration] == "" {
		return nil
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:null}}}`, consts.AnnotationSizingModeMigration)
	if err := o.client.Patch(ctx, o.cluster, client.RawPatch(types.MergePatchType, []byte(patch))); err != nil {
		return errors.Wrapf(err, "failed to remove annotation %s from cluster %s", consts.AnnotationSizingModeMigration, o.results.Cluster)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Default (non-test) implementations of the injectable seams
// ---------------------------------------------------------------------------

// eval lazily creates and returns the shared cluster evaluator.
func (o *MigrateToDriveSharingOperation) eval() *clusterEvaluator {
	if o.evaluator == nil {
		o.evaluator = newClusterEvaluator(o.mgr, o.execSvc)
	}
	return o.evaluator
}

func (o *MigrateToDriveSharingOperation) clusterEntry(ctx context.Context, cluster *weka.WekaCluster) *clusterStatusEntry {
	return o.eval().statusFor(ctx, cluster)
}

// defaultGate runs the per-cluster half of the disruption gate. With node set it additionally
// requires that cluster's drives on that node to be back ACTIVE.
func (o *MigrateToDriveSharingOperation) defaultGate(ctx context.Context, cluster *weka.WekaCluster, node weka.NodeName) ClusterVerdict {
	if node == "" {
		return o.eval().evaluateClusterWide(ctx, cluster)
	}
	return o.eval().evaluateNodeRecovery(ctx, cluster, node)
}

func (o *MigrateToDriveSharingOperation) defaultWekaStatus(ctx context.Context, cluster *weka.WekaCluster) (services.WekaStatusResponse, error) {
	entry := o.clusterEntry(ctx, cluster)
	if entry.err != nil {
		return services.WekaStatusResponse{}, entry.err
	}
	return entry.status, nil
}

func (o *MigrateToDriveSharingOperation) defaultContainerDrives(ctx context.Context, cluster *weka.WekaCluster, containerID int) ([]weka.Drive, error) {
	entry := o.clusterEntry(ctx, cluster)
	if entry.err != nil {
		return nil, entry.err
	}
	return entry.wekaService.ListContainerDrives(ctx, containerID)
}

func (o *MigrateToDriveSharingOperation) defaultProxyDrives(ctx context.Context, node weka.NodeName, proxy *weka.WekaContainer) ([]ssdproxy.PhysicalDrive, error) {
	proxyClient := ssdproxy.NewClient(o.kubeService)
	token, err := proxyClient.GetNodeAgentToken(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get node agent token")
	}
	agentPod, err := proxyClient.GetNodeAgentPod(ctx, node)
	if err != nil {
		return nil, errors.Wrap(err, "failed to reach node agent")
	}
	return proxyClient.ListPhysicalDrives(ctx, agentPod, token, string(proxy.GetUID()))
}

// ---------------------------------------------------------------------------
// Plan
// ---------------------------------------------------------------------------

// Plan rehydrates prior campaign state first (every return path below persists o.results, and an
// empty one would wipe the recorded serials of an in-flight container), then resolves the cluster,
// refuses a second concurrent campaign, checks the spec flip, discovers targets, and runs the
// policy and capacity guards. It never advances a container — that is AdvanceOne's job.
func (o *MigrateToDriveSharingOperation) Plan(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "Plan")
	defer logger.End()

	o.rehydrateFrom(o.previousResult())

	if o.payload.Cluster.Name == "" {
		// The one terminal error: only editing the CR resolves it, never waiting.
		return o.failTerminally(ctx, errors.New(
			"spec.payload.migrateToDriveSharingPayload.cluster.name is required"))
	}
	namespace := o.payload.Cluster.Namespace
	o.results.Cluster = namespace + "/" + o.payload.Cluster.Name

	cluster := &weka.WekaCluster{}
	if err := o.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: o.payload.Cluster.Name}, cluster); err != nil {
		return o.waitWithPersistedErr(ctx, errors.Wrapf(err, "failed to get WekaCluster %s", o.results.Cluster))
	}
	o.cluster = cluster

	if err := o.refuseIfAnotherCampaignRunning(ctx); err != nil {
		return o.waitWithPersistedErr(ctx, err)
	}
	if err := o.requireMigrationSpec(cluster); err != nil {
		return o.waitWithPersistedErr(ctx, err)
	}

	exclusive, sharing, err := o.discoverTargets(ctx, cluster)
	if err != nil {
		return o.waitWithPersistedErr(ctx, err)
	}
	o.results.Containers = planMigrationContainers(o.results.Containers, exclusive, sharing)
	o.results.Total = len(o.results.Containers)
	if o.results.Total == 0 {
		return o.waitWithPersistedErr(ctx, fmt.Errorf(
			"no drive containers of %s match the payload nodeSelector; fix the selector or delete this operation",
			o.results.Cluster))
	}
	o.results.Done = countMigrationDoneOrSkipped(o.results.Containers)

	pending := pendingNodes(o.results.Containers)
	for i := range o.results.Containers {
		state := &o.results.Containers[i]
		if state.Phase == MigrateDriveSharingPhasePending && state.Node == "" {
			return o.waitWithPersistedErr(ctx, fmt.Errorf(
				"drive container %s has no resolvable node; it is probably unscheduled — wait for it to "+
					"be placed, or exclude its node with the payload's nodeSelector", state.Container))
		}
	}

	if err := o.refuseIfSignPolicyMatches(ctx, pending); err != nil {
		return o.waitWithPersistedErr(ctx, err)
	}
	if err := o.checkCapacity(ctx, cluster); err != nil {
		return o.waitWithPersistedErr(ctx, err)
	}

	o.results.Err = ""
	// Mirrors clearing Err: a resolved block must not leave a stale timestamp for a later,
	// unrelated block to inherit.
	o.results.BlockedSince = nil

	logger.Info("Planned drive-sharing migration",
		"cluster", o.results.Cluster, "total", o.results.Total, "done_or_skipped", o.results.Done)
	return nil
}

// rehydrateFrom restores prior campaign state into o.results before Plan's resolution runs, so
// every one of Plan's early-return paths persists real history instead of a zeroed result.
func (o *MigrateToDriveSharingOperation) rehydrateFrom(previous *MigrateToDriveSharingResult) {
	if previous == nil {
		return
	}
	o.results = *previous
}

func (o *MigrateToDriveSharingOperation) previousResult() *MigrateToDriveSharingResult {
	return decodePreviousOwnerResult[MigrateToDriveSharingResult](o.ownerRef)
}

// requireMigrationSpec checks the user has actually flipped the cluster spec and opted in with the
// migration annotation. Both are the user's own edit, so the message says exactly what to apply.
func (o *MigrateToDriveSharingOperation) requireMigrationSpec(cluster *weka.WekaCluster) error {
	// A completed campaign has already dropped the annotation; re-planning it must not park.
	if len(o.results.Containers) > 0 && countMigrationDoneOrSkipped(o.results.Containers) == len(o.results.Containers) {
		return nil
	}
	if !cluster.IsDriveSharing() || cluster.Spec.Dynamic.UsesClusterCapacity() {
		return fmt.Errorf(
			"cluster %s is not in drive-sharing mode: edit spec.dynamicTemplate to drop the full-drives "+
				"sizing and set containerCapacity (or numDrives + driveCapacity), and set annotation "+
				"%s: %s in the same apply. clusterCapacity is not a supported migration target — switch to "+
				"it after the migration completes",
			o.results.Cluster, consts.AnnotationSizingModeMigration, consts.SizingModeMigrationDriveSharing)
	}
	if cluster.Annotations[consts.AnnotationSizingModeMigration] != consts.SizingModeMigrationDriveSharing {
		return fmt.Errorf(
			"cluster %s is missing annotation %s: %s; set it to confirm the sizing-mode migration",
			o.results.Cluster, consts.AnnotationSizingModeMigration, consts.SizingModeMigrationDriveSharing)
	}
	return nil
}

// migrationTarget is one of the cluster's drive containers with its resolved node.
type migrationTarget struct {
	name string
	node weka.NodeName
}

// discoverTargets splits the cluster's drive containers into the exclusive ones to migrate
// (filtered by the payload's nodeSelector) and the sharing ones, which are already done.
func (o *MigrateToDriveSharingOperation) discoverTargets(ctx context.Context, cluster *weka.WekaCluster) (exclusive, sharing []migrationTarget, err error) {
	containers, err := o.listDriveContainers(ctx, cluster)
	if err != nil {
		return nil, nil, err
	}

	var nodeFilter map[string]bool
	if len(o.payload.NodeSelector) > 0 {
		nodes, err := o.kubeService.GetNodes(ctx, o.payload.NodeSelector)
		if err != nil {
			return nil, nil, errors.Wrap(err, "failed to list nodes for nodeSelector")
		}
		nodeFilter = make(map[string]bool, len(nodes))
		for i := range nodes {
			nodeFilter[nodes[i].Name] = true
		}
	}

	for i := range containers {
		c := &containers[i]
		target := migrationTarget{name: c.Name, node: c.GetNodeAffinity()}
		// Filtered before the split: a sharing container on an unselected node must not count toward
		// Total, or a selector matching no exclusive container would complete the campaign.
		if nodeFilter != nil && !nodeFilter[string(target.node)] {
			continue
		}
		if c.UsesDriveSharing() {
			sharing = append(sharing, target)
			continue
		}
		exclusive = append(exclusive, target)
	}
	return exclusive, sharing, nil
}

func (o *MigrateToDriveSharingOperation) listDriveContainers(ctx context.Context, cluster *weka.WekaCluster) ([]weka.WekaContainer, error) {
	containers, err := o.kubeService.GetWekaContainersSimple(ctx, cluster.Namespace, "", map[string]string{
		domain.WekaLabelClusterId: string(cluster.UID),
		domain.WekaLabelMode:      weka.WekaContainerModeDrive,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list drive containers of cluster %s", cluster.Name)
	}
	return containers, nil
}

// planMigrationContainers builds the campaign set on the first cycle and only refreshes it
// afterwards. The set is frozen because a sharing drive container observed later is this
// campaign's own replacement, not a new target — nothing can legitimately join the campaign once
// the spec is flipped.
func planMigrationContainers(previous []MigrateToDriveSharingContainerState, exclusive, sharing []migrationTarget) []MigrateToDriveSharingContainerState {
	if len(previous) == 0 {
		states := make([]MigrateToDriveSharingContainerState, 0, len(exclusive)+len(sharing))
		for _, t := range exclusive {
			states = append(states, MigrateToDriveSharingContainerState{
				Node: t.node, Container: t.name, Phase: MigrateDriveSharingPhasePending,
			})
		}
		for _, t := range sharing {
			states = append(states, MigrateToDriveSharingContainerState{
				Node: t.node, Container: t.name, Phase: MigrateDriveSharingPhaseSkipped,
				Reason: "already uses drive sharing",
			})
		}
		sortMigrationStates(states)
		return states
	}

	live := make(map[string]migrationTarget, len(exclusive))
	for _, t := range exclusive {
		live[t.name] = t
	}
	out := make([]MigrateToDriveSharingContainerState, 0, len(previous))
	for i := range previous {
		state := previous[i]
		if state.Phase == MigrateDriveSharingPhasePending {
			target, stillTargeted := live[state.Container]
			if !stillTargeted {
				// Deleted by hand, or converted by hand: either way there is nothing left to drain.
				state.Phase = MigrateDriveSharingPhaseSkipped
				state.Reason = "drive container is gone or no longer exclusive; nothing to migrate"
			} else {
				state.Node = target.node
			}
		}
		out = append(out, state)
	}
	sortMigrationStates(out)
	return out
}

// sortMigrationStates orders by node then container name so "first Pending" is deterministic
// across resumes.
func sortMigrationStates(states []MigrateToDriveSharingContainerState) {
	sort.Slice(states, func(i, j int) bool {
		if states[i].Node != states[j].Node {
			return states[i].Node < states[j].Node
		}
		return states[i].Container < states[j].Container
	})
}

func countMigrationDoneOrSkipped(states []MigrateToDriveSharingContainerState) int {
	n := 0
	for i := range states {
		if states[i].Phase == MigrateDriveSharingPhaseDone || states[i].Phase == MigrateDriveSharingPhaseSkipped {
			n++
		}
	}
	return n
}

func indexOfMigrationPhase(states []MigrateToDriveSharingContainerState, phase string) int {
	return slices.IndexFunc(states, func(s MigrateToDriveSharingContainerState) bool { return s.Phase == phase })
}

// pendingNodes returns the distinct nodes of containers not yet migrated — the ones a sign-drives
// policy could still spoil.
func pendingNodes(states []MigrateToDriveSharingContainerState) []weka.NodeName {
	seen := map[weka.NodeName]bool{}
	var nodes []weka.NodeName
	for i := range states {
		s := &states[i]
		if s.Phase != MigrateDriveSharingPhasePending && s.Phase != MigrateDriveSharingPhaseInFlight {
			continue
		}
		if s.Node == "" || seen[s.Node] {
			continue
		}
		seen[s.Node] = true
		nodes = append(nodes, s.Node)
	}
	return nodes
}

// refuseIfAnotherCampaignRunning enforces cross-campaign exclusion: at most one non-terminal
// migrate-to-drive-sharing operation may exist. Nodes and proxies are shared between clusters, so
// two campaigns could drain the same node's drives at once; both are refused rather than one
// arbitrarily winning.
func (o *MigrateToDriveSharingOperation) refuseIfAnotherCampaignRunning(ctx context.Context) error {
	// Uncached read: a second campaign created moments ago must never be missed due to cache lag.
	var list weka.WekaManualOperationList
	if err := o.mgr.GetAPIReader().List(ctx, &list); err != nil {
		return errors.Wrap(err, "failed to list WekaManualOperations for cross-campaign exclusion")
	}
	for i := range list.Items {
		other := &list.Items[i]
		if other.UID == o.ownerRef.GetUID() {
			continue
		}
		if other.Spec.Action != weka.WekaManualOperationActionMigrateToDriveSharing {
			continue
		}
		if ownerDone(other) || ownerFailed(other) || other.DeletionTimestamp != nil {
			continue
		}
		return fmt.Errorf(
			"another migrate-to-drive-sharing operation (%s/%s) exists and has not finished; only one "+
				"may run at a time — delete one of them to continue", other.Namespace, other.Name)
	}
	return nil
}

// refuseIfSignPolicyMatches parks the campaign while any exclusive sign-drives WekaPolicy still
// selects a node we are about to migrate: between dropping the serials from weka-full-drives and the
// shared re-sign, such a policy would claim them back as exclusive. Shared policies are harmless
// here and are the documented follow-up between tenants.
func (o *MigrateToDriveSharingOperation) refuseIfSignPolicyMatches(ctx context.Context, nodes []weka.NodeName) error {
	if len(nodes) == 0 {
		return nil
	}
	var policies weka.WekaPolicyList
	if err := o.client.List(ctx, &policies); err != nil {
		return errors.Wrap(err, "failed to list WekaPolicies for the sign-drives guard")
	}

	nodeLabels := make(map[weka.NodeName]labels.Set, len(nodes))
	for _, name := range nodes {
		node := &corev1.Node{}
		if err := o.client.Get(ctx, client.ObjectKey{Name: string(name)}, node); err != nil {
			return errors.Wrapf(err, "failed to get node %s for the sign-drives guard", name)
		}
		nodeLabels[name] = labels.Set(node.Labels)
	}

	for i := range policies.Items {
		policy := &policies.Items[i]
		if policy.Spec.Type != weka.WekaPolicyTypeSignDrives || policy.Spec.Payload.SignDrives == nil ||
			policy.Spec.Payload.SignDrives.Shared {
			continue
		}
		selector := labels.SelectorFromSet(labels.Set(policy.Spec.Payload.SignDrives.NodeSelector))
		for _, name := range nodes {
			if selector.Matches(nodeLabels[name]) {
				return fmt.Errorf(
					"sign-drives WekaPolicy %s/%s selects node %s, which is still to be migrated; delete or "+
						"narrow it before migrating, otherwise it re-signs the freed drives as exclusive",
					policy.Namespace, policy.Name, name)
			}
		}
	}
	return nil
}

// checkCapacity is the one-off sanity check that the post-migration sizing still holds what the
// cluster has provisioned. It re-runs until it passes, then latches.
func (o *MigrateToDriveSharingOperation) checkCapacity(ctx context.Context, cluster *weka.WekaCluster) error {
	if o.results.CapacityChecked {
		return nil
	}
	status, err := o.wekaStatus(ctx, cluster)
	if err != nil {
		return errors.Wrap(err, "failed to fetch weka status for the capacity sanity check")
	}
	// Without a stripe width the usable->raw inflation is skipped, which would understate provisioned
	// capacity and let an undersized flip through.
	if status.StripeWidth <= 0 {
		return errors.New("weka status reports no stripe width; cannot convert provisioned capacity to raw")
	}

	// containerCapacity and driveCapacity are RAW drive capacity while weka reports USABLE, so both
	// figures are inflated to raw before being compared with the flipped sizing.
	newTotal := migrationNewTotalBytes(cluster.Spec.Dynamic)
	provisioned := rawBytesFromUsable(status.Capacity.TotalBytes-status.Capacity.UnprovisionedBytes, &status)
	oldTotal := rawBytesFromUsable(status.Capacity.TotalBytes, &status)
	o.results.OldTotalBytes = oldTotal
	o.results.NewTotalBytes = newTotal
	o.results.ProvisionedBytes = provisioned

	if newTotal < provisioned {
		return fmt.Errorf(
			"the flipped sizing yields %d raw bytes of total capacity, less than the %d raw bytes already "+
				"provisioned; raise spec.dynamicTemplate.containerCapacity (or numDrives x driveCapacity) "+
				"before migrating", newTotal, provisioned)
	}
	o.results.CapacityChecked = true
	if newTotal < oldTotal {
		util.RecordEvent(o.recorder, o.ownerRef, corev1.EventTypeWarning, migrateDriveSharingEventReasonBlocked,
			consts.ActionMigrateToDriveSharing, fmt.Sprintf(
				"drive-sharing migration of cluster %s shrinks total raw capacity from %d to %d bytes; it still "+
					"covers the %d raw bytes provisioned today", o.results.Cluster, oldTotal, newTotal, provisioned))
	}
	return nil
}

// rawBytesFromUsable inflates a usable-capacity reading from weka status to raw drive capacity,
// using the cluster's own live stripe width, protection level and hot spare. A status that does not
// report a stripe width yields the usable figure unchanged — a lower bound, so callers comparing
// against provisioned capacity must reject that case first.
func rawBytesFromUsable(usableBytes int64, status *services.WekaStatusResponse) int64 {
	raw := capacityplanner.RawCapacityGiB(int(usableBytes/gibBytes), status.StripeWidth, status.RedundancyLevel, status.HotSpare)
	if raw == 0 {
		return usableBytes
	}
	return int64(raw) * gibBytes
}

// migrationNewTotalBytes is the total capacity the cluster will have once every drive container is
// sharing: one container's capacity times the container count.
func migrationNewTotalBytes(template *weka.WekaClusterTemplate) int64 {
	if template == nil {
		return 0
	}
	perContainerGiB := template.ContainerCapacity
	if perContainerGiB == 0 {
		perContainerGiB = template.NumDrives * template.DriveCapacity
	}
	return int64(template.DriveContainers) * int64(perContainerGiB) * gibBytes
}

// ---------------------------------------------------------------------------
// AdvanceOne
// ---------------------------------------------------------------------------

// AdvanceOne is the migration state machine. At most one drive container is touched per reconcile:
// an in-flight one is advanced through its sub-phases, otherwise the first Pending one is gated and
// started. Parking returns a WaitErrorWithDuration — "not yet done, requeue" — never a Failed phase.
func (o *MigrateToDriveSharingOperation) AdvanceOne(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "AdvanceOne")
	defer logger.End()

	if idx := indexOfMigrationPhase(o.results.Containers, MigrateDriveSharingPhaseInFlight); idx >= 0 {
		return o.advanceInFlight(ctx, idx)
	}

	if o.payload.Paused {
		o.results.Current = ""
		if err := o.persist(ctx); err != nil {
			return err
		}
		return lifecycle.NewWaitErrorWithDuration(errors.New("migration paused"), migrateDriveSharingWaitDuration)
	}

	idx := indexOfMigrationPhase(o.results.Containers, MigrateDriveSharingPhasePending)
	if idx < 0 {
		o.results.Current = ""
		logger.Info("Drive-sharing migration complete", "cluster", o.results.Cluster, "total", o.results.Total)
		return nil
	}
	return o.startPending(ctx, idx)
}

// startPending gates the container at containers[idx] (which must be Pending), records the drive
// inventory that only exists while the container does, and hands it to the Draining sub-phase.
func (o *MigrateToDriveSharingOperation) startPending(ctx context.Context, idx int) error {
	state := &o.results.Containers[idx]
	o.results.Current = string(state.Node)
	state.SubPhase = MigrateDriveSharingSubPhaseGate
	logger := instrumentation.CurrentSpanLogger(ctx)

	container := &weka.WekaContainer{}
	if err := o.client.Get(ctx, client.ObjectKey{Namespace: o.cluster.Namespace, Name: state.Container}, container); err != nil {
		return o.parkOnErr(ctx, state, "get drive container", err)
	}
	// Recorded before the first mutation: the container is deleted moments later and is the only
	// place its serials, drive UUIDs and capacity exist.
	if err := o.recordDriveInventory(ctx, container, state); err != nil {
		return o.parkOnErr(ctx, state, "record drive inventory", err)
	}

	verdict := o.gate(ctx, o.cluster, "")
	if !verdict.Allowed {
		state.Reason = verdict.Reason
		o.results.Blocked = []ClusterVerdict{verdict}
		return o.parkNode(ctx, state)
	}

	status, err := o.wekaStatus(ctx, o.cluster)
	if err != nil {
		return o.parkOnErr(ctx, state, "fetch weka status for capacity headroom", err)
	}
	// state.CapacityBytes is the container's raw drive capacity, so the usable headroom weka
	// reports is inflated to raw before the comparison.
	headroom := rawBytesFromUsable(status.Capacity.UnprovisionedBytes, &status)
	if headroom < state.CapacityBytes {
		state.Reason = fmt.Sprintf(
			"not enough headroom to phase out %s: %d raw bytes unprovisioned, %d needed for its drives",
			state.Container, headroom, state.CapacityBytes)
		return o.parkNode(ctx, state)
	}
	o.results.Blocked = nil

	now := metav1.Now()
	state.StartedAt = &now
	// Cleared here, not left alone: the stalled timer must measure from StartedAt with no block residue.
	state.BlockedSince = nil
	state.Phase = MigrateDriveSharingPhaseInFlight
	state.SubPhase = MigrateDriveSharingSubPhaseDraining
	state.Reason = ""

	// Intent before action: persist InFlight before the drain below, so a crash between the two
	// leaves a resumable container instead of a Pending one that lost its recorded serials.
	if err := o.persist(ctx); err != nil {
		return err
	}

	util.RecordEvent(o.recorder, o.ownerRef, corev1.EventTypeNormal, migrateDriveSharingEventReasonStarted,
		consts.ActionMigrateToDriveSharing, fmt.Sprintf(
			"Started drive-sharing migration of %s on node %s (%d/%d complete): %d drives, %d bytes",
			state.Container, state.Node, o.results.Done, o.results.Total, len(state.Serials), state.CapacityBytes))
	logger.Info("Started drive-sharing migration for container",
		"container", state.Container, "node", state.Node, "serials", len(state.Serials))

	return lifecycle.NewWaitErrorWithDuration(
		errors.New("migration started, draining the drive container"), migrateDriveSharingWaitDuration)
}

// recordDriveInventory stamps the container's drive serials, weka drive UUIDs and total capacity
// into the campaign state. Capacity comes from weka when the container has joined the cluster,
// otherwise from the node's full-drives annotation; the source is recorded since the two can differ.
// Everything is computed into locals first and the state fields set only once every fallible call
// has succeeded, so a failure midway (or zero final capacity) never persists a half-filled record
// that the len(state.Serials) > 0 guard above would then treat as already done.
func (o *MigrateToDriveSharingOperation) recordDriveInventory(ctx context.Context, container *weka.WekaContainer, state *MigrateToDriveSharingContainerState) error {
	if len(state.Serials) > 0 {
		return nil
	}
	if container.Status.Allocations == nil || len(container.Status.Allocations.Drives) == 0 {
		return errors.New("drive container has no allocated drive serials in status.allocations.drives")
	}
	serials := append([]string(nil), container.Status.Allocations.Drives...)

	var uuids []string
	var capacityBytes int64
	var source string

	if container.Status.ClusterContainerID != nil {
		drives, err := o.containerDrives(ctx, o.cluster, *container.Status.ClusterContainerID)
		if err != nil {
			return errors.Wrapf(err, "failed to list drives of weka container %d", *container.Status.ClusterContainerID)
		}
		for _, d := range drives {
			uuids = append(uuids, d.Uuid)
			capacityBytes += d.SizeBytes
		}
		if capacityBytes > 0 {
			source = MigrateDriveSharingCapacityFromWeka
		}
	}

	if capacityBytes == 0 {
		annotated, err := o.annotatedCapacityBytes(ctx, state.Node, serials)
		if err != nil {
			return err
		}
		capacityBytes = annotated
		source = MigrateDriveSharingCapacityFromAnnotation
	}
	if capacityBytes == 0 {
		return errors.Errorf("could not determine drive capacity for %s from weka or %s on node %s",
			container.Name, consts.AnnotationWekaFullDrives, state.Node)
	}

	state.Serials = serials
	state.DriveUuids = uuids
	state.CapacityBytes = capacityBytes
	state.CapacitySource = source
	return nil
}

// annotatedCapacityBytes sums capacity_gib of the given serials in the node's weka-full-drives
// annotation — the fallback when weka has no drive sizes for the container. Every serial must be
// listed with a size, or the headroom gate would undercount what the drain removes.
func (o *MigrateToDriveSharingOperation) annotatedCapacityBytes(ctx context.Context, nodeName weka.NodeName, serials []string) (int64, error) {
	node := &corev1.Node{}
	if err := o.client.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node); err != nil {
		return 0, errors.Wrapf(err, "failed to get node %s", nodeName)
	}
	entries, err := domain.ReadDriveAnnotations(node.Annotations[consts.AnnotationWekaFullDrives])
	if err != nil {
		return 0, errors.Wrapf(err, "failed to read %s on node %s", consts.AnnotationWekaFullDrives, nodeName)
	}
	wanted := make(map[string]bool, len(serials))
	for _, s := range serials {
		wanted[s] = true
	}
	var total int64
	for _, e := range entries {
		if wanted[e.Serial] && e.CapacityGiB > 0 {
			total += int64(e.CapacityGiB) * gibBytes
			delete(wanted, e.Serial)
		}
	}
	if len(wanted) > 0 {
		return 0, errors.Errorf("%s on node %s has no capacity for serials %v",
			consts.AnnotationWekaFullDrives, nodeName, slices.Sorted(maps.Keys(wanted)))
	}
	return total, nil
}

func (o *MigrateToDriveSharingOperation) advanceInFlight(ctx context.Context, idx int) error {
	state := &o.results.Containers[idx]
	o.results.Current = string(state.Node)

	switch state.SubPhase {
	case MigrateDriveSharingSubPhaseDraining:
		return o.advanceDraining(ctx, state)
	case MigrateDriveSharingSubPhaseSigning:
		return o.advanceSigning(ctx, state)
	case MigrateDriveSharingSubPhaseProxy:
		return o.advanceProxy(ctx, state)
	case MigrateDriveSharingSubPhaseRejoining:
		return o.advanceRejoining(ctx, state)
	default:
		state.Reason = fmt.Sprintf("unknown sub-phase %q", state.SubPhase)
		return o.parkNode(ctx, state)
	}
}

// advanceDraining deletes the drive container and waits for the existing deletion flow to finish
// its graceful phase-out (deactivate, wait INACTIVE, weka cluster drive remove). The force-resign
// that flow normally performs is suppressed: the shared re-sign overwrites the signature moments
// later, and an exclusive resign in between only widens the window in which a sign-drives policy
// could claim the drives back.
func (o *MigrateToDriveSharingOperation) advanceDraining(ctx context.Context, state *MigrateToDriveSharingContainerState) error {
	container := &weka.WekaContainer{}
	err := o.client.Get(ctx, client.ObjectKey{Namespace: o.cluster.Namespace, Name: state.Container}, container)
	if apierrors.IsNotFound(err) {
		return o.enterSubPhase(ctx, state, MigrateDriveSharingSubPhaseSigning,
			"drive container phased out, re-signing its drives for the proxy")
	}
	if err != nil {
		return o.parkOnErr(ctx, state, "get drive container", err)
	}

	if !container.Spec.GetOverrides().SkipDrivesForceResign {
		patch := []byte(`{"spec":{"overrides":{"skipDrivesForceResign":true}}}`)
		if err := o.client.Patch(ctx, container, client.RawPatch(types.MergePatchType, patch)); err != nil {
			return o.parkOnErr(ctx, state, "suppress the exclusive force-resign on the drive container", err)
		}
	}
	if container.DeletionTimestamp == nil {
		if err := o.client.Delete(ctx, container); err != nil && !apierrors.IsNotFound(err) {
			return o.parkOnErr(ctx, state, "delete drive container", err)
		}
	}

	state.Reason = fmt.Sprintf("waiting for drive container %s to phase out of the cluster", state.Container)
	return o.parkNode(ctx, state)
}

// advanceSigning hands the freed serials to a child sign-drives operation in shared mode. The
// serials are first dropped from the node's exclusive bookkeeping, or the shared sign would skip
// them as already signed.
func (o *MigrateToDriveSharingOperation) advanceSigning(ctx context.Context, state *MigrateToDriveSharingContainerState) error {
	if err := RemoveFullDrivesFromNode(ctx, o.client, string(state.Node), state.Serials); err != nil {
		return o.parkOnErr(ctx, state, "remove the migrated serials from the node's full-drives inventory", err)
	}

	// The node annotation, not the child operation, decides whether the re-sign already happened: a
	// completed child self-deletes after its deletionDelay, and reading its absence as "never
	// signed" would run another erasing sign over drives the replacement is already carving.
	missing, err := o.serialsMissingFromSharedDrives(ctx, state)
	if err != nil {
		return o.parkOnErr(ctx, state, "verify the shared-drives annotation", err)
	}
	if len(missing) == 0 {
		return o.enterSubPhase(ctx, state, MigrateDriveSharingSubPhaseProxy, "drives re-signed for the proxy, checking the ssdproxy")
	}

	namespace, err := util.GetPodNamespace()
	if err != nil {
		return o.parkOnErr(ctx, state, "get operator namespace", err)
	}
	name := childSignOpName(o.ownerRef.GetName(), state.Container)

	// Latched: a child that reported Done while these serials were still missing must never be
	// recreated, since the self-delete that follows Done would otherwise let the next cycle read its
	// absence as "never signed" and run another erasing sign over drives a later sign may already
	// have handed to somebody.
	if state.SignDone {
		state.Reason = signDoneWithSerialsMissingReason(namespace, name, missing, state.Node)
		return o.parkNode(ctx, state)
	}
	state.SignOp = name

	child := &weka.WekaManualOperation{}
	err = o.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, child)
	if apierrors.IsNotFound(err) {
		if createErr := o.client.Create(ctx, o.buildChildSignOp(namespace, name, state)); createErr != nil {
			return o.parkOnErr(ctx, state, "create the shared sign-drives operation", createErr)
		}
		state.SignAttempts++
		state.SignFailedAt = nil
		state.Reason = fmt.Sprintf("created sign-drives operation %s/%s (attempt %d)", namespace, name, state.SignAttempts)
		return o.parkNode(ctx, state)
	}
	if err != nil {
		return o.parkOnErr(ctx, state, "get the shared sign-drives operation", err)
	}

	switch child.Status.Status {
	case "Done":
		state.SignDone = true
		state.Reason = signDoneWithSerialsMissingReason(namespace, name, missing, state.Node)
		return o.parkNode(ctx, state)
	case "Failed":
		return o.retryFailedSignOp(ctx, state, child)
	default:
		state.Reason = fmt.Sprintf("waiting for sign-drives operation %s/%s (status %q)", namespace, name, child.Status.Status)
		return o.parkNode(ctx, state)
	}
}

// signDoneWithSerialsMissingReason is the park reason when a child sign-drives operation reported
// Done but some of its serials never showed up as shared.
func signDoneWithSerialsMissingReason(namespace, name string, missing []string, node weka.NodeName) string {
	return fmt.Sprintf(
		"sign-drives %s/%s reported Done but serials %s are still absent from %s on node %s",
		namespace, name, strings.Join(missing, ", "), consts.AnnotationSharedDrives, node)
}

// enterSubPhase transitions state into subPhase, clears its Reason, persists, and parks with why as
// the wait error's message.
func (o *MigrateToDriveSharingOperation) enterSubPhase(ctx context.Context, state *MigrateToDriveSharingContainerState, subPhase, why string) error {
	state.SubPhase = subPhase
	state.Reason = ""
	if err := o.persist(ctx); err != nil {
		return err
	}
	return lifecycle.NewWaitErrorWithDuration(errors.New(why), migrateDriveSharingWaitDuration)
}

// retryFailedSignOp parks on the failure first, leaving the child inspectable, and only deletes it
// once the backoff has elapsed so the next cycle recreates it.
func (o *MigrateToDriveSharingOperation) retryFailedSignOp(ctx context.Context, state *MigrateToDriveSharingContainerState, child *weka.WekaManualOperation) error {
	state.Reason = fmt.Sprintf("sign-drives operation %s/%s failed: %s", child.Namespace, child.Name, child.Status.Result)
	if state.SignFailedAt == nil {
		now := metav1.Now()
		state.SignFailedAt = &now
		return o.parkNode(ctx, state)
	}
	if time.Since(state.SignFailedAt.Time) < migrateDriveSharingSignRetryBackoff {
		return o.parkNode(ctx, state)
	}
	if err := o.client.Delete(ctx, child); err != nil && !apierrors.IsNotFound(err) {
		return o.parkOnErr(ctx, state, "delete the failed sign-drives operation", err)
	}
	state.SignFailedAt = nil
	return o.parkNode(ctx, state)
}

// buildChildSignOp targets exactly the serials this container freed, on exactly its node.
// allowEraseWekaPartitions is forced on: the drives still carry the cluster's exclusive weka
// signature, which the proxy sign must overwrite.
func (o *MigrateToDriveSharingOperation) buildChildSignOp(namespace, name string, state *MigrateToDriveSharingContainerState) *weka.WekaManualOperation {
	options := weka.SignOptions{}
	if o.payload.SignOptions != nil {
		options = *o.payload.SignOptions
	}
	options.AllowEraseWekaPartitions = true

	child := &weka.WekaManualOperation{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: weka.WekaManualOperationSpec{
			Action: weka.WekaManualOperationActionSignDrives,
			Payload: weka.ManualOperatorPayload{
				SignDrives: &weka.SignDrivesPayload{
					Type:               weka.SignDrivesTypeDeviceSerials,
					DeviceSerials:      append([]string(nil), state.Serials...),
					Shared:             true,
					NodeSelector:       map[string]string{corev1.LabelHostname: string(state.Node)},
					SignOptions:        &options,
					DriveTypeOverrides: o.payload.DriveTypeOverrides,
				},
			},
		},
	}
	// An owner reference is only valid within one namespace; when the campaign lives elsewhere the
	// child is tracked through state.SignOp instead.
	if o.ownerRef.GetNamespace() == namespace {
		child.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(o.ownerRef, weka.GroupVersion.WithKind("WekaManualOperation"))}
	}
	return child
}

// childSignOpName is deterministic so a restarted operator finds the child it already created,
// hashing the tail when the composed name would exceed the object-name limit. Keyed by container,
// not node, so two drive containers on one node never share a child.
func childSignOpName(opName, container string) string {
	name := fmt.Sprintf("%s-sign-%s", opName, container)
	if len(name) <= maxChildOpNameLength {
		return name
	}
	suffix := "-" + util.GetHash(name, 8)
	return name[:maxChildOpNameLength-len(suffix)] + suffix
}

// serialsMissingFromSharedDrives reports which of the container's serials the node's shared-drives
// annotation does not yet list with a physical UUID.
func (o *MigrateToDriveSharingOperation) serialsMissingFromSharedDrives(ctx context.Context, state *MigrateToDriveSharingContainerState) ([]string, error) {
	shared, err := o.nodeSharedDrives(ctx, state.Node)
	if err != nil {
		return nil, err
	}
	signed := make(map[string]bool, len(shared))
	for _, d := range shared {
		if d.PhysicalUUID != "" {
			signed[d.Serial] = true
		}
	}
	var missing []string
	for _, serial := range state.Serials {
		if !signed[serial] {
			missing = append(missing, serial)
		}
	}
	return missing, nil
}

func (o *MigrateToDriveSharingOperation) nodeSharedDrives(ctx context.Context, nodeName weka.NodeName) ([]domain.SharedDriveInfo, error) {
	node := &corev1.Node{}
	if err := o.client.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node); err != nil {
		return nil, errors.Wrapf(err, "failed to get node %s", nodeName)
	}
	drives, _, err := domain.ReadNodeSharedDrives(node)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read %s on node %s", consts.AnnotationSharedDrives, nodeName)
	}
	return drives, nil
}

// advanceProxy restarts the node's existing ssdproxy only when it must: it cannot see the newly
// signed drives, or its pod is running on stale hugepages. Restarting it disrupts every tenant
// sharing that proxy, so it goes through the same cross-cluster gate as rotate-ssdproxy. A node
// with no proxy yet needs nothing — the replacement drive container brings one up.
func (o *MigrateToDriveSharingOperation) advanceProxy(ctx context.Context, state *MigrateToDriveSharingContainerState) error {
	proxy, err := discovery.GetSsdProxyOnNode(ctx, o.client, state.Node)
	if err != nil {
		var notFound *discovery.SsdProxyNotFoundError
		if errors.As(err, &notFound) {
			return o.enterSubPhase(ctx, state, MigrateDriveSharingSubPhaseRejoining, "no ssdproxy on the node yet, waiting for the replacement drive container")
		}
		return o.parkOnErr(ctx, state, "find the ssdproxy on the node", err)
	}

	if state.ProxyRestarted {
		podRunning, reason, checkErr := o.proxyPodRunning(ctx, proxy)
		if checkErr != nil {
			return o.parkOnErr(ctx, state, "check the ssdproxy pod", checkErr)
		}
		if !podRunning {
			state.Reason = reason
			return o.parkNode(ctx, state)
		}
		verdicts, recoverErr := o.proxyRecovered(ctx, o.mgr, o.execSvc, state.Node, proxy)
		if recoverErr != nil {
			return o.parkOnErr(ctx, state, "verify ssdproxy recovery", recoverErr)
		}
		if allowed, reason := AllAllowed(verdicts); !allowed {
			state.Reason = reason
			o.results.Blocked = verdicts
			return o.parkNode(ctx, state)
		}
		o.results.Blocked = nil
		return o.enterSubPhase(ctx, state, MigrateDriveSharingSubPhaseRejoining, "ssdproxy recovered")
	}

	reason, err := o.proxyRestartReason(ctx, state, proxy)
	if err != nil {
		return o.parkOnErr(ctx, state, "check whether the ssdproxy needs a restart", err)
	}
	if reason == "" {
		return o.enterSubPhase(ctx, state, MigrateDriveSharingSubPhaseRejoining, "ssdproxy already sees the new drives")
	}

	verdicts, err := o.proxyGate(ctx, o.mgr, o.execSvc, state.Node, proxy)
	if err != nil {
		return o.parkOnErr(ctx, state, "evaluate the ssdproxy disruption gate", err)
	}
	if allowed, blockReason := AllAllowed(verdicts); !allowed {
		state.Reason = blockReason
		o.results.Blocked = verdicts
		return o.parkNode(ctx, state)
	}
	o.results.Blocked = nil

	// Intent before action: ProxyRestarted persists first, so a crash right after the delete does
	// not restart the proxy a second time.
	state.ProxyRestarted = true
	state.Reason = "restarting the ssdproxy: " + reason
	if err := o.persist(ctx); err != nil {
		return err
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: proxy.Namespace, Name: proxy.Name}}
	if err := o.client.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		// The old pod is still running; clear the flag so the next cycle retries the restart.
		state.ProxyRestarted = false
		return o.parkOnErr(ctx, state, "delete the ssdproxy pod", err)
	}
	return lifecycle.NewWaitErrorWithDuration(
		errors.New("ssdproxy pod deleted, waiting for it to come back"), migrateDriveSharingWaitDuration)
}

// proxyPodRunning reports whether the restarted ssdproxy is back up. Checked before the recovery
// gate, which on a node whose proxy pod is still absent would find no drives to object to.
func (o *MigrateToDriveSharingOperation) proxyPodRunning(ctx context.Context, proxy *weka.WekaContainer) (running bool, reason string, err error) {
	pod := &corev1.Pod{}
	if err := o.client.Get(ctx, client.ObjectKey{Namespace: proxy.Namespace, Name: proxy.Name}, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "waiting for the ssdproxy pod to be recreated", nil
		}
		return false, "", errors.Wrapf(err, "failed to get ssdproxy pod %s", proxy.Name)
	}
	if pod.Status.Phase != corev1.PodRunning {
		return false, fmt.Sprintf("ssdproxy pod phase is %q, want %q", pod.Status.Phase, corev1.PodRunning), nil
	}
	if proxy.Status.Status != weka.Running {
		return false, fmt.Sprintf("ssdproxy container status is %q, want %q", proxy.Status.Status, weka.Running), nil
	}
	return true, "", nil
}

// proxyRestartReason returns why the proxy pod must be recreated, or "" when it must not.
func (o *MigrateToDriveSharingOperation) proxyRestartReason(ctx context.Context, state *MigrateToDriveSharingContainerState, proxy *weka.WekaContainer) (string, error) {
	shared, err := o.nodeSharedDrives(ctx, state.Node)
	if err != nil {
		return "", err
	}
	migrated := make(map[string]bool, len(state.Serials))
	for _, s := range state.Serials {
		migrated[s] = true
	}
	wantUUIDs := map[string]bool{}
	for _, d := range shared {
		if migrated[d.Serial] && d.PhysicalUUID != "" {
			wantUUIDs[d.PhysicalUUID] = true
		}
	}

	physical, err := o.proxyDrives(ctx, state.Node, proxy)
	if err != nil {
		return "", errors.Wrap(err, "failed to list the ssdproxy's physical drives")
	}
	for _, d := range physical {
		delete(wantUUIDs, d.PhysicalUUID)
	}
	if len(wantUUIDs) > 0 {
		return fmt.Sprintf("%d newly signed physical drive(s) are not visible to the running proxy", len(wantUUIDs)), nil
	}

	drifted, err := o.proxyHugepagesDrifted(ctx, proxy)
	if err != nil {
		return "", err
	}
	if drifted {
		return fmt.Sprintf("the pod runs on fewer hugepages than the container's spec.hugepages (%dMiB)", proxy.Spec.Hugepages), nil
	}
	return "", nil
}

// proxyHugepagesDrifted reports whether the proxy pod reserves fewer hugepages than its container
// spec now asks for. The spec is grown in place when a node gains shared capacity, but the pod is
// never recreated for it, so the running pod can lag behind.
func (o *MigrateToDriveSharingOperation) proxyHugepagesDrifted(ctx context.Context, proxy *weka.WekaContainer) (bool, error) {
	pod := &corev1.Pod{}
	if err := o.client.Get(ctx, client.ObjectKey{Namespace: proxy.Namespace, Name: proxy.Name}, pod); err != nil {
		if apierrors.IsNotFound(err) {
			// Already being recreated by the container controller; it will come up on the current spec.
			return false, nil
		}
		return false, errors.Wrapf(err, "failed to get ssdproxy pod %s", proxy.Name)
	}
	wekaContainer, err := resources.GetWekaPodContainer(pod)
	if err != nil {
		return false, errors.Wrap(err, "failed to find the weka container in the ssdproxy pod")
	}
	name, want := resources.HugepagesRequest(proxy)
	podQty := wekaContainer.Resources.Requests[name]
	return podQty.Cmp(want) < 0, nil
}

// advanceRejoining waits for the sharing replacement to be created, allocate virtual drives, and
// join the cluster with its drives ACTIVE on the node.
func (o *MigrateToDriveSharingOperation) advanceRejoining(ctx context.Context, state *MigrateToDriveSharingContainerState) error {
	replacement, err := o.findReplacement(ctx, state)
	if err != nil {
		return o.parkOnErr(ctx, state, "find the replacement drive container", err)
	}
	if replacement == nil {
		state.Reason = fmt.Sprintf(
			"waiting for the cluster controller to place a sharing drive container on node %s", state.Node)
		return o.parkNode(ctx, state)
	}
	state.Replacement = replacement.Name

	if replacement.Status.Status != weka.Running {
		state.Reason = fmt.Sprintf("replacement %s status is %q, want %q", replacement.Name, replacement.Status.Status, weka.Running)
		return o.parkNode(ctx, state)
	}
	if replacement.Status.Allocations == nil || len(replacement.Status.Allocations.VirtualDrives) == 0 {
		state.Reason = fmt.Sprintf("replacement %s has no virtual drives allocated yet", replacement.Name)
		return o.parkNode(ctx, state)
	}

	verdict := o.gate(ctx, o.cluster, replacement.GetNodeAffinity())
	if !verdict.Allowed {
		state.Reason = verdict.Reason
		o.results.Blocked = []ClusterVerdict{verdict}
		return o.parkNode(ctx, state)
	}
	o.results.Blocked = nil

	state.Phase = MigrateDriveSharingPhaseDone
	state.SubPhase = ""
	state.Reason = ""
	o.results.Current = ""
	o.results.Done = countMigrationDoneOrSkipped(o.results.Containers)

	// Fires exactly once per container: this branch is only reached on the transition into Done.
	o.recordOnOpAndCluster(corev1.EventTypeNormal, migrateDriveSharingEventReasonNodeComplete, fmt.Sprintf(
		"Migrated %s on node %s to drive sharing as %s (%d/%d complete)",
		state.Container, state.Node, replacement.Name, o.results.Done, o.results.Total))
	if replacement.GetNodeAffinity() != state.Node {
		o.recordOnOpAndCluster(corev1.EventTypeWarning, migrateDriveSharingEventReasonReplacementElsewhere, fmt.Sprintf(
			"Replacement for drained node %s landed on node %s (%s); the drained node's shared capacity is left for other tenants",
			state.Node, replacement.GetNodeAffinity(), replacement.Name))
	}

	if err := o.persist(ctx); err != nil {
		return err
	}
	return lifecycle.NewWaitErrorWithDuration(
		errors.New("drive container migrated, advancing to the next one"), migrateDriveSharingWaitDuration)
}

// findReplacement resolves the sharing drive container that took this one's place: a sharing
// container of the cluster that was not in the campaign's original set and that no other entry has
// already claimed. An exact match on the drained node wins. Count-based sizing pins no node to a
// drive container and the old pod's anti-affinity keeps the drained node blocked while it phases
// out, so on a fleet with spare eligible nodes the replacement can legitimately land elsewhere; the
// fallback accepts such a container only if it was created after this entry started, which rules
// out adopting an earlier node's replacement. An unscheduled container (no affinity yet) is never
// accepted: it is not placed anywhere.
func (o *MigrateToDriveSharingOperation) findReplacement(ctx context.Context, state *MigrateToDriveSharingContainerState) (*weka.WekaContainer, error) {
	containers, err := o.listDriveContainers(ctx, o.cluster)
	if err != nil {
		return nil, err
	}

	claimed := map[string]bool{}
	for i := range o.results.Containers {
		s := &o.results.Containers[i]
		claimed[s.Container] = true
		if s.Replacement != "" && s.Container != state.Container {
			claimed[s.Replacement] = true
		}
	}

	var elsewhere *weka.WekaContainer
	for i := range containers {
		c := &containers[i]
		if !c.UsesDriveSharing() || claimed[c.Name] || c.IsMarkedForDeletion() || c.GetNodeAffinity() == "" {
			continue
		}
		if c.GetNodeAffinity() == state.Node {
			return c, nil
		}
		if elsewhere == nil && state.StartedAt != nil && c.CreationTimestamp.After(state.StartedAt.Time) {
			elsewhere = c
		}
	}
	return elsewhere, nil
}

// ---------------------------------------------------------------------------
// Parking, events, persistence
// ---------------------------------------------------------------------------

// recordOnOpAndCluster mirrors a campaign-level event onto the target WekaCluster, where cluster
// owners look for it, as well as onto the operation.
func (o *MigrateToDriveSharingOperation) recordOnOpAndCluster(eventType, reason, message string) {
	util.RecordEvent(o.recorder, o.ownerRef, eventType, reason, consts.ActionMigrateToDriveSharing, message)
	if o.cluster != nil {
		util.RecordEvent(o.recorder, o.cluster, eventType, reason, consts.ActionMigrateToDriveSharing, message)
	}
}

// parkNode persists the current result and requeues via WaitErrorWithDuration, never marking
// anything Failed. Stamps BlockedSince the first time a still-Pending container parks (never
// refreshed, so elapsed keeps one stable origin) and emits a throttled Warning event.
func (o *MigrateToDriveSharingOperation) parkNode(ctx context.Context, state *MigrateToDriveSharingContainerState) error {
	if state.Phase == MigrateDriveSharingPhasePending {
		if state.BlockedSince == nil {
			now := metav1.Now()
			state.BlockedSince = &now
		}
		o.maybeWarnParked(state, state.BlockedSince, migrateBlockedWarnSignal)
	} else {
		o.maybeWarnParked(state, state.StartedAt, migrateStalledWarnSignal)
	}
	if err := o.persist(ctx); err != nil {
		return err
	}
	return lifecycle.NewWaitErrorWithDuration(errors.New(state.Reason), migrateDriveSharingWaitDuration)
}

// parkOnErr records "failed to <action>: <err>" as the container's reason and parks — every
// infrastructure failure in the state machine funnels through here rather than failing the campaign.
func (o *MigrateToDriveSharingOperation) parkOnErr(ctx context.Context, state *MigrateToDriveSharingContainerState, action string, err error) error {
	state.Reason = fmt.Sprintf("failed to %s: %v", action, err)
	return o.parkNode(ctx, state)
}

// maybeWarnParked emits signal's throttled Warning event if the container has been parked
// (measured from since) long enough to warrant it. Pure observability.
func (o *MigrateToDriveSharingOperation) maybeWarnParked(state *MigrateToDriveSharingContainerState, since *metav1.Time, signal parkedWarnSignal) {
	if since == nil {
		return
	}
	elapsed := time.Since(since.Time)
	if !signal.shouldWarn(elapsed) {
		return
	}
	detail := string(state.Node)
	if state.SubPhase != "" {
		detail = fmt.Sprintf("%s (%s)", state.Node, state.SubPhase)
	}
	// Rounded to minutes so two ticks landing in the same warn window produce identical messages
	// the event recorder can aggregate into one event.
	util.RecordEvent(o.recorder, o.ownerRef, corev1.EventTypeWarning, signal.eventReason, consts.ActionMigrateToDriveSharing,
		fmt.Sprintf("Node %s has been %s for %s: %s", detail, signal.description, elapsed.Round(time.Minute), state.Reason))
}

// maybeWarnParkedCampaign is maybeWarnParked's campaign-scoped sibling, keyed on the campaign's own
// BlockedSince since a campaign-scope park has no container to attach to.
func (o *MigrateToDriveSharingOperation) maybeWarnParkedCampaign(reason string) {
	if o.results.BlockedSince == nil {
		return
	}
	elapsed := time.Since(o.results.BlockedSince.Time)

	// A campaign-scope park can catch a container already InFlight — drained but not rejoined —
	// which is active impact, so it uses the stalled thresholds and names the container.
	if idx := indexOfMigrationPhase(o.results.Containers, MigrateDriveSharingPhaseInFlight); idx >= 0 {
		state := o.results.Containers[idx]
		if !migrateStalledWarnSignal.shouldWarn(elapsed) {
			return
		}
		util.RecordEvent(o.recorder, o.ownerRef, corev1.EventTypeWarning, migrateStalledWarnSignal.eventReason,
			consts.ActionMigrateToDriveSharing, fmt.Sprintf(
				"Drive container %s on node %s (%s) has been in flight for %s while the campaign is blocked: %s",
				state.Container, state.Node, state.SubPhase, elapsed.Round(time.Minute), reason))
		return
	}

	if !migrateCampaignParkedWarnSignal.shouldWarn(elapsed) {
		return
	}
	util.RecordEvent(o.recorder, o.ownerRef, corev1.EventTypeWarning, migrateCampaignParkedWarnSignal.eventReason,
		consts.ActionMigrateToDriveSharing, fmt.Sprintf(
			"Drive-sharing migration of cluster %s has been %s for %s: %s",
			o.results.Cluster, migrateCampaignParkedWarnSignal.description, elapsed.Round(time.Minute), reason))
}

// failTerminally records err, marks the owner Failed, and returns a WaitError so the engine
// requeues instead of hot-looping. Reserved for errors that cannot clear on their own.
func (o *MigrateToDriveSharingOperation) failTerminally(ctx context.Context, err error) error {
	o.results.Err = err.Error()
	if o.failureCallback != nil && !ownerFailed(o.ownerRef) {
		// Guarded so a permanently-misconfigured campaign rewrites its status once, not every tick.
		o.failureCallback(ctx) //nolint:errcheck // callback error is informational; returning primary error
	}
	return lifecycle.NewWaitErrorWithDuration(err, migrateDriveSharingWaitDuration)
}

// waitWithPersistedErr records a transient Plan failure and requeues, leaving the owner Running (a
// resolvable condition, not Failed). Stamps BlockedSince on first park and emits the throttled
// campaign-scoped Warning.
func (o *MigrateToDriveSharingOperation) waitWithPersistedErr(ctx context.Context, err error) error {
	o.results.Err = err.Error()
	if o.results.BlockedSince == nil {
		now := metav1.Now()
		o.results.BlockedSince = &now
	}
	o.maybeWarnParkedCampaign(err.Error())
	if perr := o.persist(ctx); perr != nil {
		return perr
	}
	return lifecycle.NewWaitErrorWithDuration(err, migrateDriveSharingWaitDuration)
}

// persist writes the current result to the owner status via progressCallback.
func (o *MigrateToDriveSharingOperation) persist(ctx context.Context) error {
	if o.progressCallback == nil {
		return nil
	}
	if err := o.progressCallback(ctx); err != nil {
		return errors.Wrap(err, "failed to persist migration progress")
	}
	return nil
}
