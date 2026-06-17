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

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/pkg/domain"
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
) ([]string, []string) {
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
