package operations

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type DiscoverDrivesOperation struct {
	client          client.Client
	kubeService     kubernetes.KubeService
	scheme          *runtime.Scheme
	payload         *v1alpha1.DiscoverDrivesPayload
	namespace       string
	image           string
	pullSecret      string
	result          map[string]nodeResult
	containers      []*v1alpha1.WekaContainer
	ownerRef        client.Object
	results         DiscoverDrivesResult
	ownerStatus     string
	mgr             ctrl.Manager
	successCallback lifecycle.StepFunc
	force           bool
}

type DriveInfo struct {
	SerialId   string `json:"serial_id"`
	WekaGuid   string `json:"weka_guid"`
	DevicePath string `json:"block_device"`
	Partition  string `json:"partition"`
}

type driveNodeResults struct {
	Err    error       `json:"err"`
	Drives []DriveInfo `json:"drives"`
}

type DiscoverDrivesResult struct {
	Err     error                       `json:"err,omitempty"`
	Results map[string]driveNodeResults `json:"results"`
}

func NewDiscoverDrivesOperation(mgr ctrl.Manager, payload *v1alpha1.DiscoverDrivesPayload, ownerRef client.Object, ownerDetails v1alpha1.OwnerWekaObject, ownerStatus string, successCallback lifecycle.StepFunc, force bool) *DiscoverDrivesOperation {
	kclient := mgr.GetClient()
	return &DiscoverDrivesOperation{
		mgr:             mgr,
		client:          kclient,
		kubeService:     kubernetes.NewKubeService(kclient),
		scheme:          mgr.GetScheme(),
		payload:         payload,
		image:           ownerDetails.Image,
		pullSecret:      ownerDetails.ImagePullSecret,
		result:          make(map[string]nodeResult),
		ownerRef:        ownerRef,
		ownerStatus:     ownerStatus,
		successCallback: successCallback,
		namespace:       ownerRef.GetNamespace(),
		force:           force,
	}
}

func (o *DiscoverDrivesOperation) AsStep() lifecycle.Step {
	return lifecycle.Step{
		Name: "DiscoverDrives",
		Run:  AsRunFunc(o),
	}
}

func (o *DiscoverDrivesOperation) GetSteps() []lifecycle.Step {
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

func (o *DiscoverDrivesOperation) GetContainers(ctx context.Context) error {
	existing, err := discovery.GetOwnedContainers(ctx, o.client, o.ownerRef.GetUID(), o.namespace, v1alpha1.WekaContainerModeAdhocOp)
	if err != nil {
		return err
	}
	o.containers = existing
	return nil
}

func (o *DiscoverDrivesOperation) EnsureContainers(ctx context.Context) error {
	instructions := "discover-drives"

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

		//if data exists and not force - skip
		if !o.force {
			if node.Annotations["weka.io/weka-drives"] != "" {
				continue
			}
		}

		containerName := fmt.Sprintf("weka-discover-drives-%s-%s", o.ownerRef.GetUID(), node.GetUID())
		newContainer := &v1alpha1.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      containerName,
				Namespace: o.namespace,
				Labels: map[string]string{
					"weka.io/mode": v1alpha1.WekaContainerModeAdhocOp,
				},
			},
			Spec: v1alpha1.WekaContainerSpec{
				Mode:            v1alpha1.WekaContainerModeAdhocOp,
				Port:            v1alpha1.StaticPortAdhocyWCOperations,
				AgentPort:       v1alpha1.StaticPortAdhocyWCOperationsAgent,
				NodeAffinity:    v1alpha1.NodeName(node.Name),
				Image:           o.image,
				ImagePullSecret: o.pullSecret,
				Instructions:    instructions,
			},
		}

		err = controllerutil.SetControllerReference(o.ownerRef, newContainer, o.scheme)
		if err != nil {
			return err
		}

		err = o.client.Create(ctx, newContainer)
		if err != nil {
			return err
		}

		o.containers = append(o.containers, newContainer)
	}

	return nil
}

