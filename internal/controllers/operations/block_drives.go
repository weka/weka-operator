package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

type BlockDrivesOperation struct {
	client          client.Client
	payload         *weka.BlockDrivesPayload
	results         BlockDrivesResult
	ownerStatus     *string
	successCallback lifecycle.StepFunc
	failureCallback lifecycle.StepFunc
	unblock         bool

	// apiReader is the uncached reader for the Node reads on persistBlockedList's write path: the
	// cached client would re-read the same stale resourceVersion on every retry and just exhaust
	// retry.DefaultRetry. Nil in unit tests; see reader().
	apiReader client.Reader
}

type BlockDrivesResult struct {
	Err    string `json:"err,omitempty"`
	Result string `json:"result"`
}

func NewBlockDrivesOperation(mgr ctrl.Manager, payload *weka.BlockDrivesPayload, ownerStatus *string, successCallback, failureCallback lifecycle.StepFunc) *BlockDrivesOperation {
	return &BlockDrivesOperation{
		client:          mgr.GetClient(),
		payload:         payload,
		ownerStatus:     ownerStatus,
		successCallback: successCallback,
		failureCallback: failureCallback,
		apiReader:       mgr.GetAPIReader(),
	}
}

func NewUnblockDrivesOperation(mgr ctrl.Manager, payload *weka.BlockDrivesPayload, ownerStatus *string, successCallback, failureCallback lifecycle.StepFunc) *BlockDrivesOperation {
	return &BlockDrivesOperation{
		client:          mgr.GetClient(),
		payload:         payload,
		ownerStatus:     ownerStatus,
		successCallback: successCallback,
		failureCallback: failureCallback,
		unblock:         true,
		apiReader:       mgr.GetAPIReader(),
	}
}

// reader returns the reader to use for Node reads that must observe the current resourceVersion.
// Falls back to the cached client when apiReader is unset, so unit tests that build
// BlockDrivesOperation with only a fake client keep working.
func (o *BlockDrivesOperation) reader() client.Reader {
	if o.apiReader != nil {
		return o.apiReader
	}
	return o.client
}

func (o *BlockDrivesOperation) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "BlockDrives",
		Run:  AsRunFunc(o),
	}
}

func (o *BlockDrivesOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{
			Name:            "Noop",
			Run:             o.Noop,
			Predicates:      lifecycle.Predicates{o.IsDone},
			FinishOnSuccess: true,
		},
		&lifecycle.SimpleStep{
			Name: "BlockDrives",
			Run:  o.BlockDrives,
			Predicates: lifecycle.Predicates{
				func() bool { return !o.unblock },
				func() bool { return len(o.payload.SerialIDs) > 0 },
			},
		},
		&lifecycle.SimpleStep{
			Name: "BlockSharedDrives",
			Run:  o.BlockSharedDrives,
			Predicates: lifecycle.Predicates{
				func() bool { return !o.unblock },
				func() bool { return len(o.payload.PhysicalUUIDs) > 0 },
			},
		},
		&lifecycle.SimpleStep{
			Name: "BlockVirtualDrives",
			Run:  o.BlockVirtualDrives,
			Predicates: lifecycle.Predicates{
				func() bool { return !o.unblock },
				func() bool { return len(o.payload.VirtualUUIDs) > 0 },
			},
		},
		&lifecycle.SimpleStep{
			Name: "UnblockDrives",
			Run:  o.UnblockDrives,
			Predicates: lifecycle.Predicates{
				func() bool { return o.unblock },
				func() bool { return len(o.payload.SerialIDs) > 0 },
			},
		},
		&lifecycle.SimpleStep{
			Name: "UnblockSharedDrives",
			Run:  o.UnblockSharedDrives,
			Predicates: lifecycle.Predicates{
				func() bool { return o.unblock },
				func() bool { return len(o.payload.PhysicalUUIDs) > 0 },
			},
		},
		&lifecycle.SimpleStep{
			Name: "UnblockVirtualDrives",
			Run:  o.UnblockVirtualDrives,
			Predicates: lifecycle.Predicates{
				func() bool { return o.unblock },
				func() bool { return len(o.payload.VirtualUUIDs) > 0 },
			},
		},
		&lifecycle.SimpleStep{
			Name: "SuccessCallback",
			Run:  o.SuccessCallback,
			Predicates: lifecycle.Predicates{
				o.OperationSucceeded,
			},
			FinishOnSuccess: true,
		},
		&lifecycle.SimpleStep{Name: "FailureCallback", Run: o.FailureCallback},
	}
}

