package operations

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type BlockDrivesOperation struct {
	client  client.Client
	payload *v1alpha1.BlockDrivesPayload
	results BlockDrivesResult
}

type BlockDrivesResult struct {
	Err    error  `json:"err,omitempty"`
	Result string `json:"result"`
}

func NewBlockDrivesOperation(mgr ctrl.Manager, payload *v1alpha1.BlockDrivesPayload) *BlockDrivesOperation {
	return &BlockDrivesOperation{
		client:  mgr.GetClient(),
		payload: payload,
	}
}

func (o *BlockDrivesOperation) AsStep() lifecycle.Step {
	return lifecycle.Step{
		Name: "BlockDrives",
		Run:  AsRunFunc(o),
	}
}

func (o *BlockDrivesOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		{Name: "BlockDrives", Run: o.BlockDrives},
	}
}

func (o *BlockDrivesOperation) BlockDrives(ctx context.Context) error {
	node := &corev1.Node{}
	if err := o.client.Get(ctx, types.NamespacedName{Name: o.payload.Node}, node); err != nil {
		return err
	}

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	// Update weka.io/blocked-drives annotation
	blockedDrives := []string{}
	if blockedDrivesStr, ok := node.Annotations["weka.io/blocked-drives"]; ok {
		json.Unmarshal([]byte(blockedDrivesStr), &blockedDrives)
	}
	blockedDrives = append(blockedDrives, o.payload.SerialIDs...)
	newBlockedDrivesStr, _ := json.Marshal(blockedDrives)
	node.Annotations["weka.io/blocked-drives"] = string(newBlockedDrivesStr)

	// Update weka.io/drives extended resource
	allDrives := []string{}
	if allDrivesStr, ok := node.Annotations["weka.io/weka-drives"]; ok {
		json.Unmarshal([]byte(allDrivesStr), &allDrives)
	}
	availableDrives := len(allDrives) - len(blockedDrives)
	node.Status.Capacity["weka.io/drives"] = *resource.NewQuantity(int64(availableDrives), resource.DecimalSI)
	node.Status.Allocatable["weka.io/drives"] = *resource.NewQuantity(int64(availableDrives), resource.DecimalSI)

	if err := o.client.Update(ctx, node); err != nil {
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

func (o *BlockDrivesOperation) Cleanup() lifecycle.Step {
	return lifecycle.Step{
		Name: "NoCleanup",
		Run: func(ctx context.Context) error {
			return nil
		},
	}
}
