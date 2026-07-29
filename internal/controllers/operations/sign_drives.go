package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-lib/pkg/workers"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
)

type SignedDrivesExtendedPayload struct {
	weka.SignDrivesPayload
	ExcludedSerialIds     []string `json:"excludedSerialIds,omitempty"`
	SsdProxyContainerUuid string   `json:"ssd_proxy_container_uuid,omitempty"`
}

type SignDrivesOperation struct {
	client          client.Client
	kubeService     kubernetes.KubeService
	scheme          *runtime.Scheme
	payload         *weka.SignDrivesPayload
	image           string
	pullSecret      string
	serviceAccount  string
	containers      []*weka.WekaContainer
	ownerRef        client.Object
	results         DiscoverDrivesResult
	ownerStatus     string
	mgr             ctrl.Manager
	successCallback lifecycle.StepFunc
	failureCallback lifecycle.StepFunc
	force           bool
	tolerations     []v1.Toleration
	recorder        record.EventRecorder

	// apiReader is the uncached reader for Node reads inside ApplyDriveTypeOverrides'
	// RetryOnConflict closures: the cached client would re-read the same stale resourceVersion on
	// every retry and just exhaust retry.DefaultRetry. Nil in unit tests; see reader().
	apiReader client.Reader
}

func (o *SignDrivesOperation) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "SignDrives",
		Run:  AsRunFunc(o),
	}
}

func NewSignDrivesOperation(mgr ctrl.Manager, payload *weka.SignDrivesPayload, ownerRef client.Object, ownerDetails weka.WekaOwnerDetails, ownerStatus string, successCallback, failureCallback lifecycle.StepFunc, force bool) *SignDrivesOperation { //nolint:gocritic // intentional code pattern, linter suggestion does not apply here
	kclient := mgr.GetClient()
	return &SignDrivesOperation{
		mgr:             mgr,
		client:          kclient,
		kubeService:     kubernetes.NewKubeService(kclient),
		scheme:          mgr.GetScheme(),
		payload:         payload,
		image:           ownerDetails.Image,
		pullSecret:      ownerDetails.ImagePullSecret,
		serviceAccount:  ownerDetails.ServiceAccountName,
		ownerRef:        ownerRef,
		ownerStatus:     ownerStatus,
		tolerations:     ownerDetails.Tolerations,
		successCallback: successCallback,
		failureCallback: failureCallback,
		force:           force,
		recorder:        mgr.GetEventRecorderFor("weka-sign-drives"),
		apiReader:       mgr.GetAPIReader(),
	}
}

// reader returns the reader to use for Node reads that must observe the current
// resourceVersion. Falls back to the cached client when apiReader is unset, so unit tests
// that build SignDrivesOperation with only a fake client keep working.
func (o *SignDrivesOperation) reader() client.Reader {
	if o.apiReader != nil {
		return o.apiReader
	}
	return o.client
}

func (o *SignDrivesOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{Name: "GetContainers", Run: o.GetContainers},
		&lifecycle.SimpleStep{Name: "DeleteOnDone", Run: o.DeleteContainers, Predicates: lifecycle.Predicates{o.IsDone}, FinishOnSuccess: true},
		&lifecycle.SimpleStep{
			Name: "ApplyDriveTypeOverrides",
			Run:  o.ApplyDriveTypeOverrides,
			Predicates: lifecycle.Predicates{
				func() bool {
					return o.payload.Shared && o.payload.DriveTypeOverrides != nil
				},
			},
		},
		&lifecycle.SimpleStep{Name: "EnsureContainers", Run: o.EnsureContainers},
		&lifecycle.SimpleStep{Name: "PollResults", Run: o.PollResults},
		&lifecycle.SimpleStep{Name: "ProcessResult", Run: o.ProcessResult},
		&lifecycle.SimpleStep{
			Name: "FailureUpdate",
			Run:  o.FailureCallback,
			Predicates: lifecycle.Predicates{
				o.OperationFailed,
			},
			FinishOnSuccess: true,
		},
		&lifecycle.SimpleStep{Name: "SuccessUpdate", Run: o.SuccessUpdate},
		&lifecycle.SimpleStep{Name: "DeleteCompletedContainers", Run: o.DeleteContainers},
	}
}

func (o *SignDrivesOperation) GetContainers(ctx context.Context) error {
	existing, err := discovery.GetOwnedContainers(ctx, o.client, o.ownerRef.GetUID(), o.ownerRef.GetNamespace(), weka.WekaContainerModeAdhocOp)
	if err != nil {
		return err
	}
	o.containers = existing
	return nil
}