// loadNodeAndBlockedList fetches the operation's target node and decodes one of its blocked-drive
// lists.
func (o *BlockDrivesOperation) loadNodeAndBlockedList(
	ctx context.Context, readBlocked func(*corev1.Node) ([]string, error),
) (*corev1.Node, []string, error) {
	node := &corev1.Node{}
	if err := o.reader().Get(ctx, types.NamespacedName{Name: o.payload.Node}, node); err != nil {
		return nil, nil, err
	}

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	blocked, err := readBlocked(node)
	if err != nil {
		return nil, nil, err
	}

	return node, blocked, nil
}

// addToBlockedList returns blocked with each entry of requested appended unless already present,
// plus the requested entries that are absent from known. Entries reported in notFound are never
// added: callers reject the whole request when notFound is non-empty.
func addToBlockedList(blocked, requested, known []string) (updated, notFound []string) {
	updated = []string{}
	updated = append(updated, blocked...)
	notFound = []string{}

	for _, id := range requested {
		if !slices.Contains(known, id) {
			notFound = append(notFound, id)
			continue
		}
		if !slices.Contains(updated, id) {
			updated = append(updated, id)
		}
	}

	return updated, notFound
}

// removeFromBlockedList returns blocked without any entry of toUnblock, plus the toUnblock entries
// that were not present.
//
// The result is a freshly allocated slice. Removing entries in place would shift blocked's backing
// array while leaving its length untouched, so a request naming several drives would corrupt the
// entries it did not name and drop only the last one it did.
func removeFromBlockedList(blocked, toUnblock []string) (remaining, notFound []string) {
	remaining = []string{}
	removed := make(map[string]bool, len(toUnblock))

	for _, entry := range blocked {
		if slices.Contains(toUnblock, entry) {
			removed[entry] = true
			continue
		}
		remaining = append(remaining, entry)
	}

	notFound = []string{}
	for _, entry := range toUnblock {
		if !removed[entry] && !slices.Contains(notFound, entry) {
			notFound = append(notFound, entry)
		}
	}

	return remaining, notFound
}

// persistBlockedList writes blocked to the node's annotation, then refreshes the node's capacity
// resources from a separate, freshly read copy.
//
// The two writes cannot be combined and cannot be ordered the other way round: Status().Update
// decodes the stored object over the one passed to it, so an annotation set but not yet saved is
// discarded by a status write that precedes its own Update. Annotations therefore go first, in
// their own call, and setNodeCapacity runs against a fresh read through the uncached reader() under
// conflict retry — the same shape as sign_drives.go's updateNodeDriveCapacity, which also leaves
// Status stale-but-consistent with what is annotated should the status write fail.
//
// setNodeCapacity is nil for identifiers that leave the node's physical drive inventory alone
// (virtual UUIDs), which skips the status write entirely. clearSignHash drops
// weka.io/sign-drives-hash to force a drive re-scan on the next sign-drives run.
func (o *BlockDrivesOperation) persistBlockedList(
	ctx context.Context, node *corev1.Node, annotation string, blocked []string,
	setNodeCapacity func(node *corev1.Node) error, clearSignHash bool,
) error {
	raw, err := json.Marshal(blocked)
	if err != nil {
		return fmt.Errorf("failed to marshal %s: %w", annotation, err)
	}

	node.Annotations[annotation] = string(raw)
	if clearSignHash {
		delete(node.Annotations, consts.AnnotationSignDrivesHash)
	}

	if updateErr := o.client.Update(ctx, node); updateErr != nil {
		return fmt.Errorf("error updating node annotations: %w", updateErr)
	}

	if setNodeCapacity == nil {
		return nil
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &corev1.Node{}
		if getErr := o.reader().Get(ctx, types.NamespacedName{Name: node.Name}, cur); getErr != nil {
			return getErr
		}
		if capErr := setNodeCapacity(cur); capErr != nil {
			return capErr
		}
		return o.client.Status().Update(ctx, cur)
	})
	if err != nil {
		return fmt.Errorf("error updating node status: %w", err)
	}

	return nil
}

// reportNotFound records unrecognised identifiers as the operation's error and succeeds the step:
// the problem is reported through the operation's result rather than by failing the reconcile.
func (o *BlockDrivesOperation) reportNotFound(
	logger *instrumentation.SpanLogger, notFound []string, listName, action string, hints ...string,
) error {
	err := fmt.Errorf("the following drives were not found in the %s: %v", listName, notFound)
	logger.Error(err, "Failed to "+action+" drives")

	msg := err.Error()
	for _, hint := range hints {
		msg += " (" + hint + ")"
	}
	o.recordErr(errors.New(msg))

	return nil
}

