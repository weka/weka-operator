package operations

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	util2 "github.com/weka/weka-operator/pkg/util"
)

type EnsureNICsOperation struct {
	client          client.Client
	kubeService     kubernetes.KubeService
	scheme          *runtime.Scheme
	payload         *weka.EnsureNICsPayload
	image           string
	pullSecret      string
	serviceAccount  string
	containers      []*weka.WekaContainer
	ownerRef        client.Object
	results         EnsureNICsResult
	ownerStatus     string
	mgr             ctrl.Manager
	successCallback lifecycle.StepFunc
	tolerations     []v1.Toleration
}

func (o *EnsureNICsOperation) AsStep() lifecycle.Step {
	return lifecycle.Step{
		Name: "EnsureNICs",
		Run:  AsRunFunc(o),
	}
}

type ensureNICsResult struct {
	Err     error        `json:"err"`
	NICs    []domain.NIC `json:"nics"`
	Ensured bool         `json:"ensured"`
}

type EnsureNICsResult struct {
	Err     error                       `json:"err,omitempty"`
	Results map[string]ensureNICsResult `json:"results"`
}

func NewEnsureNICsOperation(mgr ctrl.Manager, payload *weka.EnsureNICsPayload, ownerRef client.Object, ownerDetails weka.WekaOwnerDetails, ownerStatus string, successCallback lifecycle.StepFunc) *EnsureNICsOperation {
	kclient := mgr.GetClient()
	return &EnsureNICsOperation{
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
	}
}

func (o *EnsureNICsOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		{Name: "GetContainers", Run: o.GetContainers},
		{Name: "DeleteOnDone", Run: o.DeleteContainers, Predicates: lifecycle.Predicates{o.IsDone}, ContinueOnPredicatesFalse: true, FinishOnSuccess: true},
		{Name: "EnsureContainers", Run: o.EnsureContainers},
		{Name: "PollResults", Run: o.PollResults},
		{Name: "ProcessResult", Run: o.ProcessResult},
		{Name: "SuccessUpdate", Run: o.SuccessUpdate},
		{Name: "DeleteOnFinish", Run: o.DeleteContainers},
	}
}

func (o *EnsureNICsOperation) GetContainers(ctx context.Context) error {
	existing, err := discovery.GetOwnedContainers(ctx, o.client, o.ownerRef.GetUID(), o.ownerRef.GetNamespace(), weka.WekaContainerModeAdhocOpWC)
	if err != nil {
		return err
	}
	o.containers = existing
	return nil
}

func (o *EnsureNICsOperation) EnsureContainers(ctx context.Context) error {
	payloadBytes, err := json.Marshal(o.payload)
	if err != nil {
		return err
	}

	instructions := &weka.Instructions{
		Type:    "ensure-nics",
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

	for _, node := range matchingNodes {
		if existingContainerNodes[node.Name] {
			continue
		}

		labels := map[string]string{
			"weka.io/mode": weka.WekaContainerModeAdhocOpWC,
		}
		labels = util2.MergeMaps(o.ownerRef.GetLabels(), labels)

		containerName := fmt.Sprintf("weka-adhoc-%s-%s", o.ownerRef.GetName(), node.GetUID())
		newContainer := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      containerName,
				Namespace: o.ownerRef.GetNamespace(),
				Labels:    labels,
			},
			Spec: weka.WekaContainerSpec{
				Mode:               weka.WekaContainerModeAdhocOpWC,
				Port:               weka.StaticPortAdhocyWCOperations,
				AgentPort:          weka.StaticPortAdhocyWCOperationsAgent,
				NodeAffinity:       weka.NodeName(node.Name),
				Image:              o.image,
				ImagePullSecret:    o.pullSecret,
				Instructions:       instructions,
				Tolerations:        o.tolerations,
				ServiceAccountName: o.serviceAccount,
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

func (o *EnsureNICsOperation) PollResults(ctx context.Context) error {
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

func (o *EnsureNICsOperation) ProcessResult(ctx context.Context) error {
	results := make(map[string]ensureNICsResult)
	errorCount := 0

	for _, container := range o.containers {
		var opResult ensureNICsResult
		err := json.Unmarshal([]byte(*container.Status.ExecutionResult), &opResult)
		if err != nil {
			errs := err.Error()
			results[string(container.GetNodeAffinity())] = ensureNICsResult{
				Err: fmt.Errorf("failed to unmarshal execution result: %s", errs),
			}
			continue
		}
		results[string(container.GetNodeAffinity())] = opResult
		if opResult.Err != nil {
			errorCount++
		}

		node, err := o.kubeService.GetNode(ctx, types.NodeName(container.GetNodeAffinity()))
		if err != nil {
			return err
		}
		annotations := node.Annotations
		if annotations == nil {
			annotations = make(map[string]string)
		}
		nicsBytes, err := json.Marshal(opResult.NICs)
		if err != nil {
			return errors.Wrap(err, "Failed to marshal NICs to string")
		}
		annotations[domain.WEKANICs] = string(nicsBytes)
		node.Annotations = annotations
		err = o.client.Update(ctx, node)
		if err != nil {
			return lifecycle.NewWaitError(err)
		}

		node.Status.Capacity["weka.io/nics"] = *resource.NewQuantity(int64(len(opResult.NICs)), resource.DecimalSI)
		node.Status.Allocatable["weka.io/nics"] = *resource.NewQuantity(int64(len(opResult.NICs)), resource.DecimalSI)
		err = o.client.Status().Update(ctx, node)
		if err != nil {
			err = fmt.Errorf("error updating node status: %w", err)
			return err
		}
	}

	finalResult := EnsureNICsResult{
		Results: results,
	}

	if errorCount > 0 {
		finalResult.Err = fmt.Errorf("operation failed on %d nodes", errorCount)
	}

	o.results = finalResult
	return nil
}

func (o *EnsureNICsOperation) GetResult() EnsureNICsResult {
	return o.results
}

func (o *EnsureNICsOperation) GetJsonResult() string {
	resultJSON, _ := json.Marshal(o.results)
	return string(resultJSON)
}

func (o *EnsureNICsOperation) DeleteContainers(ctx context.Context) error {
	err := o.GetContainers(ctx)
	if err != nil {
		return err
	}

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

func (o *EnsureNICsOperation) IsDone() bool {
	return o.ownerStatus == "Done"
}

func (o *EnsureNICsOperation) Cleanup() lifecycle.Step {
	return lifecycle.Step{
		Name: "DeleteContainers",
		Run:  o.DeleteContainers,
	}
}

func (o *EnsureNICsOperation) SuccessUpdate(ctx context.Context) error {
	return o.successCallback(ctx)
}
