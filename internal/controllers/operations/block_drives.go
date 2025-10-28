package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/services/kubernetes"
)

type BlockDrivesOperation struct {
	client          client.Client
	kubeService     kubernetes.KubeService
	payload         *weka.BlockDrivesPayload
	results         BlockDrivesResult
	ownerStatus     *string
	successCallback lifecycle.StepFunc
	failureCallback lifecycle.StepFunc
	unblock         bool
}

type BlockDrivesResult struct {
	Err    string `json:"err,omitempty"`
	Result string `json:"result"`
}

func NewBlockDrivesOperation(mgr ctrl.Manager, payload *weka.BlockDrivesPayload, ownerStatus *string, successCallback, failureCallback lifecycle.StepFunc) *BlockDrivesOperation {
	return &BlockDrivesOperation{
		client:          mgr.GetClient(),
		kubeService:     kubernetes.NewKubeService(mgr.GetClient()),
		payload:         payload,
		ownerStatus:     ownerStatus,
		successCallback: successCallback,
		failureCallback: failureCallback,
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
	}
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
			FinishOnSuccess: true},
		&lifecycle.SimpleStep{
			Name: "BlockDrives",
			Run:  o.BlockDrives,
			Predicates: lifecycle.Predicates{
				func() bool { return !o.unblock },
			}},
		&lifecycle.SimpleStep{
			Name: "UnblockDrives",
			Run:  o.UnblockDrives,
			Predicates: lifecycle.Predicates{
				func() bool { return o.unblock },
			}},
		&lifecycle.SimpleStep{
			Name: "SuccessCallback",
			Run:  o.SuccessCallback,
			Predicates: lifecycle.Predicates{
				o.OperationSucceeded,
			}, FinishOnSuccess: true,
		},
		&lifecycle.SimpleStep{Name: "FailureCallback", Run: o.FailureCallback},
	}
}

func (o *BlockDrivesOperation) UnblockDrives(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "UnblockDrives", "node", o.payload.Node)
	defer end()

	node := &corev1.Node{}
	if err := o.client.Get(ctx, types.NamespacedName{Name: o.payload.Node}, node); err != nil {
		return err
	}

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	blockedDrives := []string{}
	if blockedDrivesStr, ok := node.Annotations["weka.io/blocked-drives"]; ok {
		json.Unmarshal([]byte(blockedDrivesStr), &blockedDrives)
	}

	allDrives := []string{}
	if allDrivesStr, ok := node.Annotations["weka.io/weka-drives"]; ok {
		json.Unmarshal([]byte(allDrivesStr), &allDrives)
	}

	logger.Debug("Available drives", "drives", allDrives)
	logger.Debug("Blocked drives", "drives", blockedDrives)

	notFoundDrives := []string{}
	updatedBlockedDrives := []string{}

	// Remove the unblocked drives from the list
	for _, serialID := range o.payload.SerialIDs {
		found := false
		for i, blockedDrive := range blockedDrives {
			if blockedDrive == serialID {
				found = true
				updatedBlockedDrives = append(blockedDrives[:i], blockedDrives[i+1:]...)
				break
			}
		}
		if !found {
			notFoundDrives = append(notFoundDrives, serialID)
		}
	}

	if len(notFoundDrives) > 0 {
		err := fmt.Errorf("the following drives were not found in the blocked drives list: %v", notFoundDrives)
		logger.Error(err, "Failed to unblock drives")
		o.results = BlockDrivesResult{
			Err: err.Error(),
		}
		return nil
	}

	newBlockedDrivesStr, _ := json.Marshal(updatedBlockedDrives)
	node.Annotations["weka.io/blocked-drives"] = string(newBlockedDrivesStr)

	availableDrives := len(allDrives) - len(updatedBlockedDrives)
	newQuantity := resource.MustParse(strconv.Itoa(availableDrives))

	// Update weka.io/drives extended resource
	node.Status.Capacity["weka.io/drives"] = newQuantity
	node.Status.Allocatable["weka.io/drives"] = newQuantity

	if err := o.client.Status().Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node status: %w", err)
		return err
	}

	if err := o.client.Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node annotations: %w", err)
		return err
	}

	o.results = BlockDrivesResult{
		Result: fmt.Sprintf("Successfully unblocked %d drives on node %s", len(o.payload.SerialIDs), o.payload.Node),
	}
	return nil
}

func (o *BlockDrivesOperation) BlockDrives(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "BlockDrives", "node", o.payload.Node)
	defer end()

	node := &corev1.Node{}
	if err := o.client.Get(ctx, types.NamespacedName{Name: o.payload.Node}, node); err != nil {
		return err
	}

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	blockedDrives := []string{}
	if blockedDrivesStr, ok := node.Annotations["weka.io/blocked-drives"]; ok {
		json.Unmarshal([]byte(blockedDrivesStr), &blockedDrives)
	}

	allDrives := []string{}
	if allDrivesStr, ok := node.Annotations["weka.io/weka-drives"]; ok {
		json.Unmarshal([]byte(allDrivesStr), &allDrives)
	}

	logger.Debug("Available drives", "drives", allDrives)
	logger.Debug("Blocked drives", "drives", blockedDrives)

	notFoundDrives := []string{}

	// Add the new blocked drives to the list (if not already there)
	for _, serialID := range o.payload.SerialIDs {
		isBlocked := slices.Contains(blockedDrives, serialID)

		// check if blocked drive exists in the available drives list
		existsInAllDrives := slices.Contains(allDrives, serialID)

		if !existsInAllDrives {
			notFoundDrives = append(notFoundDrives, serialID)
		}

		if !isBlocked {
			blockedDrives = append(blockedDrives, serialID)
		}
	}

	if len(notFoundDrives) > 0 {
		err := fmt.Errorf("the following drives were not found in the available drives list: %v", notFoundDrives)
		logger.Error(err, "Failed to block drives")
		o.results = BlockDrivesResult{
			Err: err.Error(),
		}
		return nil
	}

	newBlockedDrivesStr, _ := json.Marshal(blockedDrives)
	node.Annotations["weka.io/blocked-drives"] = string(newBlockedDrivesStr)

	availableDrives := len(allDrives) - len(blockedDrives)
	newQuantity := resource.MustParse(strconv.Itoa(availableDrives))

	// Update weka.io/drives extended resource
	node.Status.Capacity["weka.io/drives"] = newQuantity
	node.Status.Allocatable["weka.io/drives"] = newQuantity

	if err := o.client.Status().Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node status: %w", err)
		return err
	}

	// remove weka.io/sign-drives-hash annotation from nodes to force drives re-scan on the next sign drives operation
	delete(node.Annotations, "weka.io/sign-drives-hash")

	if err := o.client.Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node: %w", err)
		return err
	}

	o.results = BlockDrivesResult{
		Result: fmt.Sprintf("Successfully blocked %d drives on node %s", len(o.payload.SerialIDs), o.payload.Node),
	}
	return nil
}

func (o *BlockDrivesOperation) GetResult() BlockDrivesResult {
	return o.results
}

func (o *BlockDrivesOperation) GetJsonResult() string {
	resultJSON, _ := json.Marshal(o.results)
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