// recordErr keeps the first error across handlers. A payload naming more than one kind of
// identifier runs a handler per kind, and a later success must not erase an earlier failure.
func (o *BlockDrivesOperation) recordErr(err error) {
	if o.results.Err == "" {
		o.results.Err = err.Error()
	}
}

// recordResult appends one handler's summary, so a payload naming several kinds of identifier
// reports what happened to each rather than only the last.
func (o *BlockDrivesOperation) recordResult(msg string) {
	if o.results.Result == "" {
		o.results.Result = msg
		return
	}
	o.results.Result += "; " + msg
}

// readNodeDriveSerials returns every drive serial the node reports, across the current and legacy
// annotations.
func readNodeDriveSerials(node *corev1.Node) ([]string, error) {
	return domain.ReadAnnotatedDriveSerials(
		node.Annotations[consts.AnnotationWekaFullDrives],
		node.Annotations[consts.AnnotationWekaDrives],
	)
}

// sharedDrivePhysicalUUIDs returns the physical UUID of every shared drive the node reports.
func sharedDrivePhysicalUUIDs(node *corev1.Node) ([]string, error) {
	drives, _, err := domain.ReadNodeSharedDrives(node)
	if err != nil {
		return nil, err
	}
	uuids := make([]string, 0, len(drives))
	for _, d := range drives {
		uuids = append(uuids, d.PhysicalUUID)
	}
	return uuids, nil
}

// setDriveCountCapacity recomputes the weka.io/drives count from the node's own annotations, so the
// value published reflects the blocked list as actually persisted.
func setDriveCountCapacity(node *corev1.Node) error {
	allDrives, err := readNodeDriveSerials(node)
	if err != nil {
		return err
	}

	blocked, err := domain.ReadBlockedDriveSerials(node)
	if err != nil {
		return err
	}

	domain.SetNodeDriveAllocatable(node, allDrives, blocked)

	return nil
}

// setSharedDriveCapacity recomputes the shared-drive capacity resources (TLC + QLC) from the node's
// own annotations. Drives blocked by serial are excluded alongside those blocked by physical UUID,
// or a serial-blocked drive's capacity reappears the next time this UUID-keyed path runs.
func setSharedDriveCapacity(node *corev1.Node) error {
	sharedDrives, _, err := domain.ReadNodeSharedDrives(node)
	if err != nil {
		return err
	}

	blockedUUIDs, err := domain.ReadBlockedDrivePhysicalUUIDs(node)
	if err != nil {
		return err
	}

	blockedSerials, err := domain.ReadBlockedDriveSerials(node)
	if err != nil {
		return err
	}

	domain.SetSharedDriveCapacityResources(node, sharedDrives, blockedUUIDs, blockedSerials)

	return nil
}

func (o *BlockDrivesOperation) UnblockDrives(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "UnblockDrives", "node", o.payload.Node)
	defer logger.End()

	node, blockedDrives, err := o.loadNodeAndBlockedList(ctx, domain.ReadBlockedDriveSerials)
	if err != nil {
		return err
	}

	logger.Debug("Blocked drives", "drives", blockedDrives)

	updatedBlockedDrives, notFoundDrives := removeFromBlockedList(blockedDrives, o.payload.SerialIDs)
	if len(notFoundDrives) > 0 {
		return o.reportNotFound(logger, notFoundDrives, "blocked drives list", "unblock")
	}

	if err := o.persistBlockedList(
		ctx, node, consts.AnnotationBlockedDrives, updatedBlockedDrives, setDriveCountCapacity, false,
	); err != nil {
		return err
	}

	o.recordResult(fmt.Sprintf("Successfully unblocked %d drives on node %s", len(o.payload.SerialIDs), o.payload.Node))

	return nil
}