func (o *SignDrivesOperation) EnsureContainers(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	// validate image for sign-drives
	if o.image == "" {
		o.image = config.Config.SignDrivesImage
	} else if strings.Contains(o.image, "weka.io/weka-in-container") {
		err := fmt.Errorf("weka image is not allowed for sign-drives operation, do not set image to use default")
		o.results.Err = err.Error()
		o.failureCallback(ctx) //nolint:errcheck // callback error is informational; returning primary error
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	matchingNodes, err := o.kubeService.GetNodes(ctx, o.payload.NodeSelector)
	if err != nil {
		return err
	}

	// filter out nodes that are not ready
	readyNodes := []v1.Node{}
	for i := range matchingNodes {
		if resources.NodeIsReady(&matchingNodes[i]) {
			readyNodes = append(readyNodes, matchingNodes[i])
		} else {
			logger.Info("Skipping node that is not ready", "node", matchingNodes[i].Name)
		}
	}

	if len(readyNodes) == 0 {
		return fmt.Errorf("no matching nodes found for the given node selector")
	}

	existingContainerNodes := make(map[string]bool)
	for _, container := range o.containers {
		existingContainerNodes[string(container.GetNodeAffinity())] = true
	}

	defer logger.SetValues("readyNodes", len(readyNodes), "existingContainers", len(existingContainerNodes))
	logger.SetAttributes()

	//TODO: Re-factor to all pieces of results will be a generic results structure, allowing to implement generic parallezation with callback funcs
	newlyCreated := 0
	skip := 0

	toCreate := []*weka.WekaContainer{}
	for i := range readyNodes {
		node := &readyNodes[i]
		if existingContainerNodes[node.Name] {
			continue
		}

		// Create a copy of the original payload to avoid modifying it; a fresh copy per node so
		// one node's exclusions/ssdproxy UUID can't leak into the next node's instructions.
		extendedPayload := SignedDrivesExtendedPayload{
			SignDrivesPayload: *o.payload,
		}

		// if data exists and not force - skip
		if !o.force {
			// If weka-full-drives annotation is absent, the node hasn't been updated yet.
			// Invalidate hash so sign-drives re-runs and writes both annotations.
			if _, hasFullDrives := node.Annotations[consts.AnnotationWekaFullDrives]; !hasFullDrives {
				if node.Annotations[consts.AnnotationWekaDrives] != "" && node.Annotations[consts.AnnotationSignDrivesHash] != "" {
					// Clear hash so sign-drives re-runs and writes the new annotations
					delete(node.Annotations, consts.AnnotationSignDrivesHash)
					if updateErr := o.client.Update(ctx, node); updateErr != nil {
						return fmt.Errorf("failed to clear sign-drives hash for format migration on node %s: %w", node.Name, updateErr)
					}
				}
			}

			targetHash := domain.CalculateNodeDriveSignHash(node)
			if node.Annotations[consts.AnnotationSignDrivesHash] == targetHash {
				skip += 1
				continue
			}
		}

		// read signed drives from weka.io/weka-drives node annotation and add to exclusions
		alreadySignedDrives := getAlreadySignedDrives(node)
		if len(alreadySignedDrives) > 0 {
			extendedPayload.ExcludedSerialIds = alreadySignedDrives

			logger.Info("Updating exclusions with previously signed drives to avoid re-signing", "node", node.Name, "excludedDrives", alreadySignedDrives)
		}

		if o.payload.Shared {
			// in drive sharing mode, set the ssd proxy socket path in the instructions payload
			ssdProxyUuid, ssdErr := o.GetSsdProxyContainerUuid(ctx, node.Name)
			if ssdErr != nil {
				return errors.Wrap(ssdErr, "failed to get ssdproxy container uuid")
			}
			if ssdProxyUuid != nil {
				extendedPayload.SsdProxyContainerUuid = *ssdProxyUuid

				logger.Info("Setting ssdproxy container uuid in sign-drives payload", "node", node.Name, "ssdProxyContainerUuid", *ssdProxyUuid)
			}
		}

		instructions, instrErr := o.createInstructions(&extendedPayload)
		if instrErr != nil {
			return instrErr
		}

		nodeLabels := util.MergeMaps(o.ownerRef.GetLabels(), factory.RequiredAnyWekaContainerLabels(weka.WekaContainerModeAdhocOp))

		containerName := fmt.Sprintf("weka-sign-and-discover-drives-%s", node.Name)
		newContainer := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      containerName,
				Namespace: o.ownerRef.GetNamespace(),
				Labels:    nodeLabels,
			},
			Spec: weka.WekaContainerSpec{
				Mode:               weka.WekaContainerModeAdhocOp,
				NodeAffinity:       weka.NodeName(node.Name),
				Image:              o.image,
				ImagePullSecret:    o.pullSecret,
				Instructions:       instructions,
				Tolerations:        o.tolerations,
				HostPID:            true,
				ServiceAccountName: o.serviceAccount,
			},
		}
		toCreate = append(toCreate, newContainer)

	}

	results := workers.ProcessConcurrently(ctx, toCreate, 32, func(ctx context.Context, container *weka.WekaContainer) error {
		if refErr := controllerutil.SetControllerReference(o.ownerRef, container, o.scheme); refErr != nil {
			return errors.Wrap(refErr, "failed to set controller reference")
		}

		if createErr := o.client.Create(ctx, container); createErr != nil {
			return errors.Wrap(createErr, "failed to create container")
		}
		return nil
	})

	err = results.AsError()
	if err != nil {
		logger.SetError(err, fmt.Sprintf("%d failed", len(results.GetErrors())))
		return err
	} else {
		for _, result := range results.Items {
			if result.Err == nil {
				o.containers = append(o.containers, result.Object)
				newlyCreated += 1
			}
		}
		logger.SetValues("newlyCreated", newlyCreated, "skipNodes", skip)
		return nil
	}
}

