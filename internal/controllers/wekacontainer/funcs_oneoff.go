// This file contains functions related to one-off containers, such as drivers builder, drivers loader, sign drives, discover drives operations
package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
)

func (r *containerReconcilerLoop) fetchResults(ctx context.Context) error {
	container := r.container

	if container.Status.ExecutionResult != nil {
		return nil
	}

	executor, err := r.ExecService.GetExecutor(ctx, container)
	if err != nil {
		return nil
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "FetchResults", []string{"cat", "/weka-runtime/results.json"})
	if err != nil {
		return fmt.Errorf("Error fetching results, stderr: %s", stderr.String())
	}

	result := stdout.String()
	if result == "" {
		return errors.New("Empty result")
	}

	// update container to set execution result on container object
	container.Status.ExecutionResult = &result
	err = r.Status().Update(ctx, container)
	if err != nil {
		return err
	}

	return nil
}

func (r *containerReconcilerLoop) cleanupFinishedOneOff(ctx context.Context) error {
	if r.container.IsDriversBuilder() || r.isSignOrDiscoverDrivesOperation(ctx) {
		if r.pod != nil {
			return r.Delete(ctx, r.pod)
		}
	}
	if r.container.IsDriversLoaderMode() {
		for _, c := range r.container.Status.Conditions {
			if c.Type == condition.CondResultsProcessed && c.Status == metav1.ConditionTrue {
				if time.Since(c.LastTransitionTime.Time) > time.Minute*5 {
					return r.Delete(ctx, r.container)
				}
			}
		}
	}
	// Cleanup feature-flags containers after results are processed
	// These containers are created by GetFeatureFlagsOperation and should be cleaned up
	// after the feature flags have been cached (which happens when results are processed)
	if r.isFeatureFlagsOperation() {
		for _, c := range r.container.Status.Conditions {
			if c.Type == condition.CondResultsProcessed && c.Status == metav1.ConditionTrue {
				// Give a short grace period before cleanup to ensure cache is populated
				if time.Since(c.LastTransitionTime.Time) > time.Second*30 {
					return r.Delete(ctx, r.container)
				}
			}
		}
	}

	return nil
}

func (r *containerReconcilerLoop) isFeatureFlagsOperation() bool {
	return r.container.Spec.Mode == weka.WekaContainerModeAdhocOpWC &&
		r.container.Spec.Instructions != nil &&
		r.container.Spec.Instructions.Type == weka.InstructionTypeFeatureFlagsUpdate
}

// reportAdhocPodNotProgressing surfaces an abnormal not-progressing adhoc-op pod (Unschedulable,
// ImagePullBackOff, CrashLoopBackOff, config errors, ...) as a throttled warning, so operators
// don't have to wait for the deletion timeout to learn something is wrong.
func (r *containerReconcilerLoop) reportAdhocPodNotProgressing(ctx context.Context) error {
	reason, detail := podNotRunningReason(r.pod)
	if isStartingUpReason(reason) {
		return nil
	}

	_ = r.RecordEventThrottled( //nolint:errcheck // best-effort event
		v1.EventTypeWarning,
		"AdhocPodNotProgressing",
		fmt.Sprintf("Adhoc-op pod stuck (%s) for %s, will delete container after %s%s",
			reason, time.Since(podStuckSince(r.pod)).Round(time.Second),
			config.Config.StuckAdhocPodTimeout, eventDetailSuffix(detail)),
		time.Minute,
	)
	return nil
}

// deleteStuckAdhocContainer marks an adhoc-op container for deletion once its pod has failed to
// produce a result for too long. Adhoc-op containers are node-pinned and only produce a result
// once their pod runs, so a pod that can never run would otherwise leak the CR forever: cleanup
// is gated on CondResultsProcessed, set only after fetchResults execs into a running pod.
func (r *containerReconcilerLoop) deleteStuckAdhocContainer(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	reason, detail := podNotRunningReason(r.pod)
	stuckFor := time.Since(podStuckSince(r.pod)).Round(time.Second)

	_ = r.RecordEvent( //nolint:errcheck // best-effort event
		v1.EventTypeWarning,
		"AdhocPodStuck",
		fmt.Sprintf("Adhoc-op pod stuck (%s) for %s, deleting container%s",
			reason, stuckFor, eventDetailSuffix(detail)),
	)
	logger.Info("Deleting stuck adhoc-op container", "reason", reason, "detail", detail, "stuck_for", stuckFor)
	return services.SetContainerStateDeleting(ctx, r.container, r.Client)
}

