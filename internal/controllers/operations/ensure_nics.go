package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/pkg/errors"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	util2 "github.com/weka/weka-operator/pkg/util"
)

const (
	ensureNICsEventReasonFailed   = "EnsureNICsFailed"
	ensureNICsEventReasonRetrying = "EnsureNICsRetrying"
	EnsureNICsWaitDuration        = 15 * time.Second
	ensureNICsMaxBackoff          = 2 * time.Minute
	ensureNICsMaxAttempts         = 10
	ensureNICsAttemptsAnnotation  = "weka.io/ensure-nics-attempts"
)

// Cloud APIs reject back-to-back VNIC attachments transiently: HTTP 409 (OCI "instance is currently
// being modified, try again later"), 429/throttling and 5xx. Anything else, e.g. MaxNicsExceededError, is terminal.
var transientNICErrorPattern = regexp.MustCompile(`(?i)(status|code)\D{0,10}(409|429|5\d\d)\b|IncorrectState|try again later|TooManyRequests|throttl|InternalServerError|ServiceUnavailable`)

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
	npnPendingNodes []string
	retryAfter      time.Time

	recorder         events.EventRecorder
	progressCallback lifecycle.StepFunc
	failureCallback  lifecycle.StepFunc
}

func (o *EnsureNICsOperation) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "EnsureNICs",
		Run:  AsRunFunc(o),
	}
}

// nodeAttempts is persisted on the owner: owner updates trigger immediate reconciles, so the backoff
// must be enforced from RetryAfter rather than by the wait duration alone.
type nodeAttempts struct {
	Attempts   int       `json:"attempts"`
	RetryAfter time.Time `json:"retryAfter"`
}

type ensureNICsResult struct {
	Err     string       `json:"err"`
	NICs    []domain.NIC `json:"nics"`
	Ensured bool         `json:"ensured"`
	// Retryable is set by cloud-helper when it knows the failure is transient.
	Retryable bool `json:"retryable,omitempty"`
}

func (r ensureNICsResult) isRetryable() bool {
	return r.Err != "" && (r.Retryable || transientNICErrorPattern.MatchString(r.Err))
}

type EnsureNICsResult struct {
	Err     string                      `json:"err,omitempty"`
	Results map[string]ensureNICsResult `json:"results"`
}

func NewEnsureNICsOperation(mgr ctrl.Manager, payload *weka.EnsureNICsPayload, ownerRef client.Object, ownerDetails weka.WekaOwnerDetails, ownerStatus string, retryAfter time.Time, recorder events.EventRecorder, progressCallback, successCallback, failureCallback lifecycle.StepFunc) *EnsureNICsOperation { //nolint:gocritic // intentional code pattern, linter suggestion does not apply here
	kclient := mgr.GetClient()
	return &EnsureNICsOperation{
		mgr:              mgr,
		client:           kclient,
		kubeService:      kubernetes.NewKubeService(kclient),
		scheme:           mgr.GetScheme(),
		payload:          payload,
		image:            ownerDetails.Image,
		pullSecret:       ownerDetails.ImagePullSecret,
		serviceAccount:   ownerDetails.ServiceAccountName,
		ownerRef:         ownerRef,
		ownerStatus:      ownerStatus,
		retryAfter:       retryAfter,
		tolerations:      ownerDetails.Tolerations,
		successCallback:  successCallback,
		recorder:         recorder,
		progressCallback: progressCallback,
		failureCallback:  failureCallback,
	}
}