func (o *DiscoverDrivesOperation) PollResults(ctx context.Context) error {
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

func (o *DiscoverDrivesOperation) ProcessResult(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ProcessResult")
	defer end()

	results := make(map[string]driveNodeResults)
	errorCount := 0

	for _, container := range o.containers {
		var opResult driveNodeResults
		err := json.Unmarshal([]byte(*container.Status.ExecutionResult), &opResult)
		logger.Info("Processing container result", "container", container.Name, "result", opResult)
		if err != nil {
			errs := err.Error()
			results[string(container.GetNodeAffinity())] = driveNodeResults{
				Err: fmt.Errorf("failed to unmarshal execution result: %s", errs),
			}
			continue
		}
		results[string(container.GetNodeAffinity())] = opResult
		if opResult.Err != nil {
			errorCount++
		}
	}

	finalResult := DiscoverDrivesResult{
		Results: results,
	}

	if errorCount > 0 {
		errs := fmt.Sprintf("operation failed on %d nodes", errorCount)
		finalResult.Err = fmt.Errorf(errs)
	}

	o.results = finalResult
	return nil
}

func (o *DiscoverDrivesOperation) UpdateNodeAnnotations(ctx context.Context) error {
	for nodeName, result := range o.results.Results {
		node := &corev1.Node{}
		if err := o.client.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
			return err
		}

		if node.Annotations == nil {
			node.Annotations = make(map[string]string)
		}

		// Update weka.io/weka-drives annotation
		previousDrives := []string{}
		newDrivesFound := 0
		if existingDrivesStr, ok := node.Annotations["weka.io/weka-drives"]; ok {
			_ = json.Unmarshal([]byte(existingDrivesStr), &previousDrives)
		}

		seenDrives := make(map[string]bool)
		for _, drive := range previousDrives {
			if drive == "" {
				continue // clean bad records of empty serial ids
			}
			seenDrives[drive] = true
		}

		for _, drive := range result.Drives {
			if drive.SerialId == "" { // skip drives without serial id if it was not set for whatever reason
				continue
			}
			if _, ok := seenDrives[drive.SerialId]; !ok {
				newDrivesFound++
			}
			seenDrives[drive.SerialId] = true
		}

		if newDrivesFound == 0 {
			continue
		}

		updatedDrivesList := []string{}
		for drive, _ := range seenDrives {
			updatedDrivesList = append(updatedDrivesList, drive)
		}
		newDrivesStr, _ := json.Marshal(updatedDrivesList)
		node.Annotations["weka.io/weka-drives"] = string(newDrivesStr)

		// Update weka.io/drives extended resource
		blockedDrives := []string{}
		if blockedDrivesStr, ok := node.Annotations["weka.io/blocked-drives"]; ok {
			_ = json.Unmarshal([]byte(blockedDrivesStr), &blockedDrives)
		}
		availableDrives := len(seenDrives) - len(blockedDrives)
		node.Status.Capacity["weka.io/drives"] = *resource.NewQuantity(int64(availableDrives), resource.DecimalSI)

		if err := o.client.Status().Update(ctx, node); err != nil {
			return err
		}

		if err := o.client.Update(ctx, node); err != nil {
			return err
		}
	}
	return nil
}

func (o *DiscoverDrivesOperation) GetResult() DiscoverDrivesResult {
	return o.results
}

func (o *DiscoverDrivesOperation) GetJsonResult() string {
	resultJSON, _ := json.Marshal(o.results)
	return string(resultJSON)
}

func (o *DiscoverDrivesOperation) DeleteContainers(ctx context.Context) error {
	err := o.GetContainers(ctx)
	if err != nil {
		return err
	}

	for _, container := range o.containers {
		if container == nil {
			continue
		}
		err := o.client.Delete(ctx, container)
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	o.containers = nil
	return nil
}

func (o *DiscoverDrivesOperation) IsDone() bool {
	return o.ownerStatus == "Done"
}

func (o *DiscoverDrivesOperation) Cleanup() lifecycle.Step {
	return lifecycle.Step{
		Name: "DeleteContainers",
		Run:  o.DeleteContainers,
	}
}

func (o *DiscoverDrivesOperation) SuccessUpdate(ctx context.Context) error {
	return o.successCallback(ctx)
}