// nodeOverrideWrite reports what writeNodeDriveTypeOverrides actually did on one node.
type nodeOverrideWrite struct {
	wrote          bool
	rulesOnly      bool // node wasn't signed: rules persisted, no drive rewrite
	drivesChanged  int
	unmatchedRules []int
	appliedDrives  []domain.SharedDriveInfo
	sourceDrives   []domain.SharedDriveInfo // pre-override drives from the same fresh read
}

// writeNodeDriveTypeOverrides is the single write recipe for persisting a rule-set change on one
// node: writes weka.io/drive-type-overrides, and if the node is signed also re-applies the rules
// to weka-shared-drives — otherwise leaves weka-shared-drives alone and the rules take effect when
// updateProxyModeAnnotations signs the node and reads them back off the annotation. Either way
// clears the sign-drives hash to force that (re-)sign. The signed/unsigned branch is decided fresh
// inside the retry closure, off the same Get this writes from, not off the caller's precheck.
func (o *SignDrivesOperation) writeNodeDriveTypeOverrides(ctx context.Context, nodeName string, newRules []weka.DriveTypeOverrideRule) (nodeOverrideWrite, error) {
	var result nodeOverrideWrite
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		result = nodeOverrideWrite{} // reset: a conflict retry must not accumulate a prior attempt's values

		cur := &v1.Node{}
		if getErr := o.reader().Get(ctx, client.ObjectKey{Name: nodeName}, cur); getErr != nil {
			return getErr
		}

		existingRules, readErr := domain.ReadDriveTypeOverrides(cur)
		if readErr != nil {
			return fmt.Errorf("failed to read drive type overrides on node %s: %w", nodeName, readErr)
		}
		if slices.Equal(existingRules, newRules) {
			// Nothing to do: already carries the desired rules, whether via a genuine no-op pass
			// or a concurrent writer that got there first.
			return nil
		}

		if writeErr := domain.WriteDriveTypeOverrides(cur, newRules); writeErr != nil {
			return fmt.Errorf("failed to write drive type overrides on node %s: %w", nodeName, writeErr)
		}

		// What actually gets written is recomputed fresh on every retry attempt inside this
		// closure, not hoisted from the caller: updateProxyModeAnnotations (funcs_oneoff.go) can
		// rewrite weka-shared-drives concurrently between our Get and Update, and a stale
		// pre-conflict recompute would silently clobber it.
		curDrives, signed, readErr := domain.ReadNodeSharedDrives(cur)
		if readErr != nil {
			return fmt.Errorf("node %s: %w", nodeName, readErr)
		}
		if signed {
			updated, changed, unmatchedRuleIdxs := domain.ApplyDriveTypeOverrides(curDrives, newRules)
			sharedDrivesJSON, marshalErr := json.Marshal(updated)
			if marshalErr != nil {
				return fmt.Errorf("failed to marshal shared drives annotation on node %s: %w", nodeName, marshalErr)
			}
			if cur.Annotations == nil {
				cur.Annotations = make(map[string]string)
			}
			cur.Annotations[consts.AnnotationSharedDrives] = string(sharedDrivesJSON)
			result.appliedDrives = updated
			result.sourceDrives = curDrives
			result.unmatchedRules = unmatchedRuleIdxs
			result.drivesChanged = changed
		} else {
			// Not signed: there are no drives to override, so weka-shared-drives must not be
			// written back. The rules annotation above is still written, though — that's what
			// makes rules: [] clear a not-yet-signed node too, instead of leaving stale rules to
			// be re-applied once it is signed.
			result.rulesOnly = true
		}

		// Cleared for any rule-set change, including capacity-only rules: applying an override
		// overwrites the drive's IU-derived type with no backup, so only a re-sign (forced by
		// deleting this) can recover it if rules are later narrowed or cleared.
		delete(cur.Annotations, consts.AnnotationSignDrivesHash)

		if updateErr := o.client.Update(ctx, cur); updateErr != nil {
			return updateErr
		}
		result.wrote = true
		return nil
	})
	if err != nil {
		return nodeOverrideWrite{}, fmt.Errorf("failed to apply drive type overrides on node %s: %w", nodeName, err)
	}
	return result, nil
}

