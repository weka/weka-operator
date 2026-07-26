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

// reportAdhocPodNotProgressing surfaces an abnormal not-progressing adhoc-op pod
// (Unschedulable, ImagePullBackOff, CrashLoopBackOff, config errors, ...) as a
// throttled warning, so operators do not have to wait for the deletion timeout to
// learn something is wrong. Pods that are still legitimately starting up are not
// worth warning about.
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

// deleteStuckAdhocContainer marks an adhoc-op container for deletion once its pod
// has failed to produce a result for too long. Adhoc-op containers are node-pinned
// and only produce a result once their pod runs, so a pod that can never run
// (ImagePullBackOff / unschedulable / crash-looping) would otherwise leak the CR
// forever: the finished-cleanup path is gated on CondResultsProcessed, which is only
// set after fetchResults execs into a running pod.
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

// podStuckSince returns the point in time from which the pod stopped making progress
// toward producing a result.
//
// For a pod that never ran (Pending / unschedulable / ImagePullBackOff) or that
// crash-loops, that is its creation time: an adhoc-op pod is expected to run within
// minutes of being created. For a pod that ran and then went terminal (evicted, node
// lost), creation time can be hours in the past, which would leave no grace period at
// all and report a misleading duration, so the ContainersReady transition is used
// instead - it marks when the pod actually stopped running.
//
// ContainersReady is deliberately not consulted for a crash-looping pod: it flaps as
// the container restarts, which would reset the clock on every restart.
func podStuckSince(pod *v1.Pod) time.Time {
	if pod.Status.Phase == v1.PodFailed || pod.Status.Phase == v1.PodSucceeded {
		if c := podCondition(pod, v1.ContainersReady); c != nil &&
			c.Status == v1.ConditionFalse && !c.LastTransitionTime.IsZero() {
			return c.LastTransitionTime.Time
		}
	}
	return pod.CreationTimestamp.Time
}

// podCrashLoopingAfterFailure reports whether one of the pod's containers is waiting
// in a restart backoff after a failed run.
//
// The exit-code check is load-bearing: adhoc-op pods do not set RestartPolicy, so they
// get the API default Always, and a one-off command that *succeeds* is restarted too
// and is eventually reported as CrashLoopBackOff as well. Those must not be deleted
// here - they are reaped by the results-processed path (cleanupFinishedOneOff), which
// runs earlier in the flow. Init containers need no handling: a crash-looping init
// container keeps the phase at Pending, which is already covered.
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

// podNotRunningReason returns a short reason describing why the pod is not producing a
// result, plus an optional longer detail message for events and logs. Precedence: the
// first init-container or container Waiting.Reason (ImagePullBackOff,
// CreateContainerConfigError, ...); then, for a terminal pod, a container
// Terminated.Reason (OOMKilled, Error) or the pod-level reason (Evicted); then an
// explicit scheduling failure (Unschedulable) from the PodScheduled condition; else
// the phase.
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

	// Check if this is a proxy mode operation
	isProxyMode := len(opResult.ProxyDrives) > 0

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

	// Read existing shared drives from annotation
	existingDrives := []domain.SharedDriveInfo{}
	if existingDrivesStr, ok := node.Annotations[consts.AnnotationSharedDrives]; ok {
		_ = json.Unmarshal([]byte(existingDrivesStr), &existingDrives) //nolint:errcheck // error return value intentionally not checked
	}

	// Build map keyed by serial for efficient merge
	drivesBySerial := make(map[string]domain.SharedDriveInfo)
	for _, drive := range existingDrives {
		if drive.Serial != "" {
			drivesBySerial[drive.Serial] = drive
		}
	}

	// Merge new results: update existing or add new drives
	// This does NOT delete drives that aren't in opResult.ProxyDrives
	newDrivesFound := 0
	for _, drive := range opResult.ProxyDrives {
		if drive.Serial == "" {
			continue // skip drives without serial
		}
		if _, exists := drivesBySerial[drive.Serial]; !exists {
			newDrivesFound++
		}
		drivesBySerial[drive.Serial] = drive
	}

	if newDrivesFound == 0 {
		logger.Info("No new drives found")
	}

	annotatedSerials := make([]string, 0, len(drivesBySerial))
	for s := range drivesBySerial {
		annotatedSerials = append(annotatedSerials, s)
	}
	var missingDrives []string
	blockedDrives, missingDrives = appendMissingDrivesToBlocked(annotatedSerials, opResult, blockedDrives)
	for _, s := range missingDrives {
		logger.Info("Blocking missing drive", "serial_id", s)
	}

	// Convert map back to slice and calculate capacities
	mergedDrives := make([]domain.SharedDriveInfo, 0, len(drivesBySerial))
	tlcDriveCapacityGiB := int64(0)
	qlcDriveCapacityGiB := int64(0)

	for _, drive := range drivesBySerial {
		mergedDrives = append(mergedDrives, drive)
		if drive.Type == "QLC" {
			qlcDriveCapacityGiB += int64(drive.CapacityGiB)
		} else {
			tlcDriveCapacityGiB += int64(drive.CapacityGiB)
		}
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

	// Update weka.io/shared-drives-capacity extended resource
	// TLC drive type
	node.Status.Capacity[consts.ResourceSharedDrivesCapacity] = *resource.NewQuantity(tlcDriveCapacityGiB, resource.DecimalSI)
	node.Status.Allocatable[consts.ResourceSharedDrivesCapacity] = *resource.NewQuantity(tlcDriveCapacityGiB, resource.DecimalSI)
	// QLC drive type
	node.Status.Capacity[consts.ResourcesSharedDrivesCapacityQLC] = *resource.NewQuantity(qlcDriveCapacityGiB, resource.DecimalSI)
	node.Status.Allocatable[consts.ResourcesSharedDrivesCapacityQLC] = *resource.NewQuantity(qlcDriveCapacityGiB, resource.DecimalSI)

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
