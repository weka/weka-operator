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
			return r.Client.Delete(ctx, r.pod)
		}
	}
	if r.container.IsDriversLoaderMode() {
		for _, c := range r.container.Status.Conditions {
			if c.Type == condition.CondResultsProcessed && c.Status == metav1.ConditionTrue {
				if time.Since(c.LastTransitionTime.Time) > time.Minute*5 {
					return r.Client.Delete(ctx, r.container)
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
					return r.Client.Delete(ctx, r.container)
				}
			}
		}
	}

	return nil
}

func (r *containerReconcilerLoop) isFeatureFlagsOperation() bool {
	return r.container.Spec.Mode == weka.WekaContainerModeAdhocOpWC &&
		r.container.Spec.Instructions != nil &&
		r.container.Spec.Instructions.Type == "feature-flags-update"
}

func (r *containerReconcilerLoop) isSignOrDiscoverDrivesOperation(ctx context.Context) bool {
	if r.container.Spec.Mode == weka.WekaContainerModeAdhocOp && r.container.Spec.Instructions != nil {
		return r.container.Spec.Instructions.Type == "sign-drives"
	}
	if r.container.Spec.Mode == weka.WekaContainerModeAdhocOp && r.container.Spec.Instructions != nil {
		return r.container.Spec.Instructions.Type == "discover-drives"
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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "updateNodeAnnotations")
	defer end()

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
			rawDriveCapacity[raw.SerialId] = raw.CapacityGiB
		}
	}

	// Parse existing annotation (handles both old []string and new []DriveEntry formats)
	seenDrives := make(map[string]domain.DriveEntry)
	if existingDrivesStr, ok := node.Annotations[consts.AnnotationWekaDrives]; ok && existingDrivesStr != "" {
		existingEntries, _, _ := domain.ParseDriveEntries(existingDrivesStr)
		for _, entry := range existingEntries {
			if entry.Serial == "" {
				continue // clean bad records of empty serial ids
			}
			seenDrives[entry.Serial] = entry
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
		if _, ok := seenDrives[drive.SerialId]; !ok {
			newDrivesFound++
		}
		capacity := rawDriveCapacity[drive.SerialId]
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

	node.Annotations[consts.AnnotationWekaDrives] = string(newDrivesStr)
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

	availableDrives := 0
	for _, entry := range updatedDrivesList {
		if !slices.Contains(blockedDrives, entry.Serial) {
			availableDrives++
		}
	}

	// Update weka.io/drives extended resource
	node.Status.Capacity[consts.ResourceDrives] = *resource.NewQuantity(int64(availableDrives), resource.DecimalSI)
	node.Status.Allocatable[consts.ResourceDrives] = *resource.NewQuantity(int64(availableDrives), resource.DecimalSI)
	//marshal blocked drives back and update annotation
	blockedDrivesStr, err := json.Marshal(blockedDrives)
	if err != nil {
		err = fmt.Errorf("error marshalling blocked drives: %w", err)
		return err
	}
	node.Annotations[consts.AnnotationBlockedDrives] = string(blockedDrivesStr)

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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "updateProxyModeAnnotations")
	defer end()

	logger.Info("Updating node annotations for proxy mode")

	// Read existing shared drives from annotation
	existingDrives := []domain.SharedDriveInfo{}
	if existingDrivesStr, ok := node.Annotations[consts.AnnotationSharedDrives]; ok {
		_ = json.Unmarshal([]byte(existingDrivesStr), &existingDrives)
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