// updateNodeDriveCapacity recomputes and writes the shared-drive capacity extended resources
// after a signed-path write has landed. Runs as its own RetryOnConflict, independent of the write
// above (different subresource: object body vs. Status), off its own fresh Get. Annotations are
// written first (by the caller) so that if this fails, Status is left stale-but-consistent with
// what's annotated, rather than recording an override no annotation remembers.
func (o *SignDrivesOperation) updateNodeDriveCapacity(ctx context.Context, nodeName string, appliedDrives []domain.SharedDriveInfo) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &v1.Node{}
		if getErr := o.reader().Get(ctx, client.ObjectKey{Name: nodeName}, cur); getErr != nil {
			return getErr
		}
		blockedSerials, blockedErr := domain.ReadBlockedDriveSerials(cur)
		if blockedErr != nil {
			return fmt.Errorf("node %s: %w", nodeName, blockedErr)
		}
		blockedPhysicalUUIDs, blockedUUIDErr := domain.ReadBlockedDrivePhysicalUUIDs(cur)
		if blockedUUIDErr != nil {
			return fmt.Errorf("node %s: %w", nodeName, blockedUUIDErr)
		}
		// Summed from this fresh read, not appliedDrives: updateProxyModeAnnotations can add a
		// newly discovered drive in this window, and summing appliedDrives would publish capacity
		// that omits it.
		capacityDrives, curSigned, readErr := domain.ReadNodeSharedDrives(cur)
		if readErr != nil {
			return fmt.Errorf("node %s: %w", nodeName, readErr)
		}
		if !curSigned {
			// Annotation cleared underneath us. Fall back to what we just persisted rather than
			// publishing zero capacity, which would unschedule running drive pods on the strength
			// of a transient read.
			capacityDrives = appliedDrives
		}
		domain.SetSharedDriveCapacityResources(cur, capacityDrives, blockedPhysicalUUIDs, blockedSerials)
		return o.client.Status().Update(ctx, cur)
	})
	if err != nil {
		return fmt.Errorf("failed to update drive capacity resources on node %s: %w", nodeName, err)
	}
	return nil
}

// nodeOverrideOutcome is what applyDriveTypeOverridesToNode did on one node, already aggregated
// for the caller's loop.
type nodeOverrideOutcome struct {
	evaluated      bool // rules were matched against real drives; feeds the Event denominator
	rulesOnly      bool
	wrote          bool
	drivesChanged  int
	unmatchedRules []int // rule indexes matching no drive, already model-evidence filtered
}

