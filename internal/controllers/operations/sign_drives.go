package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
	"github.com/weka/weka-operator/pkg/workers"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type SignDrivesOperation struct {
	client          client.Client
	kubeService     kubernetes.KubeService
	scheme          *runtime.Scheme
	payload         *weka.SignDrivesPayload
	image           string
	pullSecret      string
	containers      []*weka.WekaContainer
	ownerRef        client.Object
	results         DiscoverDrivesResult
	ownerStatus     string
	mgr             ctrl.Manager
	successCallback lifecycle.StepFunc
	failureCallback lifecycle.StepFunc
	force           bool
	tolerations     []v1.Toleration
	recorder        record.EventRecorder
}

func (o *SignDrivesOperation) AsStep() lifecycle.Step {
	return lifecycle.Step{
		Name: "SignDrives",
		Run:  AsRunFunc(o),
	}
}

func NewSignDrivesOperation(mgr ctrl.Manager, payload *weka.SignDrivesPayload, ownerRef client.Object, ownerDetails weka.WekaContainerDetails, ownerStatus string, successCallback, failureCallback lifecycle.StepFunc, force bool) *SignDrivesOperation {
	kclient := mgr.GetClient()
	return &SignDrivesOperation{
		mgr:             mgr,
		client:          kclient,
		kubeService:     kubernetes.NewKubeService(kclient),
		scheme:          mgr.GetScheme(),
		payload:         payload,
		image:           ownerDetails.Image,
		pullSecret:      ownerDetails.ImagePullSecret,
		ownerRef:        ownerRef,
		ownerStatus:     ownerStatus,
		tolerations:     ownerDetails.Tolerations,
		successCallback: successCallback,
		failureCallback: failureCallback,
		force:           force,
	}
}

func (o *SignDrivesOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		{Name: "GetContainers", Run: o.GetContainers},
		{Name: "DeleteOnDone", Run: o.DeleteContainers, Predicates: lifecycle.Predicates{o.IsDone}, ContinueOnPredicatesFalse: true, FinishOnSuccess: true},
		{Name: "EnsureContainers", Run: o.EnsureContainers},
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
		{Name: "SuccessUpdate", Run: o.SuccessUpdate},
		{Name: "DeleteOnFinish", Run: o.DeleteContainers},
	}
}

func (o *SignDrivesOperation) GetContainers(ctx context.Context) error {
	existing, err := discovery.GetOwnedContainers(ctx, o.client, o.ownerRef.GetUID(), o.ownerRef.GetNamespace(), weka.WekaContainerModeAdhocOp)
	if err != nil {
		return err
	}
	o.containers = existing
	return nil
}

