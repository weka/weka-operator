package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-lib/pkg/workers"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
)

type SignedDrivesExtendedPayload struct {
	weka.SignDrivesPayload
	ExcludedSerialIds     []string `json:"excludedSerialIds,omitempty"`
	SsdProxyContainerUuid string   `json:"ssd_proxy_container_uuid,omitempty"`
}

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
	return &lifecycle.SimpleStep{
		Name: "SignDrives",
		Run:  AsRunFunc(o),
	}
}

func NewSignDrivesOperation(mgr ctrl.Manager, payload *weka.SignDrivesPayload, ownerRef client.Object, ownerDetails weka.WekaOwnerDetails, ownerStatus string, successCallback, failureCallback lifecycle.StepFunc, force bool) *SignDrivesOperation { //nolint:gocritic // intentional code pattern, linter suggestion does not apply here
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
		&lifecycle.SimpleStep{Name: "GetContainers", Run: o.GetContainers},
		&lifecycle.SimpleStep{Name: "DeleteOnDone", Run: o.DeleteContainers, Predicates: lifecycle.Predicates{o.IsDone}, FinishOnSuccess: true},
		&lifecycle.SimpleStep{Name: "EnsureContainers", Run: o.EnsureContainers},
		&lifecycle.SimpleStep{Name: "PollResults", Run: o.PollResults},
		&lifecycle.SimpleStep{Name: "ProcessResult", Run: o.ProcessResult},
		&lifecycle.SimpleStep{
			Name: "FailureUpdate",
			Run:  o.FailureCallback,
			Predicates: lifecycle.Predicates{
				o.OperationFailed,
			},
			FinishOnSuccess: true,
		},
		&lifecycle.SimpleStep{Name: "SuccessUpdate", Run: o.SuccessUpdate},
		&lifecycle.SimpleStep{Name: "DeleteCompletedContainers", Run: o.DeleteContainers},
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
		o.failureCallback(ctx) //nolint:errcheck // callback error is informational; returning primary error
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	// Create a copy of the original payload to avoid modifying it
	extendedPayload := SignedDrivesExtendedPayload{
		SignDrivesPayload: *o.payload,
	}

	matchingNodes, err := o.kubeService.GetNodes(ctx, o.payload.NodeSelector)
	if err != nil {
		return err
	}

	// filter out nodes that are not ready
	readyNodes := []v1.Node{}
	for i := range matchingNodes {
		if resources.NodeIsReady(&matchingNodes[i]) {
			readyNodes = append(readyNodes, matchingNodes[i])
		} else {
			logger.Info("Skipping node that is not ready", "node", matchingNodes[i].Name)
		}
	}

	if len(readyNodes) == 0 {
		return fmt.Errorf("no matching nodes found for the given node selector")
	}

	existingContainerNodes := make(map[string]bool)
	for _, container := range o.containers {
		existingContainerNodes[string(container.GetNodeAffinity())] = true
	}

	defer logger.SetValues("readyNodes", len(readyNodes), "existingContainers", len(existingContainerNodes))
	logger.SetAttributes()

	//TODO: Re-factor to all pieces of results will be a generic results structure, allowing to implement generic parallezation with callback funcs
	newlyCreated := 0
	skip := 0

	toCreate := []*weka.WekaContainer{}
	for i := range readyNodes {
		node := &readyNodes[i]
		if existingContainerNodes[node.Name] {
			continue
		}

		// if data exists and not force - skip
		if !o.force {
			// If weka-full-drives annotation is absent, the node hasn't been updated yet.
			// Invalidate hash so sign-drives re-runs and writes both annotations.
			if _, hasFullDrives := node.Annotations[consts.AnnotationWekaFullDrives]; !hasFullDrives {
				if node.Annotations[consts.AnnotationWekaDrives] != "" && node.Annotations[consts.AnnotationSignDrivesHash] != "" {
					// Clear hash so sign-drives re-runs and writes the new annotations
					delete(node.Annotations, consts.AnnotationSignDrivesHash)
					if updateErr := o.client.Update(ctx, node); updateErr != nil {
						return fmt.Errorf("failed to clear sign-drives hash for format migration on node %s: %w", node.Name, updateErr)
					}
				}
			}

			targetHash := domain.CalculateNodeDriveSignHash(node)
			if node.Annotations[consts.AnnotationSignDrivesHash] == targetHash {
				skip += 1
				continue
			}
		}

		// read signed drives from weka.io/weka-drives node annotation and add to exclusions
		alreadySignedDrives := getAlreadySignedDrives(node)
		if len(alreadySignedDrives) > 0 {
			extendedPayload.ExcludedSerialIds = alreadySignedDrives

			logger.Info("Updating exclusions with previously signed drives to avoid re-signing", "node", node.Name, "excludedDrives", alreadySignedDrives)
		}

		if o.payload.Shared {
			// in drive sharing mode, set the ssd proxy socket path in the instructions payload
			ssdProxyUuid, ssdErr := o.GetSsdProxyContainerUuid(ctx, node.Name)
			if ssdErr != nil {
				return errors.Wrap(ssdErr, "failed to get ssdproxy container uuid")
			}
			if ssdProxyUuid != nil {
				extendedPayload.SsdProxyContainerUuid = *ssdProxyUuid

				logger.Info("Setting ssdproxy container uuid in sign-drives payload", "node", node.Name, "ssdProxyContainerUuid", *ssdProxyUuid)
			}
		}

		instructions, instrErr := o.createInstructions(&extendedPayload)
		if instrErr != nil {
			return instrErr
		}

		nodeLabels := util.MergeMaps(o.ownerRef.GetLabels(), factory.RequiredAnyWekaContainerLabels(weka.WekaContainerModeAdhocOp))

		containerName := fmt.Sprintf("weka-sign-and-discover-drives-%s", node.Name)
		newContainer := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      containerName,
				Namespace: o.ownerRef.GetNamespace(),
				Labels:    nodeLabels,
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
		if refErr := controllerutil.SetControllerReference(o.ownerRef, container, o.scheme); refErr != nil {
			return errors.Wrap(refErr, "failed to set controller reference")
		}

		if createErr := o.client.Create(ctx, container); createErr != nil {
			return errors.Wrap(createErr, "failed to create container")
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

// isResultsProcessed returns true when the WekaContainer reconciliation has run
// processResults (i.e. updateNodeAnnotations). Deleting before this condition is set
// causes node annotations to never be written.
func isResultsProcessed(container *weka.WekaContainer) bool {
	for _, c := range container.Status.Conditions {
		if c.Type == condition.CondResultsProcessed && c.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

func (o *SignDrivesOperation) PollResults(ctx context.Context) error {
	// if force is not set, do not wait for all results, and return as many are fully processed
	if !o.force {
		// wait for at least one result to have node annotations updated
		for _, container := range o.containers {
			if isResultsProcessed(container) {
				return nil
			}
		}
	}

	allReady := true
	for _, container := range o.containers {
		if !isResultsProcessed(container) {
			allReady = false
			break
		}
	}

	if !allReady {
		return lifecycle.NewWaitError(fmt.Errorf("not all container results are processed yet"))
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
		drivesCount := len(nodeResults.Drives) + len(nodeResults.ProxyDrives)
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

	resultJSON, _ := json.Marshal(ret) //nolint:errcheck // marshal of known-serializable struct; error not possible
	res := string(resultJSON)

	if len(drivesByNode) > 0 {
		_ = o.RecordEvent("SignDrives", res) //nolint:errcheck // event recording is best-effort; error not actionable
	}
	return res
}

func (o *SignDrivesOperation) DeleteContainers(ctx context.Context) error {
	updatedContainers := []*weka.WekaContainer{}

	for _, container := range o.containers {
		if isResultsProcessed(container) || o.force {
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

func (o *SignDrivesOperation) RecordEvent(reason, message string) error {
	if o.ownerRef == nil {
		return fmt.Errorf("ownerRef is nil")
	}

	o.recorder.Event(o.ownerRef, v1.EventTypeNormal, reason, message)
	return nil
}

// getAlreadySignedDrives extracts the list of already signed drives from node annotations.
// It checks both weka.io/weka-drives (regular mode) and weka.io/weka-shared-drives (drive sharing mode).
func getAlreadySignedDrives(node *v1.Node) []string {
	alreadySignedDrives := []string{}

	if node.Annotations == nil {
		return alreadySignedDrives
	}

	// Regular drives (non-proxy mode) — reads serials from both new and legacy annotations
	fullAnnotation := node.Annotations[consts.AnnotationWekaFullDrives]
	legacyAnnotation := node.Annotations[consts.AnnotationWekaDrives]
	if serials, err := domain.ReadAnnotatedDriveSerials(fullAnnotation, legacyAnnotation); err == nil {
		alreadySignedDrives = append(alreadySignedDrives, serials...)
	}

	// Shared drives (proxy/drive sharing mode)
	if sharedDrivesStr, ok := node.Annotations[consts.AnnotationSharedDrives]; ok && sharedDrivesStr != "" {
		var sharedDrives []domain.SharedDriveInfo
		if err := json.Unmarshal([]byte(sharedDrivesStr), &sharedDrives); err == nil {
			for _, drive := range sharedDrives {
				alreadySignedDrives = append(alreadySignedDrives, drive.Serial)
			}
		}
	}

	return alreadySignedDrives
}

func (o *SignDrivesOperation) createInstructions(extendedPayload *SignedDrivesExtendedPayload) (*weka.Instructions, error) {
	// Marshal the extended payload
	payloadBytes, err := json.Marshal(extendedPayload)
	if err != nil {
		return nil, err
	}

	instructions := &weka.Instructions{
		Type:    "sign-drives",
		Payload: string(payloadBytes),
	}

	return instructions, nil
}

func (o *SignDrivesOperation) GetSsdProxyContainerUuid(ctx context.Context, nodeName string) (*string, error) {
	// Get the operator namespace where ssdproxy containers are deployed
	ssdProxy, err := discovery.GetSsdProxyOnNode(ctx, o.client, weka.NodeName(nodeName))
	var notFoundErr *discovery.SsdProxyNotFoundError
	if errors.As(err, &notFoundErr) {
		// No ssdproxy found on the node, return nil
		return nil, nil
	}

	if err != nil {
		return nil, errors.Wrap(err, "failed to get ssdproxy container on node")
	}

	uuid := string(ssdProxy.GetUID())
	return &uuid, nil
}