// adhocPodNotProgressing reports whether an adhoc-op pod is not doing useful work:
// either its phase is not Running, or the phase is Running but a container is stuck
// in a restart backoff after a failed run.
func (r *containerReconcilerLoop) adhocPodNotProgressing() bool {
	return r.pod.Status.Phase != v1.PodRunning || podCrashLoopingAfterFailure(r.pod)
}

// adhocPodStuckTimeoutElapsed reports whether the pod has been stuck long enough to
// delete the container.
func (r *containerReconcilerLoop) adhocPodStuckTimeoutElapsed() bool {
	return podStuckTimeoutElapsed(
		r.pod,
		time.Now(),
		config.Config.StuckAdhocPodTimeout,
		config.Config.StuckAdhocPodStartingTimeout,
	)
}

// podStuckTimeoutElapsed reports whether the pod has been stuck past the applicable
// timeout. A pod that is still starting up gets the longer startingTimeout: a
// first-time pull of the weka image on a cold node can legitimately outlast the
// timeout used for hard failures.
func podStuckTimeoutElapsed(pod *v1.Pod, now time.Time, timeout, startingTimeout time.Duration) bool {
	reason, _ := podNotRunningReason(pod)
	if isStartingUpReason(reason) {
		timeout = startingTimeout
	}
	return now.Sub(podStuckSince(pod)) > timeout
}

// podStuckSince returns the point in time from which the pod stopped making progress toward
// producing a result. For a pod that never ran or that crash-loops, that is its creation time.
// For a pod that ran and then went terminal (evicted, node lost), creation time could be hours
// in the past and report a misleading duration, so the ContainersReady transition is used
// instead - except for a crash-looping pod, where it flaps on every restart and would reset
// the clock.
func podStuckSince(pod *v1.Pod) time.Time {
	if pod.Status.Phase == v1.PodFailed || pod.Status.Phase == v1.PodSucceeded {
		if c := podCondition(pod, v1.ContainersReady); c != nil &&
			c.Status == v1.ConditionFalse && !c.LastTransitionTime.IsZero() {
			return c.LastTransitionTime.Time
		}
	}
	return pod.CreationTimestamp.Time
}

// podCrashLoopingAfterFailure reports whether one of the pod's containers is waiting in a
// restart backoff after a failed run. The exit-code check is load-bearing: adhoc-op pods get
// the API default RestartPolicy Always, so a one-off command that *succeeds* is restarted too
// and eventually also reports CrashLoopBackOff - those must not be deleted here, they're reaped
// by cleanupFinishedOneOff earlier in the flow. Init containers need no handling: a
// crash-looping init container keeps the pod phase at Pending, which is already covered elsewhere.
func podCrashLoopingAfterFailure(pod *v1.Pod) bool {
	for i := range pod.Status.ContainerStatuses {
		status := &pod.Status.ContainerStatuses[i]
		if w := status.State.Waiting; w == nil || w.Reason != "CrashLoopBackOff" {
			continue
		}
		if t := status.LastTerminationState.Terminated; t != nil && t.ExitCode != 0 {
			return true
		}
	}
	return false
}

// isStartingUpReason reports whether a pod's not-running reason means it is still
// legitimately starting up, as opposed to a hard failure (Unschedulable,
// ImagePullBackOff, CrashLoopBackOff, config errors, ...).
func isStartingUpReason(reason string) bool {
	switch reason {
	case "Pending", "ContainerCreating", "PodInitializing":
		return true
	default:
		return false
	}
}