// applyDriveTypeOverridesToNode evaluates and, if needed, applies newRules to a single node,
// returning one already-filtered set of unmatched rule indexes. listed is the caller's cached
// list read: it drives the cheap-precheck fast path below, and its own matched/unmatched result
// is what's reported when no write happens. The write recipes re-read through o.reader() and so
// observe any newer state regardless of what listed contains.
func (o *SignDrivesOperation) applyDriveTypeOverridesToNode(ctx context.Context, listed *v1.Node, newRules []weka.DriveTypeOverrideRule) (nodeOverrideOutcome, error) {
	nodeName := listed.Name
	logger := instrumentation.CurrentSpanLogger(ctx)

	existingRules, readErr := domain.ReadDriveTypeOverrides(listed)
	if readErr != nil {
		return nodeOverrideOutcome{}, fmt.Errorf("failed to read drive type overrides on node %s: %w", nodeName, readErr)
	}
	listedDrives, signed, readErr := domain.ReadNodeSharedDrives(listed)
	if readErr != nil {
		return nodeOverrideOutcome{}, fmt.Errorf("node %s: %w", nodeName, readErr)
	}

	outcome := nodeOverrideOutcome{evaluated: signed}

	srcDrives := listedDrives
	var unmatched []int
	if signed {
		// Matching is on Model/CapacityGiB, never Type, so re-running this over already-overridden
		// drives yields the same matched/unmatched set as running it fresh — valid even when no
		// write happens this pass.
		_, _, unmatched = domain.ApplyDriveTypeOverrides(listedDrives, newRules)
	}

	// slices.Equal treats nil and empty as equal, which is what "same rules" means here. Rule
	// order is semantic (first match wins), so element order matters. This only decides whether
	// to attempt a write at all; writeNodeDriveTypeOverrides recomputes everything fresh.
	if !slices.Equal(existingRules, newRules) {
		write, err := o.writeNodeDriveTypeOverrides(ctx, nodeName, newRules)
		if err != nil {
			return outcome, err
		}
		outcome.wrote = write.wrote
		outcome.rulesOnly = write.rulesOnly
		outcome.drivesChanged = write.drivesChanged

		// The write's fresh read is authoritative over `listed` (the cached one) for both halves of
		// the reporting, and the two must move together: `evaluated` is the Event's denominator and
		// `unmatched` its numerator, so letting them come from different reads produces nonsense
		// like "matched no drive on 1 of 0 evaluated nodes".
		if write.wrote && write.rulesOnly {
			// Cache said signed, the fresh read says not: there were no drives to match against, so
			// this node was not evaluated and its stale unmatched set must not be reported. Also
			// keeps evaluatedNodes and rulesOnlyNodes disjoint, as the caller documents them.
			outcome.evaluated = false
			unmatched = nil
		}
		if write.wrote && !write.rulesOnly {
			// Cache said not signed, the fresh read says it is: the rules were matched against real
			// drives, so this node counts as evaluated.
			outcome.evaluated = true
			srcDrives, unmatched = write.sourceDrives, write.unmatchedRules
			if capErr := o.updateNodeDriveCapacity(ctx, nodeName, write.appliedDrives); capErr != nil {
				return outcome, capErr
			}
		}
	}

	// hasModelEvidence suppresses false "matched no drive" warnings for model-based rules when no
	// drive on this node has a recorded Model yet (e.g. an older agent, or a failed lookup) — a
	// forced re-sign will populate it, so the rule isn't dead.
	hasModelEvidence := anyDriveHasModel(srcDrives)
	for _, idx := range unmatched {
		rule := newRules[idx]
		if strings.TrimSpace(rule.Model) != "" && !hasModelEvidence {
			continue
		}
		outcome.unmatchedRules = append(outcome.unmatchedRules, idx)
		logger.Warn("Drive type override rule matched no drive", "node", nodeName, "ruleIndex", idx, "model", rule.Model, "capacityGiB", rule.CapacityGiB, "type", rule.Type)
	}

	return outcome, nil
}

