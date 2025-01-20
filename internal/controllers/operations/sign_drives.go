package operations

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	util2 "github.com/weka/weka-operator/pkg/util"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	force           bool
	tolerations     []v1.Toleration
}

func (o *SignDrivesOperation) AsStep() lifecycle.Step {
	return lifecycle.Step{
		Name: "SignDrives",
		Run:  AsRunFunc(o),
	}
}

func NewSignDrivesOperation(mgr ctrl.Manager, payload *weka.SignDrivesPayload, ownerRef client.Object, ownerDetails weka.WekaContainerDetails, ownerStatus string, successCallback lifecycle.StepFunc, force bool) *SignDrivesOperation {
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
		{Name: "UpdateNodeAnnotations", Run: o.UpdateNodeAnnotations},
		{Name: "SuccessUpdate", Run: o.SuccessUpdate},
		{Name: "DeleteOnFinish", Run: o.DeleteContainers},
	}
}

func (o *SignDrivesOperation) GetContainers(ctx context.Context) error {
	existing, err := discovery.GetOwnedContainers(ctx, o.client, o.ownerRef.GetUID(), o.ownerRef.GetNamespace(), weka.WekaContainerModeAdhocOpWC)
	if err != nil {
		return err
	}
	o.containers = existing
	return nil
}

func (o *SignDrivesOperation) EnsureContainers(ctx context.Context) error {
	var instructions string

	instructionsStr, err := json.Marshal(o.payload)
	if err != nil {
		return err
	}
	instructions = string(instructionsStr)

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

	for _, node := range matchingNodes {
		if existingContainerNodes[node.Name] {
			continue
		}

		// if data exists and not force - skip
		if !o.force {
			if node.Annotations["weka.io/weka-drives"] != "" {
				continue
			}
		}

		labels := map[string]string{
			"weka.io/mode": weka.WekaContainerModeAdhocOpWC,
		}
		labels = util2.MergeMaps(o.ownerRef.GetLabels(), labels)

		containerName := fmt.Sprintf("weka-sign-and-discover-drives-%s", node.Name)
		newContainer := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      containerName,
				Namespace: o.ownerRef.GetNamespace(),
				Labels:    labels,
			},
			Spec: weka.WekaContainerSpec{
				Mode:            weka.WekaContainerModeAdhocOpWC,
				Port:            weka.StaticPortAdhocyWCOperations,
				AgentPort:       weka.StaticPortAdhocyWCOperationsAgent,
				NodeAffinity:    weka.NodeName(node.Name),
				Image:           o.image,
				ImagePullSecret: o.pullSecret,
				Instructions:    instructions,
				Tolerations:     o.tolerations,
			},
		}

		err = controllerutil.SetControllerReference(o.ownerRef, newContainer, o.scheme)
		if err != nil {
			return errors.Wrap(err, "failed to set controller reference")
		}

		err = o.client.Create(ctx, newContainer)
		if err != nil {
			return errors.Wrap(err, "failed to create container")
		}

		o.containers = append(o.containers, newContainer)
	}

	return nil
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

func (o *SignDrivesOperation) UpdateNodeAnnotations(ctx context.Context) error {
	return updateNodeAnnotations(ctx, o.client, &o.results)
}

func (o *SignDrivesOperation) GetResult() DiscoverDrivesResult {
	return o.results
}

func (o *SignDrivesOperation) GetJsonResult() string {
	total := 0
	errs := []string{}
	maxErrors := 30

	for _, nodeResults := range o.results.Results {
		total += len(nodeResults.Drives)
		if len(errs) < maxErrors {
			errs = append(errs, nodeResults.Err.Error())
		}
	}
	ret := map[string]interface{}{
		"result": fmt.Sprintf("Signed %d drives on %d nodes", total, len(o.results.Results)),
		"errors": errs,
	}

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