func (o *SignDrivesOperation) EnsureContainers(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	// validate image for sign-drives
	if o.image == "" {
		o.image = config.Config.SignDrivesImage
	} else if strings.Contains(o.image, "weka.io/weka-in-container") {
		err := fmt.Errorf("weka image is not allowed for sign-drives operation, do not set image to use default")
		o.results.Err = err.Error()
		o.failureCallback(ctx)
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	payloadBytes, err := json.Marshal(o.payload)
	if err != nil {
		return err
	}

	instructions := &weka.Instructions{
		Type:    "sign-drives",
		Payload: string(payloadBytes),
	}

	matchingNodes, err := o.kubeService.GetNodes(ctx, o.payload.NodeSelector)
	if err != nil {
		return err
	}

	if len(matchingNodes) == 0 {
		return fmt.Errorf("no matching nodes found for the given node selector")
	}

	existingContainerNodes := make(map[string]bool)
	for _, container := range o.containers {
		existingContainerNodes[string(container.GetNodeAffinity())] = true
	}

	defer logger.SetValues("matchingNodes", len(matchingNodes), "existingContainers", len(existingContainerNodes))
	logger.SetAttributes()

	//TODO: Re-factor to all pieces of results will be a generic results structure, allowing to implement generic parallezation with callback funcs
	newlyCreated := 0
	skip := 0

	toCreate := []*weka.WekaContainer{}
	for _, node := range matchingNodes {
		if existingContainerNodes[node.Name] {
			continue
		}

		// if data exists and not force - skip
		// TODO: Does it work? repeat runs do create repeate wekacontainers, so sounds like this check is broken
		if !o.force {
			if node.Annotations["weka.io/weka-drives"] != "" {
				skip += 1
				continue
			}
		}

		labels := util.MergeMaps(o.ownerRef.GetLabels(), factory.RequiredAnyWekaContainerLabels(weka.WekaContainerModeAdhocOp))

		containerName := fmt.Sprintf("weka-sign-and-discover-drives-%s", node.Name)
		newContainer := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      containerName,
				Namespace: o.ownerRef.GetNamespace(),
				Labels:    labels,
			},
			Spec: weka.WekaContainerSpec{
				Mode:            weka.WekaContainerModeAdhocOp,
				NodeAffinity:    weka.NodeName(node.Name),
				Image:           o.image,
				ImagePullSecret: o.pullSecret,
				Instructions:    instructions,
				Tolerations:     o.tolerations,
			},
		}
		toCreate = append(toCreate, newContainer)

	}

	results := workers.ProcessConcurrently(ctx, toCreate, 32, func(ctx context.Context, container *weka.WekaContainer) error {
		err := controllerutil.SetControllerReference(o.ownerRef, container, o.scheme)
		if err != nil {
			return errors.Wrap(err, "failed to set controller reference")
		}

		err = o.client.Create(ctx, container)
		if err != nil {
			return errors.Wrap(err, "failed to create container")
		}
		return nil
	})

	err = results.AsError()
	if err != nil {
		logger.SetError(err, fmt.Sprintf("%d failed", len(results.GetErrors())))
		return err
	} else {
		for _, result := range results.Items {
			if result.Err == nil {
				o.containers = append(o.containers, result.Object)
				newlyCreated += 1
			}
		}
		logger.SetValues("newlyCreated", newlyCreated, "skipNodes", skip)
		return nil
	}
}

func (o *SignDrivesOperation) PollResults(ctx context.Context) error {
	allReady := true
	for _, container := range o.containers {
		if container.Status.ExecutionResult == nil {
			allReady = false
			break
		}
	}

	if !allReady {
		return lifecycle.NewWaitError(fmt.Errorf("not all container execution results are ready"))
	}

	return nil
}

func (o *SignDrivesOperation) ProcessResult(ctx context.Context) error {
	res, err := processResult(ctx, o.containers)
	if res != nil {
		o.results = *res
	}
	return err
}

func (o *SignDrivesOperation) GetResult() DiscoverDrivesResult {
	return o.results
}

func (o *SignDrivesOperation) GetJsonResult() string {
	total := 0
	errs := []string{}
	maxErrors := 5

	if o.results.Err != "" {
		return o.results.Err
	}

	drivesByNode := map[string]int{}
	for nodeName, nodeResults := range o.results.Results {
		total += len(nodeResults.Drives)
		drivesByNode[nodeName] = len(nodeResults.Drives)
		if nodeResults.Err != nil {
			if len(errs) < maxErrors {
				errs = append(errs, nodeResults.Err.Error())
			}
		}
	}

	msg := fmt.Sprintf("Signed %d drives on %d nodes", total, len(o.results.Results))
	ret := map[string]interface{}{
		"message": msg,
		"result":  drivesByNode,
		"errors":  errs,
	}

	o.RecordEvent("SignDrives", msg)

	resultJSON, _ := json.Marshal(ret)
	return string(resultJSON)
}

func (o *SignDrivesOperation) DeleteContainers(ctx context.Context) error {
	for _, container := range o.containers {
		if container == nil {
			continue
		}
		err := o.client.Delete(ctx, container)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	o.containers = nil
	return nil
}

func (o *SignDrivesOperation) IsDone() bool {
	return o.ownerStatus == "Done"
}

func (o *SignDrivesOperation) SuccessUpdate(ctx context.Context) error {
	return o.successCallback(ctx)
}

func (o *SignDrivesOperation) FailureCallback(ctx context.Context) error {
	if o.failureCallback == nil {
		return nil
	}
	return o.failureCallback(ctx)
}

func (o *SignDrivesOperation) OperationFailed() bool {
	return o.results.Err != ""
}

func (o *SignDrivesOperation) RecordEvent(reason string, message string) error {
	if o.ownerRef == nil {
		return fmt.Errorf("ownerRef is nil")
	}

	o.recorder.Event(o.ownerRef, v1.EventTypeNormal, reason, message)
	return nil
}
