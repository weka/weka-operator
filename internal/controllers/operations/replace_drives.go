package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/pkg/util"
)

type ReplaceDrivesResult struct {
	Err    string `json:"err,omitempty"`
	Result string `json:"result"`
}

type ReplaceDrivesOperation struct {
	manager         ctrl.Manager
	payload         *weka.ReplaceDrivesPayload
	results         ReplaceDrivesResult
	ownerRef        client.Object
	ownerStatus     *string
	successCallback lifecycle.StepFunc
	failureCallback lifecycle.StepFunc
	container       *weka.WekaContainer // internal field
}

func NewReplaceDrivesOperation(mgr ctrl.Manager, payload *weka.ReplaceDrivesPayload, ownerStatus *string, successCallback, failureCallback lifecycle.StepFunc) *ReplaceDrivesOperation {
	return &ReplaceDrivesOperation{
		manager:         mgr,
		payload:         payload,
		ownerStatus:     ownerStatus,
		successCallback: successCallback,
		failureCallback: failureCallback,
	}
}

func (o *ReplaceDrivesOperation) AsStep() lifecycle.Step {
	return lifecycle.Step{
		Name: "ReplaceDrive",
		Run:  AsRunFunc(o),
	}
}

func (o *ReplaceDrivesOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		{
			Name:                      "Noop",
			Run:                       o.Noop,
			Predicates:                lifecycle.Predicates{o.IsDone},
			FinishOnSuccess:           true,
			ContinueOnPredicatesFalse: true,
		},
		{
			Name:                      "ReplaceDrives",
			Run:                       o.ReplaceDrives,
			ContinueOnPredicatesFalse: true,
		},
		{
			Name: "SuccessCallback",
			Run:  o.SuccessCallback,
			Predicates: lifecycle.Predicates{
				o.OperationSucceeded,
			},
			ContinueOnPredicatesFalse: true,
			FinishOnSuccess:           true,
		},
		{Name: "FailureCallback", Run: o.FailureCallback},
	}
}

