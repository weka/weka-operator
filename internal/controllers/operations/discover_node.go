package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	k8sutil "github.com/weka/weka-k8s-api/util"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	util2 "github.com/weka/weka-operator/pkg/util"
)

type DiscoverNodeOperation struct {
	client         client.Client
	kubeService    kubernetes.KubeService
	execService    exec.ExecService
	scheme         *runtime.Scheme
	nodeName       weka.NodeName
	image          string
	pullSecret     string
	serviceAccount string
	result         *discovery.DiscoveryNodeInfo
	container      *weka.WekaContainer
	ownerRef       client.Object
	mgr            ctrl.Manager
	tolerations    []corev1.Toleration
	node           *corev1.Node
}

func NewDiscoverNodeOperation(mgr ctrl.Manager, restClient rest.Interface, node weka.NodeName, ownerRef client.Object, ownerDetails *weka.WekaOwnerDetails) *DiscoverNodeOperation {
	kclient := mgr.GetClient()
	return &DiscoverNodeOperation{
		mgr:            mgr,
		client:         kclient,
		kubeService:    kubernetes.NewKubeService(kclient),
		execService:    exec.NewExecService(restClient, mgr.GetConfig()),
		scheme:         mgr.GetScheme(),
		nodeName:       node,
		image:          ownerDetails.Image,
		pullSecret:     ownerDetails.ImagePullSecret,
		serviceAccount: ownerDetails.ServiceAccountName,
		tolerations:    ownerDetails.Tolerations,
		result:         nil,
		ownerRef:       ownerRef,
	}
}

func (o *DiscoverNodeOperation) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "DiscoverNode",
		Run:  AsRunFunc(o),
	}
}

func (o *DiscoverNodeOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{Name: "GetNode", Run: o.GetNode},
		&lifecycle.SimpleStep{Name: "GetContainer", Run: o.GetContainer},
		&lifecycle.SimpleStep{
			Name:            "FinishOnExistingInfo",
			Run:             o.DeleteContainers,
			FinishOnSuccess: true,
			Predicates: lifecycle.Predicates{
				o.HasData,
			},
		},
		&lifecycle.SimpleStep{Name: "EnsureContainers", Run: o.EnsureContainers},
		&lifecycle.SimpleStep{Name: "PollResults", Run: o.PollResults},
		&lifecycle.SimpleStep{Name: "ProcessResult", Run: o.ProcessResult},
		&lifecycle.SimpleStep{Name: "UpdateNodes", Run: o.UpdateNodes},
		&lifecycle.SimpleStep{Name: "DeleteOnFinish", Run: o.DeleteContainers},
	}
}

func (o *DiscoverNodeOperation) GetNode(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	node, err := o.kubeService.GetNode(ctx, types.NodeName(o.nodeName))
	if err != nil {
		return err
	}
	if node == nil {
		return fmt.Errorf("no matching nodes found for the given node selector")
	}
	o.node = node

	// Check if node already has the discovery.json annotation
	if annotation, ok := node.Annotations[discovery.DiscoveryAnnotation]; ok {
		discoveryNodeInfo := &discovery.DiscoveryNodeInfo{}
		err = json.Unmarshal([]byte(annotation), discoveryNodeInfo)
		if err == nil && discoveryNodeInfo.BootID == node.Status.NodeInfo.BootID && discoveryNodeInfo.Schema >= discovery.DiscoveryTargetSchema {
			o.result = discoveryNodeInfo
			enrichErr := o.Enrich(ctx)
			if enrichErr != nil {
				return enrichErr
			}
			return nil
		}
		if err != nil {
			logger.Error(err, "Failed to unmarshal discovery.json data")
		} else {
			logger.Debug("Discovery cache miss — need to re-run discovery",
				"cachedBootId", discoveryNodeInfo.BootID,
				"currentBootId", node.Status.NodeInfo.BootID,
				"cachedSchema", discoveryNodeInfo.Schema,
				"targetSchema", discovery.DiscoveryTargetSchema)
		}
	}

	return nil
}

func (o *DiscoverNodeOperation) GetProvider() discovery.Provider {
	return discovery.ProviderFromID(o.node.Spec.ProviderID)
}

func (o *DiscoverNodeOperation) Enrich(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	o.result.NumCpus = int(o.node.Status.Allocatable.Cpu().Value())
	o.result.Provider = o.GetProvider()
	o.result.Arch = o.node.Status.NodeInfo.Architecture

	fp, err := discovery.DetectKubeletFullPcpusOnly(ctx, o.mgr.GetConfig(), o.node.Name)
	if err != nil {
		logger.Error(err, "failed to detect kubelet full-pcpus-only; defaulting to false", "node", o.node.Name)
		fp = false
	}
	o.result.NodeFullPcpusOnly = fp

	if o.result.IsRhCos() {
		if o.result.OsBuildId == "" {
			return errors.New("Failed to get OCP version from node")
		}
		image, err := discovery.GetOcpToolkitImage(ctx, o.client, o.result.OsBuildId)
		if err != nil || image == "" {
			return errors.Wrap(err, fmt.Sprintf("Failed to get OCP toolkit image for version %s", o.result.OsBuildId))
		}
		o.result.InitContainerImage = image
	}
	return nil
}