// podNotRunningReason returns a short reason describing why the pod is not producing a result,
// plus an optional longer detail for events and logs. Precedence: first init/container
// Waiting.Reason; then, for a terminal pod, a container Terminated.Reason or pod-level reason;
// then an explicit PodScheduled failure (Unschedulable); else the phase.
func podNotRunningReason(pod *v1.Pod) (reason, detail string) {
	statusLists := [][]v1.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses}
	for _, statuses := range statusLists {
		for i := range statuses {
			if w := statuses[i].State.Waiting; w != nil && w.Reason != "" {
				return w.Reason, w.Message
			}
		}
	}

	if pod.Status.Phase == v1.PodFailed || pod.Status.Phase == v1.PodSucceeded {
		for _, statuses := range statusLists {
			for i := range statuses {
				if t := statuses[i].State.Terminated; t != nil && t.Reason != "" {
					return t.Reason, t.Message
				}
			}
		}
		// Pod-level failure with no container status, e.g. an eviction.
		if pod.Status.Reason != "" {
			return pod.Status.Reason, pod.Status.Message
		}
	}

	// No container reason yet (e.g. pod not scheduled). Surface an explicit scheduling
	// failure if present, else fall back to phase.
	for i := range pod.Status.Conditions {
		if c := &pod.Status.Conditions[i]; c.Type == v1.PodScheduled &&
			c.Status == v1.ConditionFalse && c.Reason != "" {
			return c.Reason, c.Message
		}
	}
	return string(pod.Status.Phase), ""
}

// podCondition returns the pod condition of the given type, or nil.
func podCondition(pod *v1.Pod, condType v1.PodConditionType) *v1.PodCondition {
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == condType {
			return &pod.Status.Conditions[i]
		}
	}
	return nil
}

// eventDetailSuffix formats an optional detail message for inclusion in an event,
// truncated because Kubernetes caps event messages at 1024 characters.
func eventDetailSuffix(detail string) string {
	const maxDetailLen = 200
	if detail == "" {
		return ""
	}
	if len(detail) > maxDetailLen {
		detail = detail[:maxDetailLen] + "..."
	}
	return ": " + detail
}

func (r *containerReconcilerLoop) isSignOrDiscoverDrivesOperation(ctx context.Context) bool {
	if r.container.Spec.Mode == weka.WekaContainerModeAdhocOp && r.container.Spec.Instructions != nil {
		return r.container.Spec.Instructions.Type == weka.InstructionTypeSignDrives ||
			r.container.Spec.Instructions.Type == weka.InstructionTypeDiscoverDrives
	}
	return false
}

func (r *containerReconcilerLoop) processResults(ctx context.Context) error {
	switch {
	case r.container.IsDriversBuilder():
		return r.UploadBuiltDrivers(ctx)
	case r.isSignOrDiscoverDrivesOperation(ctx):
		return r.updateNodeAnnotations(ctx)
	default:
		return nil
	}
}