func (o *EnsureNICsOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{Name: "SkipIfTerminal", Run: o.DeleteContainers, Predicates: lifecycle.Predicates{o.IsTerminal}, FinishOnSuccess: true},
		&lifecycle.SimpleStep{Name: "GetContainers", Run: o.GetContainers},
		&lifecycle.SimpleStep{Name: "WaitForTerminatingContainers", Run: o.WaitForTerminatingContainers},
		&lifecycle.SimpleStep{Name: "WaitRetryAfter", Run: o.WaitRetryAfter, Predicates: lifecycle.Predicates{o.isBackingOff}},
		&lifecycle.SimpleStep{Name: "MarkRunning", Run: o.progressCallback, Predicates: lifecycle.Predicates{func() bool { return o.progressCallback != nil }}},
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

// Finalizers keep deleted containers listable with their previous results, which must not be read as current.
func (o *EnsureNICsOperation) WaitForTerminatingContainers(_ context.Context) error {
	for _, container := range o.containers {
		if container.DeletionTimestamp != nil {
			return lifecycle.NewWaitError(fmt.Errorf("waiting for container %s to finish terminating", container.Name))
		}
	}
	return nil
}

func (o *EnsureNICsOperation) isBackingOff() bool {
	return time.Now().Before(o.retryAfter)
}

func (o *EnsureNICsOperation) WaitRetryAfter(ctx context.Context) error {
	if err := o.DeleteContainers(ctx); err != nil {
		return err
	}
	return lifecycle.NewWaitErrorWithDuration(errors.New("backing off after failure"), time.Until(o.retryAfter))
}

// ensureNICsInstructionPayload carries per-node data the runtime needs but the user API does not expose.
type ensureNICsInstructionPayload struct {
	*weka.EnsureNICsPayload
	ExcludeMACs []string `json:"excludeMACs,omitempty"`
}

// OKE VCN-Native Pod Networking attaches pod VNICs to every node; IMDS cannot tell them apart from
// Weka data VNICs, so they must be excluded using the cluster-scoped NativePodNetwork objects.
var nativePodNetworkListGVK = schema.GroupVersionKind{Group: "oci.oraclecloud.com", Version: "v1beta1", Kind: "NativePodNetworkList"}

// getPodVNICMACs returns nil when the cluster does not use NPN. Otherwise it maps each node whose
// NativePodNetwork reached SUCCESS to its pod VNIC MACs; a node missing from the map is not ready yet.
func (o *EnsureNICsOperation) getPodVNICMACs(ctx context.Context) (map[string][]string, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(nativePodNetworkListGVK)
	if err := o.mgr.GetAPIReader().List(ctx, list); err != nil {
		if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, errors.Wrap(err, "failed to list NativePodNetworks (operator needs get/list on nativepodnetworks.oci.oraclecloud.com)")
	}
	return podVNICMACsByNode(list.Items), nil
}

func podVNICMACsByNode(items []unstructured.Unstructured) map[string][]string {
	if len(items) == 0 {
		return nil
	}
	result := map[string][]string{}
	for _, item := range items {
		if state, _, _ := unstructured.NestedString(item.Object, "status", "state"); state != "SUCCESS" { //nolint:errcheck // a missing or malformed state means not ready
			continue
		}
		vnics, _, _ := unstructured.NestedSlice(item.Object, "status", "vnics") //nolint:errcheck // no or malformed status.vnics means nothing to exclude
		for _, owner := range item.GetOwnerReferences() {
			if owner.Kind != "Node" {
				continue
			}
			macs := []string{}
			for _, v := range vnics {
				vnic, ok := v.(map[string]any)
				if !ok {
					continue
				}
				if mac, ok := vnic["macAddress"].(string); ok && mac != "" {
					macs = append(macs, mac)
				}
			}
			result[owner.Name] = macs
		}
	}
	return result
}

func (o *EnsureNICsOperation) EnsureContainers(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

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

	attempts, err := o.getAttempts()
	if err != nil {
		return err
	}
	var retryWait time.Duration

	var podVNICMACs map[string][]string
	podVNICMACsLoaded := false
	o.npnPendingNodes = nil
	for i := range matchingNodes {
		if existingContainerNodes[matchingNodes[i].Name] {
			continue
		}
		node := &matchingNodes[i]

		if remaining := time.Until(attempts[node.Name].RetryAfter); remaining > 0 {
			retryWait = max(retryWait, remaining)
			continue
		}

		if !podVNICMACsLoaded {
			podVNICMACs, err = o.getPodVNICMACs(ctx)
			if err != nil {
				return err
			}
			podVNICMACsLoaded = true
		}
		// The instruction is fixed at container creation, so a node whose pod VNICs are not known yet must wait.
		excludeMACs, ready := podVNICMACs[node.Name]
		if podVNICMACs != nil && !ready {
			o.npnPendingNodes = append(o.npnPendingNodes, node.Name)
			continue
		}
		if len(excludeMACs) > 0 {
			logger.Info("Excluding OKE pod VNICs from Weka data NICs", "node", node.Name, "macs", excludeMACs)
		}
		payloadBytes, err := json.Marshal(ensureNICsInstructionPayload{EnsureNICsPayload: o.payload, ExcludeMACs: excludeMACs})
		if err != nil {
			return err
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
				Instructions:       &weka.Instructions{Type: weka.InstructionTypeEnsureNICs, Payload: string(payloadBytes)},
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

	if retryWait > 0 {
		return lifecycle.NewWaitErrorWithDuration(fmt.Errorf("waiting to retry ensure-nics after a transient failure"), retryWait)
	}
	return nil
}

func (o *EnsureNICsOperation) ProcessResult(ctx context.Context) error {
	results := make(map[string]ensureNICsResult)
	errorCount := 0
	allReady := true
	attempts, err := o.getAttempts()
	if err != nil {
		return err
	}
	retries := map[string]*weka.WekaContainer{}

	for _, container := range o.containers {
		if container.Status.ExecutionResult == nil {
			allReady = false
			continue
		}

		node := string(container.GetNodeAffinity())
		var opResult ensureNICsResult
		err := json.Unmarshal([]byte(*container.Status.ExecutionResult), &opResult)
		if err != nil {
			results[node] = ensureNICsResult{
				Err: fmt.Sprintf("failed to unmarshal execution result: %v", err),
			}
			errorCount++
			continue
		}
		if opResult.isRetryable() {
			if time.Now().Before(attempts[node].RetryAfter) {
				// Already counted; only the delete in retryNodes failed.
				if err := o.client.Delete(ctx, container); err != nil && !apierrors.IsNotFound(err) {
					return err
				}
				allReady = false
				continue
			}
			if attempts[node].Attempts < ensureNICsMaxAttempts {
				retries[node] = container
				results[node] = opResult
				continue
			}
			opResult.Err += fmt.Sprintf(" (gave up after %d attempts)", ensureNICsMaxAttempts)
		}
		results[node] = opResult
		if opResult.Err != "" {
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
		finalResult.Err = fmt.Sprintf("operation failed on %d nodes", errorCount)
	}

	o.results = finalResult
	o.recordNodeFailures(retries)

	if len(retries) > 0 {
		return o.retryNodes(ctx, retries, attempts)
	}
	if !allReady {
		return lifecycle.NewWaitError(fmt.Errorf("not all container execution results are ready"))
	}
	if len(o.npnPendingNodes) > 0 {
		return lifecycle.NewWaitError(fmt.Errorf("waiting for NativePodNetwork to reach SUCCESS on nodes %v", o.npnPendingNodes))
	}

	if errorCount > 0 {
		return o.failTerminally(ctx)
	}

	return nil
}

func (o *EnsureNICsOperation) recordNodeFailures(retries map[string]*weka.WekaContainer) {
	nodes := make([]string, 0, len(o.results.Results))
	for node, r := range o.results.Results {
		if _, retrying := retries[node]; r.Err != "" && !retrying {
			nodes = append(nodes, node)
		}
	}
	sort.Strings(nodes)
	for _, node := range nodes {
		util2.RecordEvent(o.recorder, o.ownerRef, v1.EventTypeWarning, ensureNICsEventReasonFailed, consts.ActionEnsureNICs,
			fmt.Sprintf("node %s: %s", node, o.results.Results[node].Err))
	}
}

// The failure is saved before the containers go, so a failed status write keeps the results to retry from.
func (o *EnsureNICsOperation) failTerminally(ctx context.Context) error {
	if o.failureCallback != nil {
		if err := o.failureCallback(ctx); err != nil {
			return err
		}
	}
	if err := o.DeleteContainers(ctx); err != nil {
		return err
	}
	return lifecycle.NewWaitErrorWithDuration(errors.New(o.results.Err), EnsureNICsWaitDuration)
}

// retryNodes recreates the containers of nodes that failed transiently, counting attempts on the owner so
// they survive reconciles.
func (o *EnsureNICsOperation) retryNodes(ctx context.Context, retries map[string]*weka.WekaContainer, attempts map[string]nodeAttempts) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	nodes := make([]string, 0, len(retries))
	for node := range retries {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)

	var backoff time.Duration
	for _, node := range nodes {
		attempt := attempts[node].Attempts + 1
		delay := min(EnsureNICsWaitDuration<<(attempt-1), ensureNICsMaxBackoff)
		attempts[node] = nodeAttempts{Attempts: attempt, RetryAfter: time.Now().Add(delay)}
		msg := fmt.Sprintf("node %s: attempt %d/%d: %s", node, attempt, ensureNICsMaxAttempts, o.results.Results[node].Err)
		logger.Info("Retrying ensure-nics after transient failure", "node", node, "attempt", attempt, "error", o.results.Results[node].Err)
		util2.RecordEvent(o.recorder, o.ownerRef, v1.EventTypeWarning, ensureNICsEventReasonRetrying, consts.ActionEnsureNICs, msg)
		backoff = max(backoff, delay)
	}
	if err := o.setAttempts(ctx, attempts); err != nil {
		return err
	}
	for _, node := range nodes {
		if err := o.client.Delete(ctx, retries[node]); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return lifecycle.NewWaitErrorWithDuration(fmt.Errorf("retrying ensure-nics on nodes %v", nodes), backoff)
}

func (o *EnsureNICsOperation) getAttempts() (map[string]nodeAttempts, error) {
	attempts := map[string]nodeAttempts{}
	raw := o.ownerRef.GetAnnotations()[ensureNICsAttemptsAnnotation]
	if raw == "" {
		return attempts, nil
	}
	if err := json.Unmarshal([]byte(raw), &attempts); err != nil {
		return nil, errors.Wrapf(err, "failed to parse annotation %s", ensureNICsAttemptsAnnotation)
	}
	return attempts, nil
}

// setAttempts removes the annotation when attempts is nil.
func (o *EnsureNICsOperation) setAttempts(ctx context.Context, attempts map[string]nodeAttempts) error {
	base, ok := o.ownerRef.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("owner %T is not a client.Object", o.ownerRef)
	}
	annotations := o.ownerRef.GetAnnotations()
	if attempts == nil {
		if _, found := annotations[ensureNICsAttemptsAnnotation]; !found {
			return nil
		}
		delete(annotations, ensureNICsAttemptsAnnotation)
	} else {
		raw, err := json.Marshal(attempts)
		if err != nil {
			return errors.Wrap(err, "failed to marshal attempts")
		}
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[ensureNICsAttemptsAnnotation] = string(raw)
	}
	o.ownerRef.SetAnnotations(annotations)
	return o.client.Patch(ctx, o.ownerRef, client.MergeFrom(base))
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
	return o.setAttempts(ctx, nil)
}

// A WekaPolicy can be Failed from type resolution and nothing resets that after a spec fix, so only a
// manual operation treats Failed as final.
func (o *EnsureNICsOperation) IsTerminal() bool {
	if _, isManualOp := o.ownerRef.(*weka.WekaManualOperation); isManualOp && o.ownerStatus == "Failed" {
		return true
	}
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