// ApplyDriveTypeOverrides persists the payload's drive-type override rules on each matching node
// and re-applies them to its already-annotated shared drives immediately, so a rule change takes
// effect without a fresh sign-drives pod run. It doesn't reuse EnsureContainers' node loop, which
// skips exactly the already-annotated nodes this needs to revisit.
func (o *SignDrivesOperation) ApplyDriveTypeOverrides(ctx context.Context) error {
	// GetSteps already predicates this step on the same two conditions; repeated here so the
	// function can't panic if that predicate is ever reordered or dropped.
	if !o.payload.Shared || o.payload.DriveTypeOverrides == nil {
		return nil
	}

	logger := instrumentation.CurrentSpanLogger(ctx)
	newRules := o.payload.DriveTypeOverrides.Rules

	// Listed through the uncached reader: a stale "not signed yet" read would make this whole step
	// a silent no-op, reaching SuccessUpdate having applied nothing, with no Node watch to
	// retrigger a one-shot WekaManualOperation. Confined to operations that actually use the
	// feature by the early return above.
	nodeList := &v1.NodeList{}
	if err := o.reader().List(ctx, nodeList, client.MatchingLabels(o.payload.NodeSelector)); err != nil {
		return fmt.Errorf("failed to list nodes for drive type overrides: %w", err)
	}
	nodes := nodeList.Items

	// unmatchedNodeCountByRule tracks, per rule index, how many nodes had no matching drive.
	// Aggregated across the loop so a bad rule emits one Event total, not one per node.
	unmatchedNodeCountByRule := map[int]int{}

	// evaluatedNodes counts nodes actually matched against newRules this pass; rulesOnlyNodes
	// (not-yet-signed nodes that only got the rules annotation) are excluded, since no rule was
	// matched against any drive there. nodesUpdated/drivesChanged feed the Applied/Cleared Event.
	var evaluatedNodes, rulesOnlyNodes, nodesUpdated, drivesChanged int

	// nodeErrs accumulates per-node failures instead of aborting the loop. A single node with an
	// undecodable annotation used to abort the whole step, so no node in the selector got signed
	// until a human repaired it — a fleet-wide stall from one bad node. Mirrors EnsureContainers'
	// workers.ProcessConcurrently + results.AsError() shape, minus the concurrency: this loop
	// mutates shared counters, so it stays sequential.
	var nodeErrs []error

	for i := range nodes {
		listed := &nodes[i]
		outcome, err := o.applyDriveTypeOverridesToNode(ctx, listed, newRules)
		if outcome.evaluated {
			evaluatedNodes++
		}
		if err != nil {
			nodeErrs = append(nodeErrs, err)
			continue
		}

		if outcome.wrote && outcome.rulesOnly {
			rulesOnlyNodes++
			logger.Info("Persisted drive type override rules on a not-yet-signed node; they apply when it is signed", "node", listed.Name, "rules", len(newRules))
		}
		if outcome.wrote && !outcome.rulesOnly {
			nodesUpdated++
			drivesChanged += outcome.drivesChanged
		}
		for _, idx := range outcome.unmatchedRules {
			unmatchedNodeCountByRule[idx]++
		}
	}

	logger.SetValues("evaluatedNodes", evaluatedNodes, "rulesOnlyNodes", rulesOnlyNodes, "failedNodes", len(nodeErrs))

	// Emit one Event per rule, not one per node — per-node messages would defeat the API
	// server's event aggregation on a large selector. Reports how many selected nodes each rule
	// matched nothing on; per-node detail stays in the logs above.
	for idx, rule := range newRules {
		nodeCount := unmatchedNodeCountByRule[idx]
		if nodeCount == 0 {
			continue
		}
		msg := fmt.Sprintf("Drive type override rule (model=%q, capacityGiB=%d, type=%s) matched no drive on %d of %d evaluated nodes", rule.Model, rule.CapacityGiB, rule.Type, nodeCount, evaluatedNodes)
		o.recorder.Event(o.ownerRef, v1.EventTypeWarning, "DriveTypeOverrideNoMatch", msg)
	}

	if nodesUpdated > 0 {
		// This fires once per rule-set change, whereas the unmatched Warning above re-fires on
		// every evaluated pass by design — emitting this every pass would turn a one-off "applied"
		// record into noise.
		if len(newRules) == 0 {
			o.recorder.Event(o.ownerRef, v1.EventTypeNormal, "DriveTypeOverridesCleared",
				fmt.Sprintf("Cleared drive type overrides on %d node(s); overridden drives keep their forced type until the re-sign that follows restores the IU-derived one", nodesUpdated))
		} else {
			o.recorder.Event(o.ownerRef, v1.EventTypeNormal, "DriveTypeOverridesApplied",
				fmt.Sprintf("Applied %d drive type override rule(s): %d node(s) updated, %d drive type(s) changed", len(newRules), nodesUpdated, drivesChanged))
		}
	}

	// Not-yet-signed nodes are reported separately, because they are invisible to the Event above:
	// nodesUpdated only counts nodes that already had drives to re-type, so a first-ever sign — where
	// every selected node is unsigned — left nodesUpdated at 0 and emitted nothing at all. The drives
	// do get their forced type, but later, when processResults applies the persisted rules to the
	// freshly-signed inventory. Events are the only reporting channel for this feature, so without
	// this the most common greenfield case has no record that the overrides were registered.
	// Gated on rulesOnlyNodes (set only when a write actually landed), so it keeps the same
	// once-per-rule-set-change cadence as Applied rather than re-firing every pass. Rules-only
	// clears need no Event: there is no forced type on an unsigned node to report undoing.
	if rulesOnlyNodes > 0 && len(newRules) > 0 {
		o.recorder.Event(o.ownerRef, v1.EventTypeNormal, "DriveTypeOverridesPersisted",
			fmt.Sprintf("Persisted %d drive type override rule(s) on %d not-yet-signed node(s); the forced types are applied when those nodes are first signed", len(newRules), rulesOnlyNodes))
	}

	// Surfaced after the Event above so the nodes that did succeed are still recorded, and ahead of
	// the WaitError because a real failure must reach the operation's status rather than being
	// masked as "waiting". Returning an error defers the remaining steps just as the WaitError
	// would, so the cache-coherence guarantee still holds. This error never reaches
	// OperationFailed() (the steps engine logs it and requeues), so the Event below is the only
	// user-visible record of the failure — a single bad node must not fail the whole operation,
	// which is exactly the fleet-wide stall nodeErrs accumulation exists to avoid, but it also must
	// not fail silently.
	if len(nodeErrs) > 0 {
		const maxReported = 5
		reported := nodeErrs
		if len(reported) > maxReported {
			// Capped the way workers.Results.AsError caps: on a large selector every node can fail
			// the same way, and one message per node buries the signal.
			reported = append(reported[:maxReported:maxReported], fmt.Errorf("and %d further node(s) failed the same step", len(nodeErrs)-maxReported))
		}
		merr := &workers.MultiError{Errors: reported}
		// One Event for the whole pass, not one per node, matching how DriveTypeOverrideNoMatch
		// aggregates above. len(nodes) (the selected node count), not evaluatedNodes: a node can
		// fail its precheck read before ever being evaluated, so evaluatedNodes would be the wrong
		// denominator here.
		o.recorder.Event(o.ownerRef, v1.EventTypeWarning, "DriveTypeOverrideFailed",
			fmt.Sprintf("Drive type overrides failed on %d of %d node(s): %s", len(nodeErrs), len(nodes), merr.Error()))
		return merr
	}

	if nodesUpdated > 0 {
		// This pass wrote to at least one node, so EnsureContainers (which runs right after, via
		// the cached client) would still see the stale sign-drives hash and skip re-signing, since
		// the cache hasn't observed the write yet. Deferring to the next reconcile gives the cache
		// time to catch up.
		return lifecycle.NewWaitError(errors.New("drive type overrides applied; waiting a cycle for the node cache to observe the change before ensuring containers"))
	}

	return nil
}

