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

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/weka-operator/internal/pkg/domain"
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
	return &lifecycle.SimpleStep{
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

func NewEnsureNICsOperation(mgr ctrl.Manager, payload *weka.EnsureNICsPayload, ownerRef client.Object, ownerDetails weka.WekaOwnerDetails, ownerStatus string, successCallback lifecycle.StepFunc) *EnsureNICsOperation { //nolint:gocritic // intentional code pattern, linter suggestion does not apply here
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
		&lifecycle.SimpleStep{Name: "GetContainers", Run: o.GetContainers},
		&lifecycle.SimpleStep{Name: "DeleteOnDone", Run: o.DeleteContainers, Predicates: lifecycle.Predicates{o.IsDone}, FinishOnSuccess: true},
		&lifecycle.SimpleStep{Name: "EnsureContainers", Run: o.EnsureContainers},
		&lifecycle.SimpleStep{Name: "ProcessResult", Run: o.ProcessResult},
		&lifecycle.SimpleStep{Name: "SuccessUpdate", Run: o.SuccessUpdate},
		&lifecycle.SimpleStep{Name: "DeleteOnFinish", Run: o.DeleteContainers},
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

	for i := range matchingNodes {
		if existingContainerNodes[matchingNodes[i].Name] {
			continue
		}
		node := &matchingNodes[i]

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

func (o *EnsureNICsOperation) ProcessResult(ctx context.Context) error {
	results := make(map[string]ensureNICsResult)
	errorCount := 0
	allReady := true

	for _, container := range o.containers {
		if container.Status.ExecutionResult == nil {
			allReady = false
			continue
		}

		var opResult ensureNICsResult
		err := json.Unmarshal([]byte(*container.Status.ExecutionResult), &opResult)
		if err != nil {
			results[string(container.GetNodeAffinity())] = ensureNICsResult{
				Err: fmt.Errorf("failed to unmarshal execution result: %w", err),
			}
			continue
		}
		results[string(container.GetNodeAffinity())] = opResult
		if opResult.Err != nil {
			errorCount++
			continue
		}

		// Patch node as soon as its container has results
		if err := o.patchNodeNICs(ctx, container, opResult); err != nil {
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

	if !allReady {
		return lifecycle.NewWaitError(fmt.Errorf("not all container execution results are ready"))
	}

	return nil
}

func (o *EnsureNICsOperation) patchNodeNICs(ctx context.Context, container *weka.WekaContainer, opResult ensureNICsResult) error {
	node, err := o.kubeService.GetNode(ctx, types.NodeName(container.GetNodeAffinity()))
	if err != nil {
		return err
	}

	nicsBytes, err := json.Marshal(opResult.NICs)
	if err != nil {
		return errors.Wrap(err, "failed to marshal NICs")
	}

	// Patch node annotations with NICs, gated on the annotation diff to avoid
	// unnecessary updates on re-polls. Use a merge patch (not a full-object
	// Update) so the change is targeted at the annotation and less conflict-prone.
	if node.Annotations[domain.WEKANICs] != string(nicsBytes) {
		patch := client.MergeFrom(node.DeepCopy())
		if node.Annotations == nil {
			node.Annotations = make(map[string]string)
		}
		node.Annotations[domain.WEKANICs] = string(nicsBytes)
		if err = o.client.Patch(ctx, node, patch); err != nil {
			return lifecycle.NewWaitError(err)
		}
	}

	// Reconcile node extended resources independently of the annotation. The
	// annotation and status are written via separate, non-atomic API calls, so
	// the annotation alone is not a safe "already patched" sentinel: a prior
	// reconcile may have written the annotation but failed the status update.
	desiredQty := *resource.NewQuantity(int64(len(opResult.NICs)), resource.DecimalSI)
	if node.Status.Capacity == nil {
		node.Status.Capacity = v1.ResourceList{}
	}
	if node.Status.Allocatable == nil {
		node.Status.Allocatable = v1.ResourceList{}
	}
	capCur, capOk := node.Status.Capacity[domain.WEKANICs]
	allocCur, allocOk := node.Status.Allocatable[domain.WEKANICs]
	if !capOk || !allocOk || capCur.Cmp(desiredQty) != 0 || allocCur.Cmp(desiredQty) != 0 {
		node.Status.Capacity[domain.WEKANICs] = desiredQty
		node.Status.Allocatable[domain.WEKANICs] = desiredQty
		if err = o.client.Status().Update(ctx, node); err != nil {
			return lifecycle.NewWaitError(fmt.Errorf("error updating node status: %w", err))
		}
	}

	return nil
}

func (o *EnsureNICsOperation) GetResult() EnsureNICsResult {
	return o.results
}

func (o *EnsureNICsOperation) GetJsonResult() string {
	resultJSON, _ := json.Marshal(o.results) //nolint:errcheck // marshal of known-serializable struct; error not possible
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
	return &lifecycle.SimpleStep{
		Name: "DeleteContainers",
		Run:  o.DeleteContainers,
	}
}

func (o *EnsureNICsOperation) SuccessUpdate(ctx context.Context) error {
	return o.successCallback(ctx)
}
