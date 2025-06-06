package operations

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/weka/weka-k8s-api/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/pkg/util"
)

type ReplaceDriveResult struct {
	Err    string `json:"err,omitempty"`
	Result string `json:"result"`
}

type ReplaceDriveOperation struct {
	client          client.Client
	payload         *v1alpha1.ReplaceDrivePayload
	results         ReplaceDriveResult
	ownerRef        client.Object
	ownerStatus     *string
	successCallback lifecycle.StepFunc
	failureCallback lifecycle.StepFunc
	container       *v1alpha1.WekaContainer // internal field
}

func NewReplaceDriveOperation(client client.Client, payload *v1alpha1.ReplaceDrivePayload, ownerStatus *string, successCallback, failureCallback lifecycle.StepFunc) *ReplaceDriveOperation {
	return &ReplaceDriveOperation{
		client:          client,
		payload:         payload,
		ownerStatus:     ownerStatus,
		successCallback: successCallback,
		failureCallback: failureCallback,
	}
}

func (o *ReplaceDriveOperation) AsStep() lifecycle.Step {
	return lifecycle.Step{
		Name: "ReplaceDrive",
		Run:  AsRunFunc(o),
	}
}

func (o *ReplaceDriveOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
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

func (o *ReplaceDriveOperation) getContainerName() string {
	payloadHash := util.GetHash(fmt.Sprintf("%s%s", o.payload.OldSerialID, o.payload.NewSerialID), 8)
	return fmt.Sprintf("weka-replace-drive-%s-%s", o.payload.NodeName, payloadHash)
}

func (o *ReplaceDriveOperation) GetContainer(ctx context.Context) error {
	name := o.getContainerName()
	ref := v1alpha1.ObjectReference{
		Name:      name,
		Namespace: o.ownerRef.GetNamespace(),
	}
	existing, err := discovery.GetContainerByName(ctx, o.client, ref)
	if err != nil && apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("no weka container with name %s was found", name)
	}
	o.container = existing
	return nil
}

func (o *ReplaceDriveOperation) GetJsonResult() string {
	result, _ := json.Marshal(o.results)
	return string(result)
}

func (o *ReplaceDriveOperation) SuccessCallback(ctx context.Context) error {
	if o.successCallback == nil {
		return nil
	}
	return o.successCallback(ctx)
}

func (o *ReplaceDriveOperation) FailureCallback(ctx context.Context) error {
	if o.failureCallback == nil {
		return nil
	}
	return o.failureCallback(ctx)
}

func (o *ReplaceDriveOperation) IsDone() bool {
	return o.ownerStatus != nil && *o.ownerStatus == "Done"
}

func (o *ReplaceDriveOperation) OperationSucceeded() bool {
	return o.results.Err == ""
}