func (o *BlockDrivesOperation) BlockDrives(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "BlockDrives", "node", o.payload.Node)
	defer logger.End()

	node, blockedDrives, err := o.loadNodeAndBlockedList(ctx, domain.ReadBlockedDriveSerials)
	if err != nil {
		return err
	}

	allDrives, err := readNodeDriveSerials(node)
	if err != nil {
		return err
	}

	logger.Debug("Available drives", "drives", allDrives)
	logger.Debug("Blocked drives", "drives", blockedDrives)

	updatedBlockedDrives, notFoundDrives := addToBlockedList(blockedDrives, o.payload.SerialIDs, allDrives)
	if len(notFoundDrives) > 0 {
		return o.reportNotFound(logger, notFoundDrives, "available drives list", "block")
	}

	if err := o.persistBlockedList(
		ctx, node, consts.AnnotationBlockedDrives, updatedBlockedDrives, setDriveCountCapacity, true,
	); err != nil {
		return err
	}

	o.recordResult(fmt.Sprintf("Successfully blocked %d drives on node %s", len(o.payload.SerialIDs), o.payload.Node))

	return nil
}

func (o *BlockDrivesOperation) BlockSharedDrives(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "BlockSharedDrives", "node", o.payload.Node)
	defer logger.End()

	node, blockedDriveUuids, err := o.loadNodeAndBlockedList(ctx, domain.ReadBlockedDrivePhysicalUUIDs)
	if err != nil {
		return err
	}

	allSharedDriveUuids, err := sharedDrivePhysicalUUIDs(node)
	if err != nil {
		return err
	}

	logger.Debug("Available shared drives", "shared_drive_uuids", allSharedDriveUuids)
	logger.Debug("Blocked drive uuids", "blocked_drive_uuids", blockedDriveUuids)

	updatedBlockedDriveUuids, notFoundDrives := addToBlockedList(blockedDriveUuids, o.payload.PhysicalUUIDs, allSharedDriveUuids)
	if len(notFoundDrives) > 0 {
		return o.reportNotFound(logger, notFoundDrives, "available drives list", "block")
	}

	if err := o.persistBlockedList(
		ctx, node, consts.AnnotationBlockedDrivesPhysicalUuids, updatedBlockedDriveUuids, setSharedDriveCapacity, true,
	); err != nil {
		return err
	}

	o.recordResult(fmt.Sprintf("Successfully blocked %d drives on node %s", len(o.payload.PhysicalUUIDs), o.payload.Node))

	return nil
}

func (o *BlockDrivesOperation) UnblockSharedDrives(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "UnblockSharedDrives", "node", o.payload.Node)
	defer logger.End()

	node, blockedDriveUuids, err := o.loadNodeAndBlockedList(ctx, domain.ReadBlockedDrivePhysicalUUIDs)
	if err != nil {
		return err
	}

	logger.Debug("Blocked drive uuids", "blocked_drive_uuids", blockedDriveUuids)

	updatedBlockedDriveUuids, notFoundDrives := removeFromBlockedList(blockedDriveUuids, o.payload.PhysicalUUIDs)
	if len(notFoundDrives) > 0 {
		return o.reportNotFound(logger, notFoundDrives, "blocked drives list", "unblock")
	}

	if err := o.persistBlockedList(
		ctx, node, consts.AnnotationBlockedDrivesPhysicalUuids, updatedBlockedDriveUuids, setSharedDriveCapacity, false,
	); err != nil {
		return err
	}

	o.recordResult(fmt.Sprintf("Successfully unblocked %d drives on node %s", len(o.payload.PhysicalUUIDs), o.payload.Node))

	return nil
}

// buildNodeClaimedVids returns the virtual UUIDs claimed by the allocation record of every
// WekaContainer on the node.
//
// That set is exactly what a virtual-UUID block can act on: removal works by dropping the VID's
// entry from its owning container's record, so a VID no record claims is one no block can affect.
// A VID signed on the proxy but claimed by nobody is the clean-stale-virtual-drives operation's job.
//
// Read through the cache. A stale read cannot do damage here: a VID allocated moments ago reads as
// unknown and the request is rejected having written nothing, and one released moments ago records a
// blocked entry that matches nothing. clean-stale-virtual-drives needs an uncached read of the same
// set only because it deletes on the strength of it.
func (o *BlockDrivesOperation) buildNodeClaimedVids(ctx context.Context, nodeName string) ([]string, error) {
	containerList := &weka.WekaContainerList{}
	if err := o.client.List(ctx, containerList); err != nil {
		return nil, fmt.Errorf("failed to list WekaContainers: %w", err)
	}

	claimed := []string{}
	for i := range containerList.Items {
		container := &containerList.Items[i]
		if string(container.GetNodeAffinity()) != nodeName {
			continue
		}
		if container.Status.Allocations == nil {
			continue
		}
		for _, vid := range container.Status.Allocations.GetVirtualDrivesUuids() {
			if !slices.Contains(claimed, vid) {
				claimed = append(claimed, vid)
			}
		}
	}

	return claimed, nil
}