// anyDriveHasModel reports whether at least one drive carries a non-empty, non-whitespace
// Model. Used to suppress "matched no drive" reporting for model-based rules on nodes whose
// drives have no Model recorded yet, rather than reporting a spurious dead rule.
func anyDriveHasModel(drives []domain.SharedDriveInfo) bool {
	for _, d := range drives {
		if strings.TrimSpace(d.Model) != "" {
			return true
		}
	}
	return false
}

// isResultsProcessed returns true when the WekaContainer reconciliation has run
// processResults (i.e. updateNodeAnnotations). Deleting before this condition is set
// causes node annotations to never be written.
func isResultsProcessed(container *weka.WekaContainer) bool {
	for _, c := range container.Status.Conditions {
		if c.Type == condition.CondResultsProcessed && c.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

func (o *SignDrivesOperation) PollResults(ctx context.Context) error {
	// if force is not set, do not wait for all results, and return as many as are fully processed
	if !o.force {
		// wait for at least one result to have node annotations updated
		for _, container := range o.containers {
			if isResultsProcessed(container) {
				return nil
			}
		}
	}

	allReady := true
	for _, container := range o.containers {
		if !isResultsProcessed(container) {
			allReady = false
			break
		}
	}

	if !allReady {
		return lifecycle.NewWaitError(fmt.Errorf("not all container results are processed yet"))
	}

	return nil
}

func (o *SignDrivesOperation) ProcessResult(ctx context.Context) error {
	res, err := processResult(ctx, o.containers, !o.force)
	if err != nil || res == nil {
		return err
	}
	o.results = *res
	return err
}

func (o *SignDrivesOperation) GetJsonResult() string {
	total := 0
	errs := []string{}
	maxErrors := 5

	if o.results.Err != "" {
		return o.results.Err
	}

	drivesByNode := map[string]int{}
	for nodeName, nodeResults := range o.results.Results {
		drivesCount := len(nodeResults.Drives) + len(nodeResults.ProxyDrives)
		total += drivesCount
		if drivesCount > 0 {
			drivesByNode[nodeName] = drivesCount
		}
		if nodeResults.Err != nil {
			if len(errs) < maxErrors {
				errs = append(errs, nodeResults.Err.Error())
			}
		}
	}

	ret := map[string]interface{}{}
	if len(drivesByNode) > 0 {
		ret["results"] = drivesByNode
		ret["message"] = fmt.Sprintf("Signed %d drives on %d nodes", total, len(o.results.Results))
	} else {
		ret["message"] = "No new drives signed"
	}
	if len(errs) > 0 {
		ret["errors"] = errs
	}

	resultJSON, _ := json.Marshal(ret) //nolint:errcheck // marshal of known-serializable struct; error not possible
	res := string(resultJSON)

	if len(drivesByNode) > 0 {
		_ = o.RecordEvent("SignDrives", res) //nolint:errcheck // event recording is best-effort; error not actionable
	}
	return res
}

func (o *SignDrivesOperation) DeleteContainers(ctx context.Context) error {
	updatedContainers := []*weka.WekaContainer{}

	for _, container := range o.containers {
		if isResultsProcessed(container) || o.force {
			err := o.client.Delete(ctx, container)
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		} else {
			updatedContainers = append(updatedContainers, container)
		}
	}
	o.containers = updatedContainers
	return nil
}

func (o *SignDrivesOperation) IsDone() bool {
	return o.ownerStatus == "Done"
}

func (o *SignDrivesOperation) SuccessUpdate(ctx context.Context) error {
	return o.successCallback(ctx)
}

func (o *SignDrivesOperation) FailureCallback(ctx context.Context) error {
	if o.failureCallback == nil {
		return nil
	}
	return o.failureCallback(ctx)
}

func (o *SignDrivesOperation) OperationFailed() bool {
	return o.results.Err != ""
}

func (o *SignDrivesOperation) RecordEvent(reason, message string) error {
	if o.ownerRef == nil {
		return fmt.Errorf("ownerRef is nil")
	}

	o.recorder.Event(o.ownerRef, v1.EventTypeNormal, reason, message)
	return nil
}

// getAlreadySignedDrives extracts the list of already signed drives from node annotations.
// It checks both weka.io/weka-drives (regular mode) and weka.io/weka-shared-drives (drive sharing mode).
func getAlreadySignedDrives(node *v1.Node) []string {
	alreadySignedDrives := []string{}

	if node.Annotations == nil {
		return alreadySignedDrives
	}

	// Regular drives (non-proxy mode) — reads serials from both new and legacy annotations
	fullAnnotation := node.Annotations[consts.AnnotationWekaFullDrives]
	legacyAnnotation := node.Annotations[consts.AnnotationWekaDrives]
	if serials, err := domain.ReadAnnotatedDriveSerials(fullAnnotation, legacyAnnotation); err == nil {
		alreadySignedDrives = append(alreadySignedDrives, serials...)
	}

	// Shared drives (proxy/drive sharing mode)
	if sharedDrivesStr, ok := node.Annotations[consts.AnnotationSharedDrives]; ok && sharedDrivesStr != "" {
		var sharedDrives []domain.SharedDriveInfo
		if err := json.Unmarshal([]byte(sharedDrivesStr), &sharedDrives); err == nil {
			for _, drive := range sharedDrives {
				alreadySignedDrives = append(alreadySignedDrives, drive.Serial)
			}
		}
	}

	return alreadySignedDrives
}

func (o *SignDrivesOperation) createInstructions(extendedPayload *SignedDrivesExtendedPayload) (*weka.Instructions, error) {
	// Marshal the extended payload
	payloadBytes, err := json.Marshal(extendedPayload)
	if err != nil {
		return nil, err
	}

	instructions := &weka.Instructions{
		Type:    weka.InstructionTypeSignDrives,
		Payload: string(payloadBytes),
	}

	return instructions, nil
}

func (o *SignDrivesOperation) GetSsdProxyContainerUuid(ctx context.Context, nodeName string) (*string, error) {
	// Get the operator namespace where ssdproxy containers are deployed
	ssdProxy, err := discovery.GetSsdProxyOnNode(ctx, o.client, weka.NodeName(nodeName))
	var notFoundErr *discovery.SsdProxyNotFoundError
	if errors.As(err, &notFoundErr) {
		// No ssdproxy found on the node, return nil
		return nil, nil
	}

	if err != nil {
		return nil, errors.Wrap(err, "failed to get ssdproxy container on node")
	}

	uuid := string(ssdProxy.GetUID())
	return &uuid, nil
}
