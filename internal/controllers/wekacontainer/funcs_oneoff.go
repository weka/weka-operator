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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

	// Update weka.io/weka-drives annotation
	previousDrives := []string{}
	newDrivesFound := 0
	if existingDrivesStr, ok := node.Annotations["weka.io/weka-drives"]; ok {
		_ = json.Unmarshal([]byte(existingDrivesStr), &previousDrives)
	}

	seenDrives := make(map[string]bool)
	for _, drive := range previousDrives {
		if drive == "" {
			continue // clean bad records of empty serial ids
		}
		seenDrives[drive] = true
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
		seenDrives[drive.SerialId] = true
	}

	if newDrivesFound == 0 {
		logger.Info("No new drives found")
	}

	updatedDrivesList := []string{}
	for drive := range seenDrives {
		updatedDrivesList = append(updatedDrivesList, drive)
	}
	newDrivesStr, err := json.Marshal(updatedDrivesList)
	if err != nil {
		err = fmt.Errorf("error marshalling updated drives list: %w", err)
		return err
	}

	node.Annotations["weka.io/weka-drives"] = string(newDrivesStr)
	// calculate hash, based on o.node.Status.NodeInfo.BootID
	node.Annotations["weka.io/sign-drives-hash"] = domain.CalculateNodeDriveSignHash(node)

	blockedDrives := []string{}
	if blockedDrivesStr, ok := node.Annotations["weka.io/blocked-drives"]; ok {
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
	for _, drive := range updatedDrivesList {
		if !slices.Contains(blockedDrives, drive) {
			availableDrives++
		}
	}

	// Update weka.io/drives extended resource
	node.Status.Capacity["weka.io/drives"] = *resource.NewQuantity(int64(availableDrives), resource.DecimalSI)
	node.Status.Allocatable["weka.io/drives"] = *resource.NewQuantity(int64(availableDrives), resource.DecimalSI)
	//marshal blocked drives back and update annotation
	blockedDrivesStr, err := json.Marshal(blockedDrives)
	if err != nil {
		err = fmt.Errorf("error marshalling blocked drives: %w", err)
		return err
	}
	node.Annotations["weka.io/blocked-drives"] = string(blockedDrivesStr)

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