func (r *containerReconcilerLoop) updateNodeAnnotations(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "updateNodeAnnotations")
	defer logger.End()

	container := r.container
	node := r.node

	if node == nil {
		return errors.New("node is not set")
	}

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	var opResult *operations.DriveNodeResults
	err := json.Unmarshal([]byte(*container.Status.ExecutionResult), &opResult)
	if err != nil {
		err = fmt.Errorf("error unmarshalling execution result: %w", err)
		return err
	}

	// Check if this is a proxy (shared) mode operation. For sign-drives, prefer the "shared" flag
	// from the instruction payload over inferring it from ProxyDrives length: a shared run that
	// legitimately signs zero proxy drives would otherwise fall through to the non-proxy branch
	// and corrupt the node's drive bookkeeping. Fall back to the length check when the payload
	// is absent/unparseable, or the instruction is discover-drives (no "shared" field).
	isProxyMode := len(opResult.ProxyDrives) > 0
	if r.container.Spec.Instructions != nil && r.container.Spec.Instructions.Type == weka.InstructionTypeSignDrives {
		var payload weka.SignDrivesPayload
		// Decoded into a local rather than the enclosing err: the payload is operator-generated,
		// so a parse failure here is our own bug — logged, not swallowed.
		if payloadErr := json.Unmarshal([]byte(r.container.Spec.Instructions.Payload), &payload); payloadErr != nil {
			logger.Warn("Failed to parse sign-drives instruction payload; inferring proxy mode from reported drives", "error", payloadErr, "proxyDrives", len(opResult.ProxyDrives))
		} else {
			isProxyMode = payload.Shared
		}
	}

	if isProxyMode {
		return r.updateProxyModeAnnotations(ctx, node, opResult)
	}

	// Update weka.io/weka-drives annotation (regular mode)
	newDrivesFound := 0

	// Build a map from raw drives for capacity lookup
	rawDriveCapacity := make(map[string]int)
	for _, raw := range opResult.RawDrives {
		if raw.SerialId != "" {
			if raw.CapacityGiB == 0 {
				return fmt.Errorf("drive %s in RawDrives has zero capacity", raw.SerialId)
			}
			rawDriveCapacity[raw.SerialId] = raw.CapacityGiB
		}
	}

	// Seed from weka-full-drives only — preserves existing entries with real capacity.
	// Legacy-only annotation entries are intentionally not carried forward here;
	// they will be re-discovered with proper capacity by this discovery run.
	seenDrives := make(map[string]domain.DriveEntry)
	fullAnnotation := node.Annotations[consts.AnnotationWekaFullDrives]
	wasFullDrivesAbsent := fullAnnotation == ""

	if fullAnnotation != "" {
		existingEntries, readErr := domain.ReadDriveAnnotations(fullAnnotation)
		if readErr != nil {
			return fmt.Errorf("error reading existing drive annotations: %w", readErr)
		}
		for _, entry := range existingEntries {
			if entry.Serial != "" {
				seenDrives[entry.Serial] = entry
			}
		}
	}

	complete := func() error {
		r.container.Status.Status = weka.Completed
		return r.Status().Update(ctx, r.container)
	}

	for _, drive := range opResult.Drives {
		if drive.SerialId == "" { // skip drives without serial id if it was not set for whatever reason
			continue
		}
		capacity, ok := rawDriveCapacity[drive.SerialId]
		if !ok {
			return fmt.Errorf("drive %s present in Drives but missing from RawDrives", drive.SerialId)
		}
		if _, ok := seenDrives[drive.SerialId]; !ok {
			newDrivesFound++
		}
		// capacity is guaranteed > 0 by the RawDrives validation above
		seenDrives[drive.SerialId] = domain.DriveEntry{Serial: drive.SerialId, CapacityGiB: capacity}
	}

	if newDrivesFound == 0 {
		logger.Info("No new drives found")
	}

	updatedDrivesList := make([]domain.DriveEntry, 0, len(seenDrives))
	for _, entry := range seenDrives {
		updatedDrivesList = append(updatedDrivesList, entry)
	}
	newDrivesStr, err := json.Marshal(updatedDrivesList)
	if err != nil {
		err = fmt.Errorf("error marshalling updated drives list: %w", err)
		return err
	}

	// Write new annotation with full drive metadata
	node.Annotations[consts.AnnotationWekaFullDrives] = string(newDrivesStr)

	// calculate hash, based on o.node.Status.NodeInfo.BootID
	node.Annotations[consts.AnnotationSignDrivesHash] = domain.CalculateNodeDriveSignHash(node)

	blockedDrives := []string{}
	if blockedDrivesStr, ok := node.Annotations[consts.AnnotationBlockedDrives]; ok {
		err = json.Unmarshal([]byte(blockedDrivesStr), &blockedDrives)
		if err != nil {
			err = fmt.Errorf("error unmarshalling blocked drives: %w", err)
			return err
		}
	}

	for _, drive := range opResult.RawDrives {
		if drive.IsMounted {
			if _, ok := seenDrives[drive.SerialId]; ok {
				// We found mounted drive that previously was used for weka
				// We should block it from being used in the future
				// check if already in blocked list, and if not add it
				if !slices.Contains(blockedDrives, drive.SerialId) {
					blockedDrives = append(blockedDrives, drive.SerialId)
					logger.Info("Blocking drive", "serial_id", drive.SerialId, "reason", "drive is mounted")
				}
			}
		}
	}

	annotatedSerials := make([]string, 0, len(seenDrives))
	for s := range seenDrives {
		annotatedSerials = append(annotatedSerials, s)
	}
	var missingDrives []string
	blockedDrives, missingDrives = appendMissingDrivesToBlocked(annotatedSerials, opResult, blockedDrives)
	for _, s := range missingDrives {
		logger.Info("Blocking missing drive", "serial_id", s)
	}

	availableDrives := 0
	for _, entry := range updatedDrivesList {
		if !slices.Contains(blockedDrives, entry.Serial) {
			availableDrives++
		}
	}

	// Update weka.io/drives extended resource
	node.Status.Capacity[consts.ResourceDrives] = *resource.NewQuantity(int64(availableDrives), resource.DecimalSI)
	node.Status.Allocatable[consts.ResourceDrives] = *resource.NewQuantity(int64(availableDrives), resource.DecimalSI)
	// marshal blocked drives back and update annotation
	blockedDrivesStr, err := json.Marshal(blockedDrives)
	if err != nil {
		err = fmt.Errorf("error marshalling blocked drives: %w", err)
		return err
	}
	node.Annotations[consts.AnnotationBlockedDrives] = string(blockedDrivesStr)

	// Skip allocatable update when weka-full-drives was absent AND no kernel drives found —
	// the result is provably incomplete (in-Weka drives not visible to kernel).
	// UpdateFullDrivesAnnotationFromAddedDrives will set allocatable once it has the full picture.
	if !wasFullDrivesAbsent || len(updatedDrivesList) > 0 {
		domain.SetNodeDriveAllocatable(node, domain.DriveEntrySerials(updatedDrivesList), blockedDrives)
	}

	if err := r.Status().Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node status: %w", err)
		return err
	}

	if err := r.Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node annotations: %w", err)
		return err
	}
	return complete()
}