func (o *ReplaceDrivesOperation) ReplaceDrives(ctx context.Context) error {
	if o.payload == nil {
		return fmt.Errorf("replace drives operation payload is nil")
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ReplaceDrivesOperation", "node", o.payload.NodeName)
	defer end()

	// first run BlockDrives operation to block the drives that are being replaced
	blockDrivesOp := NewBlockDrivesOperation(o.manager, &weka.BlockDrivesPayload{
		Node:      string(o.payload.NodeName),
		SerialIDs: o.payload.OldSerialIDs,
	}, nil, nil, nil)

	err := ExecuteOperation(ctx, blockDrivesOp)
	if err != nil {
		return errors.Wrap(err, "failed to block drives before replacing them")
	}

	nodeName := o.payload.NodeName
	// re-allocate drives if failed drives are still in container allocations
	resourceAllocator, err := allocator.GetAllocator(ctx, o.manager.GetClient())
	if err != nil {
		return err
	}

	allocations, err := resourceAllocator.GetAllocations(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get resource allocations")
	}

	nodeAllocations := allocations.NodeMap[nodeName]
	if nodeAllocations.Drives == nil {
		return fmt.Errorf("no drive allocations found for node %s", nodeName)
	}

	// in allocations, find the drives that need to be replaced and also find the container that has this drive in allocations (if any)
	alocatedDrivesToReplaceByWekaContainer := make(map[string][]string, len(nodeAllocations.Drives))
	for owner, drives := range nodeAllocations.Drives {
		for _, drive := range drives {
			if slices.Contains(o.payload.OldSerialIDs, drive) {
				logger.Info("Found drive to be replaced in node allocations", "serialID", drive, "container_name", owner.Container)
				// if the container is not in the list, add it
				if _, exists := alocatedDrivesToReplaceByWekaContainer[owner.Container]; !exists {
					alocatedDrivesToReplaceByWekaContainer[owner.Container] = []string{}
				}
				alocatedDrivesToReplaceByWekaContainer[owner.Container] = append(alocatedDrivesToReplaceByWekaContainer[owner.Container], drive)
			}
		}
	}

	// if no drives to replace found in allocations, return
	if len(alocatedDrivesToReplaceByWekaContainer) == 0 {
		o.results.Result = "No drives to replace found in node allocations"
		o.results.Err = ""
		return nil
	}

	// TODO: check whether some of these drives (OldSerialIDs) are added in weka and delete them if so (?)

	// get all drive containers for the node
	var driveContainers weka.WekaContainerList
	err = o.manager.GetClient().List(ctx, &driveContainers, client.MatchingLabels{
		"weka.io/mode": weka.WekaContainerModeDrive,
	})
	if err != nil {
		return errors.Wrap(err, "failed to list drive containers")
	}

	// filter containers by node name
	var driveContainersForNode []*weka.WekaContainer
	for i := range driveContainers.Items {
		if driveContainers.Items[i].GetNodeAffinity() == nodeName {
			driveContainersForNode = append(driveContainersForNode, &driveContainers.Items[i])
		}
	}

	logger.Info("Found drive containers for node", "count", len(driveContainersForNode), "node", nodeName)

	if len(driveContainersForNode) == 0 {
		// write an empty result and return
		o.results.Err = "no drive containers found for node"
		return nil
	}

	// create a map of drive containers by name for easy access
	driveContainersByName := make(map[string]*weka.WekaContainer, len(driveContainersForNode))
	for _, container := range driveContainersForNode {
		driveContainersByName[container.Name] = container
	}

	// allocate new drives for each container that has drives to replace and store the results
	allocResults := make(map[string]error, len(alocatedDrivesToReplaceByWekaContainer))

	for containerName, driveSerialIDsToReplace := range alocatedDrivesToReplaceByWekaContainer {
		logger.Info("Allocating new drives for container", "container_name", containerName, "drives_to_replace", driveSerialIDsToReplace)
		container, exists := driveContainersByName[containerName]
		if !exists {
			return fmt.Errorf("drive container %s not found for node %s", containerName, nodeName)
		}

		ownerRefs := container.GetOwnerReferences()
		if len(ownerRefs) == 0 {
			return fmt.Errorf("drive container %s has no owner references", containerName)
		}

		clusterObjKey := client.ObjectKey{
			Name:      ownerRefs[0].Name,
			Namespace: container.Namespace,
		}

		err = resourceAllocator.AllocateNewDrivesForContainer(ctx, clusterObjKey, container, len(driveSerialIDsToReplace), driveSerialIDsToReplace)
		if err != nil {
			logger.Error(err, "Failed to allocate new drives for container", "container_name", containerName)
		}
		allocResults[containerName] = err
	}

	// get updated allocations
	newNodeAllocations, err := resourceAllocator.RefreshAllocations(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get resource allocations after drive allocation")
	}

	var resultErrs []error

	// update drive allocations for containers where allocation was successful
	for _, container := range driveContainersForNode {
		if err, exists := allocResults[container.Name]; exists && err == nil {
			owner := container.GetOwnerReferences()
			nodeName := container.GetNodeAffinity()
			nodeAlloc, ok := newNodeAllocations.NodeMap[nodeName]
			if !ok {
				return errors.New("node allocations are not set")
			}

			allocOwner := allocator.Owner{
				OwnerCluster: allocator.OwnerCluster{
					ClusterName: owner[0].Name,
					Namespace:   container.Namespace,
				},
				Container: container.Name,
				Role:      container.Spec.Mode,
			}

			newDrives, ok := nodeAlloc.Drives[allocOwner]
			if !ok {
				return fmt.Errorf("no drives allocated")
			}

			if !util.SliceEquals(newDrives, container.Status.Allocations.Drives) {
				container.Status.Allocations.Drives = newDrives
				err = o.manager.GetClient().Status().Update(ctx, container)
				if err != nil {
					return errors.Wrap(err, "failed to update drive container status after drive allocation")
				}

				logger.Info("Updated drive container status with new drives", "container_name", container.Name, "new_drives", newDrives)
			}
		} else {
			err := fmt.Errorf("%v, container: %s", err, container.Name)
			resultErrs = append(resultErrs, err)
		}
	}

	if len(resultErrs) > 0 {
		errMsg := fmt.Sprintf("Failed to replace drives for node %s: %v", nodeName, resultErrs)
		o.results.Err = errMsg
		return errors.New(errMsg)
	}

	o.results.Result = fmt.Sprintf("Successfully replaced drives for node %s", nodeName)
	o.results.Err = ""
	return nil
}

func (o *ReplaceDrivesOperation) GetJsonResult() string {
	result, _ := json.Marshal(o.results)
	return string(result)
}

func (o *ReplaceDrivesOperation) SuccessCallback(ctx context.Context) error {
	if o.successCallback == nil {
		return nil
	}
	return o.successCallback(ctx)
}

func (o *ReplaceDrivesOperation) FailureCallback(ctx context.Context) error {
	if o.failureCallback == nil {
		return nil
	}
	return o.failureCallback(ctx)
}

func (o *ReplaceDrivesOperation) IsDone() bool {
	return o.ownerStatus != nil && *o.ownerStatus == "Done"
}

func (o *ReplaceDrivesOperation) OperationSucceeded() bool {
	return o.results.Err == ""
}

func (o *ReplaceDrivesOperation) Noop(ctx context.Context) error {
	return nil
}