func (o *DiscoverNodeOperation) GetContainer(ctx context.Context) error {
	containerName := o.getContainerName()
	container, err := discovery.GetContainerByName(ctx, o.client, weka.ObjectReference{
		Namespace: o.ownerRef.GetNamespace(),
		Name:      containerName,
	})
	if err != nil && apierrors.IsNotFound(err) {
		return nil
	}
	if container != nil {
		o.container = container
	}
	return nil
}

// isContainerSpecChanged reports whether the existing discovery container no longer matches the owner-derived spec
func (o *DiscoverNodeOperation) isContainerSpecChanged() bool {
	spec := o.container.Spec
	return spec.Image != o.image ||
		spec.ImagePullSecret != o.pullSecret ||
		spec.ServiceAccountName != o.serviceAccount ||
		!reflect.DeepEqual(k8sutil.NormalizeTolerations(spec.Tolerations), k8sutil.NormalizeTolerations(o.tolerations))
}

func (o *DiscoverNodeOperation) EnsureContainers(ctx context.Context) error {

	if o.container != nil {
		if o.container.GetDeletionTimestamp() != nil {
			return lifecycle.NewWaitError(fmt.Errorf("discovery container %s is being deleted", o.container.Name))
		}
		// the discovery container is a shared per-node singleton; only its controller-owner
		// enforces spec drift, otherwise owners with different specs delete each other's container in a loop
		if !metav1.IsControlledBy(o.container, o.ownerRef) {
			return nil
		}
		if !o.isContainerSpecChanged() {
			return nil
		}
		// recreate on a later pass — a same-name Create would fail while the old container is terminating
		if err := o.DeleteContainers(ctx); err != nil {
			return err
		}
		return lifecycle.NewWaitError(fmt.Errorf("discovery container spec changed, recreating"))
	}

	// If we already have valid discovery information, skip container creation
	if o.result != nil {
		return nil
	}

	labels := map[string]string{
		"weka.io/mode": weka.WekaContainerModeDiscovery,
	}
	labels = util2.MergeMaps(o.ownerRef.GetLabels(), labels)

	containerName := o.getContainerName()
	discoveryContainer := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      containerName,
			Namespace: o.ownerRef.GetNamespace(), // this means, that discovery can be created multiple times in different namespaces. but, this way it has proper owner. need to ensure that discovery can sustain running in parallel
			Labels:    labels,
		},
		Spec: weka.WekaContainerSpec{
			Mode:               weka.WekaContainerModeDiscovery,
			NodeAffinity:       weka.NodeName(o.node.Name),
			Image:              o.image,
			ImagePullSecret:    o.pullSecret,
			Tolerations:        o.tolerations,
			ServiceAccountName: o.serviceAccount,
		},
	}

	err := ctrl.SetControllerReference(o.ownerRef, discoveryContainer, o.scheme)
	if err != nil {
		return err
	}

	err = o.client.Create(ctx, discoveryContainer)
	if err != nil {
		return err
	}

	o.container = discoveryContainer

	return nil
}

func (o *DiscoverNodeOperation) PollResults(ctx context.Context) error {
	if o.container.Status.ExecutionResult == nil {
		return lifecycle.NewWaitError(fmt.Errorf("container execution result is not ready"))
	}

	return nil
}

func (o *DiscoverNodeOperation) ProcessResult(ctx context.Context) error {
	discoveryNodeInfo := &discovery.DiscoveryNodeInfo{}
	err := json.Unmarshal([]byte(*o.container.Status.ExecutionResult), discoveryNodeInfo)
	if err != nil {
		return errors.Wrap(err, "Failed to unmarshal discovery.json")
	}

	discoveryNodeInfo.Schema = discovery.DiscoveryTargetSchema
	o.result = discoveryNodeInfo
	err = o.Enrich(ctx)
	if err != nil {
		return err
	}

	return nil
}

func (o *DiscoverNodeOperation) UpdateNodes(ctx context.Context) error {
	if o.result == nil {
		return errors.New("No discovery information available")
	}

	o.result.BootID = o.node.Status.NodeInfo.BootID
	//TODO: Remove once moved to k8s scheduler, this allow to implement per-node in-operator scheduler

	discoveryString, err := json.Marshal(o.result)
	if err != nil {
		return errors.Wrap(err, "Failed to marshal discovery.json")
	}

	if o.node.Annotations == nil {
		o.node.Annotations = make(map[string]string)
	}
	o.node.Annotations[discovery.DiscoveryAnnotation] = string(discoveryString)
	err = o.client.Update(ctx, o.node)
	if err != nil {
		return err
	}

	return nil
}

func (o *DiscoverNodeOperation) DeleteContainers(ctx context.Context) error {
	if o.container == nil {
		return nil
	}
	err := o.client.Delete(ctx, o.container)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	o.container = nil
	return nil
}

func (o *DiscoverNodeOperation) GetResult() *discovery.DiscoveryNodeInfo {
	return o.result
}

func (o *DiscoverNodeOperation) GetJsonResult() string {
	if o.result == nil {
		return "{}"
	}
	resultJSON, _ := json.Marshal(o.result) //nolint:errcheck // error return value intentionally not checked
	return string(resultJSON)
}

func (o *DiscoverNodeOperation) Cleanup() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "DeleteContainers",
		Run:  o.DeleteContainers,
	}
}

func (o *DiscoverNodeOperation) HasData() bool {
	return o.result != nil
}

func (o *DiscoverNodeOperation) getContainerName() string {
	return fmt.Sprintf("weka-dsc-%s", o.node.Name)
}