func (r *containerReconcilerLoop) updateProxyModeAnnotations(ctx context.Context, node *v1.Node, opResult *operations.DriveNodeResults) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "updateProxyModeAnnotations")
	defer logger.End()

	logger.Info("Updating node annotations for proxy mode")

	blockedDrives := []string{}
	if blockedDrivesStr, ok := node.Annotations[consts.AnnotationBlockedDrives]; ok {
		if err := json.Unmarshal([]byte(blockedDrivesStr), &blockedDrives); err != nil {
			return fmt.Errorf("error unmarshalling blocked drives: %w", err)
		}
	}

	// Read existing shared drives from annotation. A malformed annotation is a hard error, not a
	// silent empty read: the merge below writes this list back, so discarding the parse error
	// would rewrite the annotation from this run's report alone — dropping every drive the run
	// didn't re-report, including the persisted Model that model-based overrides match on.
	// The "signed" result is discarded: the capacity guard further down needs mere annotation
	// presence, not signed-ness.
	existingDrives, _, err := domain.ReadNodeSharedDrives(node)
	if err != nil {
		return fmt.Errorf("error reading existing shared drives: %w", err)
	}
	_, hadSharedDrivesAnnotation := node.Annotations[consts.AnnotationSharedDrives]

	// Build map keyed by serial for efficient merge
	drivesBySerial := make(map[string]domain.SharedDriveInfo)
	// annotatedTypes snapshots the type each drive carried in the annotation BEFORE this run's
	// report is merged in. warnOverriddenDriveTypes compares against it rather than the post-merge
	// state: the agent always reports the IU-derived type and MergeSharedDriveInfo prefers a
	// non-empty incoming Type, so an overridden drive looks "changed" on every single re-sign
	// forever. Comparing against what was persisted makes the warning fire only when a drive's type
	// genuinely moved, which is when virtual drives carved from it can actually disagree with it.
	annotatedTypes := make(map[string]string, len(existingDrives))
	for _, drive := range existingDrives {
		if drive.Serial != "" {
			drivesBySerial[drive.Serial] = drive
			annotatedTypes[drive.Serial] = drive.Type
		}
	}

	// Merge new results: update existing or add new drives; does NOT delete drives absent from
	// opResult.ProxyDrives. Existing entries merge field-wise via domain.MergeSharedDriveInfo
	// rather than being overwritten, so an agent that fails to report Model never erases a
	// persisted Model and silently disarms model-based overrides.
	newDrivesFound := 0
	for _, drive := range opResult.ProxyDrives {
		if drive.Serial == "" {
			continue // skip drives without serial
		}
		if existing, exists := drivesBySerial[drive.Serial]; exists {
			drivesBySerial[drive.Serial] = domain.MergeSharedDriveInfo(existing, drive)
		} else {
			newDrivesFound++
			drivesBySerial[drive.Serial] = drive
		}
	}

	if newDrivesFound == 0 {
		logger.Info("No new drives found")
	}

	annotatedSerials := make([]string, 0, len(drivesBySerial))
	for s := range drivesBySerial {
		annotatedSerials = append(annotatedSerials, s)
	}
	slices.Sort(annotatedSerials)
	var missingDrives []string
	blockedDrives, missingDrives = appendMissingDrivesToBlocked(annotatedSerials, opResult, blockedDrives)
	for _, s := range missingDrives {
		logger.Info("Blocking missing drive", "serial_id", s)
	}

	// Built from the sorted serials above so the marshalled annotation is deterministic across
	// runs — map iteration order alone would make weka.io/weka-shared-drives look "changed" on
	// every pass even when the drive set is identical.
	mergedDrives := make([]domain.SharedDriveInfo, 0, len(annotatedSerials))
	for _, s := range annotatedSerials {
		mergedDrives = append(mergedDrives, drivesBySerial[s])
	}

	// Re-apply any persisted drive-type override rules before computing capacities, so the
	// TLC/QLC split below (and the annotation we write) reflect the override.
	rules, err := domain.ReadDriveTypeOverrides(node)
	if err != nil {
		return fmt.Errorf("error reading drive type overrides: %w", err)
	}
	if len(rules) > 0 {
		var changed int
		var unmatchedRules []int
		mergedDrives, changed, unmatchedRules = domain.ApplyDriveTypeOverrides(mergedDrives, rules)
		if changed > 0 {
			logger.Info("Applied drive type overrides", "changed", changed)
			// Name the drives whose type flipped relative to what was persisted — virtual drives
			// already allocated from them keep their recorded type until their containers are
			// reallocated. Compared against annotatedTypes, not the post-merge state, so a re-sign
			// that merely re-derives the same override does not re-warn (see annotatedTypes above).
			warnOverriddenDriveTypes(logger, annotatedTypes, mergedDrives)
		}
		for _, idx := range unmatchedRules {
			rule := rules[idx]
			logger.Warn("Drive type override rule matched no drive", "ruleIndex", idx, "model", rule.Model, "capacityGiB", rule.CapacityGiB, "type", rule.Type)
		}
	}

	// A proxy-mode run that ends with zero known shared drives is only trustworthy when the kernel
	// view was complete. With an incomplete view we must not persist an empty annotation together
	// with a fresh weka.io/sign-drives-hash: sign_drives.go skips nodes on non-force runs once their
	// hash matches, so writing that pair here would permanently mark the node "signed" with zero
	// capacity even once a later, complete scan finds drives.
	//
	// This is a terminal failure, NOT a WaitError: fetchResults short-circuits on a non-nil
	// container.Status.ExecutionResult, so every later reconcile would just re-derive this same
	// verdict from the same frozen results.json — the implied wait would never end.
	if len(mergedDrives) == 0 && !opResult.KernelViewComplete {
		err = fmt.Errorf("proxy mode drive discovery for node %s has an incomplete kernel view and produced zero shared drives; refusing to persist an empty %s annotation and a fresh %s to avoid permanently locking the node out of future re-scans. Check that this node's NVMe devices are bound to the kernel \"nvme\" driver, then re-run sign-drives with force",
			node.Name, consts.AnnotationSharedDrives, consts.AnnotationSignDrivesHash)
		if eventErr := r.RecordEvent(v1.EventTypeWarning, "IncompleteKernelView", err.Error()); eventErr != nil {
			logger.Warn("Failed to record IncompleteKernelView event", "error", eventErr)
		}
		return err
	}

	// Write merged proxy drives to annotation
	proxyDrivesJSON, err := json.Marshal(mergedDrives)
	if err != nil {
		return fmt.Errorf("error marshalling proxy drives: %w", err)
	}

	node.Annotations[consts.AnnotationSharedDrives] = string(proxyDrivesJSON)
	node.Annotations[consts.AnnotationSignDrivesHash] = domain.CalculateNodeDriveSignHash(node)

	blockedDrivesStr, err := json.Marshal(blockedDrives)
	if err != nil {
		return fmt.Errorf("error marshalling blocked drives: %w", err)
	}
	node.Annotations[consts.AnnotationBlockedDrives] = string(blockedDrivesStr)

	blockedPhysicalUUIDs, err := domain.ReadBlockedDrivePhysicalUUIDs(node)
	if err != nil {
		return err
	}

	// Update weka.io/shared-drives-capacity extended resources (TLC + QLC), now deliberately
	// excluding blocked drives to agree with block_drives.go's BlockSharedDrives. Skip creating
	// the resources when this node never had a weka.io/weka-shared-drives annotation and still
	// has none now — mirrors the weka.io/drives guard above; don't conjure capacity resources
	// for a node that has never reported any shared drives.
	// Sums come back from the setter rather than being recomputed for the log line below. When the
	// guard skips the write, mergedDrives is empty, so both sums are zero by construction.
	var tlcDriveCapacityGiB, qlcDriveCapacityGiB int64
	if hadSharedDrivesAnnotation || len(mergedDrives) > 0 {
		tlcDriveCapacityGiB, qlcDriveCapacityGiB = domain.SetSharedDriveCapacityResources(node, mergedDrives, blockedPhysicalUUIDs, blockedDrives)
	}

	logger.Info("Updated proxy mode annotations", "drives", len(mergedDrives), "newDrives", newDrivesFound, "tlcCapacityGiB", tlcDriveCapacityGiB, "qlcCapacityGiB", qlcDriveCapacityGiB)

	// Update node status and annotations
	if err := r.Status().Update(ctx, node); err != nil {
		return fmt.Errorf("error updating node status: %w", err)
	}

	if err := r.Update(ctx, node); err != nil {
		return fmt.Errorf("error updating node annotations: %w", err)
	}

	// Mark container as completed
	r.container.Status.Status = weka.Completed
	return r.Status().Update(ctx, r.container)
}