func (o *BlockDrivesOperation) BlockVirtualDrives(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "BlockVirtualDrives", "node", o.payload.Node)
	defer logger.End()

	node, blockedVids, err := o.loadNodeAndBlockedList(ctx, domain.ReadBlockedDriveVirtualUUIDs)
	if err != nil {
		return err
	}

	claimedVids, err := o.buildNodeClaimedVids(ctx, o.payload.Node)
	if err != nil {
		return err
	}

	logger.Debug("Claimed virtual drives", "virtual_uuids", claimedVids)
	logger.Debug("Blocked virtual drives", "virtual_uuids", blockedVids)

	updatedBlockedVids, notFoundVids := addToBlockedList(blockedVids, o.payload.VirtualUUIDs, claimedVids)
	if len(notFoundVids) > 0 {
		return o.reportNotFound(logger, notFoundVids, "allocation records of containers on this node", "block",
			"a virtual drive signed on the proxy but claimed by no container is removed by the clean-stale-virtual-drives operation, not by block-drives")
	}

	// No capacity recompute and no sign-drives-hash reset: the node's physical drive inventory is
	// unchanged, so neither its capacity resources nor a drive re-scan are affected. Forcing a
	// re-scan would touch the proxy, disturbing the neighbouring VIDs this operation exists to spare.
	if err := o.persistBlockedList(
		ctx, node, consts.AnnotationBlockedDrivesVirtualUuids, updatedBlockedVids, nil, false,
	); err != nil {
		return err
	}

	o.recordResult(fmt.Sprintf("Successfully blocked %d virtual drives on node %s", len(o.payload.VirtualUUIDs), o.payload.Node))

	// Warn before anything is deleted rather than only once the removal runs: with scaling off the
	// drive goes away and nothing ever carves a replacement, which is not what blocking a single VID
	// is normally asked for.
	if !config.Config.DriveSharing.EnableDynamicDriveScaling {
		o.recordResult("WARNING: no replacement will be created because dynamic drive scaling is disabled " +
			"(ENABLE_DYNAMIC_DRIVE_SCALING_FOR_SHARED_DRIVES); the container will stay below its target capacity")
	}

	return nil
}

func (o *BlockDrivesOperation) UnblockVirtualDrives(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "UnblockVirtualDrives", "node", o.payload.Node)
	defer logger.End()

	node, blockedVids, err := o.loadNodeAndBlockedList(ctx, domain.ReadBlockedDriveVirtualUUIDs)
	if err != nil {
		return err
	}

	logger.Debug("Blocked virtual drives", "virtual_uuids", blockedVids)

	// Validated against the blocked list alone, never against what the node still claims: a retired
	// virtual UUID exists nowhere any more, so requiring it to be known would make unblocking the
	// drives this operation just replaced impossible.
	updatedBlockedVids, notFoundVids := removeFromBlockedList(blockedVids, o.payload.VirtualUUIDs)
	if len(notFoundVids) > 0 {
		return o.reportNotFound(logger, notFoundVids, "blocked drives list", "unblock")
	}

	if err := o.persistBlockedList(
		ctx, node, consts.AnnotationBlockedDrivesVirtualUuids, updatedBlockedVids, nil, false,
	); err != nil {
		return err
	}

	o.recordResult(fmt.Sprintf("Successfully unblocked %d virtual drives on node %s", len(o.payload.VirtualUUIDs), o.payload.Node))

	return nil
}

func (o *BlockDrivesOperation) GetResult() BlockDrivesResult {
	return o.results
}

func (o *BlockDrivesOperation) GetJsonResult() string {
	resultJSON, err := json.Marshal(o.results)
	if err != nil {
		return ""
	}
	return string(resultJSON)
}

func (o *BlockDrivesOperation) IsDone() bool {
	return o.ownerStatus != nil && *o.ownerStatus == "Done"
}

func (o *BlockDrivesOperation) OperationSucceeded() bool {
	return o.results.Err == ""
}

func (o *BlockDrivesOperation) SuccessCallback(ctx context.Context) error {
	if o.successCallback == nil {
		return nil
	}
	return o.successCallback(ctx)
}

func (o *BlockDrivesOperation) FailureCallback(ctx context.Context) error {
	if o.failureCallback == nil {
		return nil
	}
	return o.failureCallback(ctx)
}

func (o *BlockDrivesOperation) Noop(ctx context.Context) error {
	return nil
}
