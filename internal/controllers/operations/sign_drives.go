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
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
	"github.com/weka/weka-operator/pkg/workers"
)

type SignDrivesOperation struct {
	client          client.Client
	kubeService     kubernetes.KubeService
	scheme          *runtime.Scheme
	payload         *weka.SignDrivesPayload
	image           string
	pullSecret      string
	serviceAccount  string
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
	return &lifecycle.SingleStep{
		Name: "SignDrives",
		Run:  AsRunFunc(o),
	}
}

func NewSignDrivesOperation(mgr ctrl.Manager, payload *weka.SignDrivesPayload, ownerRef client.Object, ownerDetails weka.WekaOwnerDetails, ownerStatus string, successCallback, failureCallback lifecycle.StepFunc, force bool) *SignDrivesOperation {
	kclient := mgr.GetClient()
	return &SignDrivesOperation{
		mgr:             mgr,
		client:          kclient,
		kubeService:     kubernetes.NewKubeService(kclient),
		scheme:          mgr.GetScheme(),
		payload:         payload,
		image:           ownerDetails.Image,
		pullSecret:      ownerDetails.ImagePullSecret,
		serviceAccount:  ownerDetails.ServiceAccountName,
		ownerRef:        ownerRef,
		ownerStatus:     ownerStatus,
		tolerations:     ownerDetails.Tolerations,
		successCallback: successCallback,
		failureCallback: failureCallback,
		force:           force,
		recorder:        mgr.GetEventRecorderFor("weka-sign-drives"),
	}
}

func (o *SignDrivesOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SingleStep{Name: "GetContainers", Run: o.GetContainers},
		&lifecycle.SingleStep{Name: "DeleteOnDone", Run: o.DeleteContainers, Predicates: []lifecycle.PredicateFunc{o.IsDone}, FinishOnSuccess: true},
		&lifecycle.SingleStep{Name: "EnsureContainers", Run: o.EnsureContainers},
		&lifecycle.SingleStep{Name: "PollResults", Run: o.PollResults},
		&lifecycle.SingleStep{Name: "ProcessResult", Run: o.ProcessResult},
		&lifecycle.SingleStep{
			Name: "FailureUpdate",
			Run:  o.FailureCallback,
			Predicates: []lifecycle.PredicateFunc{
				o.OperationFailed,
			},
			FinishOnSuccess: true,
		},
		&lifecycle.SingleStep{Name: "SuccessUpdate", Run: o.SuccessUpdate},
		&lifecycle.SingleStep{Name: "DeleteCompletedContainers", Run: o.DeleteContainers},
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
		if !o.force {
			targetHash := domain.CalculateNodeDriveSignHash(&node)
			if node.Annotations["weka.io/sign-drives-hash"] == targetHash {
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
				Mode:               weka.WekaContainerModeAdhocOp,
				NodeAffinity:       weka.NodeName(node.Name),
				Image:              o.image,
				ImagePullSecret:    o.pullSecret,
				Instructions:       instructions,
				Tolerations:        o.tolerations,
				HostPID:            true,
				ServiceAccountName: o.serviceAccount,
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
	// if force is not set, do not wait for all results, and return as many as are completed instead
	if !o.force {
		// wait for at least one result to be ready
		for _, container := range o.containers {
			if container.Status.ExecutionResult != nil {
				return nil
			}
		}
	}

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
	res, err := processResult(ctx, o.containers, !o.force)
	if err != nil || res == nil {
		return err
	}
	o.results = *res
	return err
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
		drivesCount := len(nodeResults.Drives)
		total += drivesCount
		if drivesCount > 0 {
			drivesByNode[nodeName] = drivesCount
		}
		if nodeResults.Err != nil {
			if len(errs) < maxErrors {
				errs = append(errs, nodeResults.Err.Error())
			}
		}
	}

	ret := map[string]interface{}{}
	if len(drivesByNode) > 0 {
		ret["results"] = drivesByNode
		ret["message"] = fmt.Sprintf("Signed %d drives on %d nodes", total, len(o.results.Results))
	} else {
		ret["message"] = "No new drives signed"
	}
	if len(errs) > 0 {
		ret["errors"] = errs
	}

	resultJSON, _ := json.Marshal(ret)
	res := string(resultJSON)

	if len(drivesByNode) > 0 {
		o.RecordEvent("SignDrives", res)
	}
	return res
}

func (o *SignDrivesOperation) DeleteContainers(ctx context.Context) error {
	updatedContainers := []*weka.WekaContainer{}

	for _, container := range o.containers {
		if container.Status.ExecutionResult != nil || o.force {
			err := o.client.Delete(ctx, container)
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		} else {
			updatedContainers = append(updatedContainers, container)
		}
	}
	o.containers = updatedContainers
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