// warnOverriddenDriveTypes logs a Warn naming every drive whose Type an override changed.
// Virtual drives already allocated from those physical drives keep their own recorded type
// (and pool) until reallocated, so allocatable can temporarily disagree with what's running —
// surfaced so it isn't mistaken for a bug.
func warnOverriddenDriveTypes(logger *instrumentation.SpanLogger, beforeTypes map[string]string, after []domain.SharedDriveInfo) {
	for _, drive := range after {
		priorType, existed := beforeTypes[drive.Serial]
		// A drive absent from beforeTypes is newly discovered, so nothing was ever allocated from
		// it and there is nothing to disagree with — not worth a warning.
		if !existed || priorType == drive.Type {
			continue
		}
		logger.Warn("Drive type overridden; virtual drives already allocated from this drive keep their recorded type until their containers are reallocated",
			"serial", drive.Serial, "physicalUUID", drive.PhysicalUUID, "previousType", priorType, "newType", drive.Type)
	}
}

// appendMissingDrivesToBlocked extends blockedDrives with any annotatedSerial
// absent from opResult.RawDrives. Gated by KernelViewComplete — when the
// kernel view isn't complete, missing-from-RawDrives is not conclusive
// evidence the drive is gone, so the function defers.
// Returns the blockedDrives and the serials newly added.
func appendMissingDrivesToBlocked(
	annotatedSerials []string,
	opResult *operations.DriveNodeResults,
	blockedDrives []string,
) (updatedBlocked, newlyAdded []string) {
	if !opResult.KernelViewComplete {
		return blockedDrives, nil
	}

	kernelVisible := make(map[string]struct{}, len(opResult.RawDrives))
	for _, raw := range opResult.RawDrives {
		if raw.SerialId != "" {
			kernelVisible[raw.SerialId] = struct{}{}
		}
	}

	blockedSet := make(map[string]struct{}, len(blockedDrives))
	for _, s := range blockedDrives {
		blockedSet[s] = struct{}{}
	}

	// Sort so map-derived input yields a stable blockedDrives order.
	sortedSerials := slices.Clone(annotatedSerials)
	slices.Sort(sortedSerials)

	var missingDrives []string
	for _, s := range sortedSerials {
		if s == "" {
			continue
		}
		if _, kernelOK := kernelVisible[s]; kernelOK {
			continue
		}
		if _, alreadyBlocked := blockedSet[s]; alreadyBlocked {
			continue
		}
		blockedDrives = append(blockedDrives, s)
		blockedSet[s] = struct{}{}
		missingDrives = append(missingDrives, s)
	}

	return blockedDrives, missingDrives
}
