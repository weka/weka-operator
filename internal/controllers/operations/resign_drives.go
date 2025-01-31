package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/factory"

	"github.com/pkg/errors"
	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	util2 "github.com/weka/weka-operator/pkg/util"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ResignDrivesResult struct {
	Err            string   `json:"err,omitempty"`
	ResignedDrives []string `json:"drives"`
}

type ResignDrivesOperation struct {
	client          client.Client
	kubeService     kubernetes.KubeService
	scheme          *runtime.Scheme
	payload         *v1alpha1.ForceResignDrivesPayload
	image           string
	pullSecret      string
	container       *v1alpha1.WekaContainer // internal field
	ownerRef        client.Object
	ownerStatus     *string
	results         ResignDrivesResult // internal field
	mgr             ctrl.Manager
	tolerations     []corev1.Toleration
	successCallback lifecycle.StepFunc
	failureCallback lifecycle.StepFunc
}

func NewResignDrivesOperation(mgr ctrl.Manager, payload *v1alpha1.ForceResignDrivesPayload, ownerRef client.Object, ownerDetails v1alpha1.WekaContainerDetails, ownerStatus *string, successCallback, failureCallback lifecycle.StepFunc) *ResignDrivesOperation {
	return &ResignDrivesOperation{
		mgr:             mgr,
		client:          mgr.GetClient(),
		kubeService:     kubernetes.NewKubeService(mgr.GetClient()),
		scheme:          mgr.GetScheme(),
		payload:         payload,
		image:           ownerDetails.Image,
		pullSecret:      ownerDetails.ImagePullSecret,
		tolerations:     ownerDetails.Tolerations,
		ownerRef:        ownerRef,
		ownerStatus:     ownerStatus,
		successCallback: successCallback,
		failureCallback: failureCallback,
	}
}

func (o *ResignDrivesOperation) AsStep() lifecycle.Step {
	return lifecycle.Step{
		Name: "ResignDrives",
		Run:  AsRunFunc(o),
	}
}

func (o *ResignDrivesOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		{Name: "GetContainer", Run: o.GetContainer},
		{Name: "DeleteOnDone", Run: o.DeleteContainer, Predicates: lifecycle.Predicates{o.IsDone}, ContinueOnPredicatesFalse: true, FinishOnSuccess: true},
		{
			Name:                      "EnsureContainer",
			Run:                       o.EnsureContainer,
			Predicates:                lifecycle.Predicates{o.HasNoContainer},
			ContinueOnPredicatesFalse: true,
		},
		{Name: "PollResults", Run: o.PollResults},
		{Name: "ProcessResult", Run: o.ProcessResult},
		{
			Name: "FailureUpdate",
			Run:  o.FailureCallback,
			Predicates: lifecycle.Predicates{
				o.OperationFailed,
			},
			ContinueOnPredicatesFalse: true,
			FinishOnSuccess:           true,
		},
		{Name: "SuccessCallback", Run: o.SuccessCallback},
		{Name: "DeleteContainer", Run: o.DeleteContainer},
	}
}

func (o *ResignDrivesOperation) ProcessResult(ctx context.Context) error {
	resignResult := &ResignDrivesResult{}
	err := json.Unmarshal([]byte(*o.container.Status.ExecutionResult), resignResult)
	if err != nil {
		return errors.Wrap(err, "Failed to unmarshal results")
	}

	if resignResult.Err != "" {
		err = fmt.Errorf("resign drives operation failed: %s, re-creating container", resignResult.Err)
		_ = o.DeleteContainer(ctx)
		return err
	}

	if len(resignResult.ResignedDrives) == 0 {
		err = fmt.Errorf("resign drives operation did not resign any drives, re-creating container")
		_ = o.DeleteContainer(ctx)
		return err
	}

	o.results = *resignResult
	return nil
}

func (o *ResignDrivesOperation) DeleteContainer(ctx context.Context) error {
	if o.container != nil {
		err := o.client.Delete(ctx, o.container)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	o.container = nil
	return nil
}

func (o *ResignDrivesOperation) EnsureContainer(ctx context.Context) error {
	if o.container != nil {
		return nil
	}

	// validate image for sign-drives
	if o.image == "" {
		o.image = config.Config.SignDrivesImage
	} else if strings.Contains(o.image, "weka.io/weka-in-container") {
		err := fmt.Errorf("weka image is not allowed for sign-drives operation, do not set image to use default")
		o.results.Err = err.Error()
		o.failureCallback(ctx)
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	labels := util2.MergeMaps(o.ownerRef.GetLabels(), factory.RequiredAnyWekaContainerLabels(v1alpha1.WekaContainerModeAdhocOp))

	payloadBytes, err := json.Marshal(o.payload)
	if err != nil {
		return err
	}

	instructions := &v1alpha1.Instructions{
		Type:    "force-resign-drives",
		Payload: string(payloadBytes),
	}

	containerName := o.getContainerName()
	container := &v1alpha1.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      containerName,
			Namespace: o.ownerRef.GetNamespace(),
			Labels:    labels,
		},
		Spec: v1alpha1.WekaContainerSpec{
			Mode:            v1alpha1.WekaContainerModeAdhocOp,
			NodeAffinity:    o.payload.NodeName,
			Image:           o.image,
			ImagePullSecret: o.pullSecret,
			Instructions:    instructions,
			Tolerations:     o.tolerations,
		},
	}

	err = ctrl.SetControllerReference(o.ownerRef, container, o.scheme)
	if err != nil {
		return err
	}

	err = o.client.Create(ctx, container)
	if err != nil {
		return err
	}

	o.container = container
	return nil
}

func (o *ResignDrivesOperation) getContainerName() string {
	return fmt.Sprintf("weka-force-resign-drives-%s", o.ownerRef.GetUID())
}

func (o *ResignDrivesOperation) GetContainer(ctx context.Context) error {
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

func (o *ResignDrivesOperation) PollResults(ctx context.Context) error {
	if o.container.Status.ExecutionResult == nil {
		return lifecycle.NewWaitError(errors.New("container execution result is not ready"))
	}
	return nil
}

func (o *ResignDrivesOperation) IsDone() bool {
	return o.ownerStatus != nil && *o.ownerStatus == "Done"
}

func (o *ResignDrivesOperation) GetJsonResult() string {
	resultJSON, _ := json.Marshal(o.results)
	return string(resultJSON)
}

func (o *ResignDrivesOperation) HasNoContainer() bool {
	return o.container == nil
}

func (o *ResignDrivesOperation) SuccessCallback(ctx context.Context) error {
	return o.successCallback(ctx)
}

func (o *ResignDrivesOperation) FailureCallback(ctx context.Context) error {
	if o.failureCallback == nil {
		return nil
	}
	return o.failureCallback(ctx)
}

func (o *ResignDrivesOperation) OperationFailed() bool {
	return o.results.Err != ""
}
