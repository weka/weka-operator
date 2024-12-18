package controllers

import (
	"context"
	"fmt"
	"reflect"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/util"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	util2 "github.com/weka/weka-operator/pkg/util"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const defaultPortRangeBase = 45000

func NewClientReconcileLoop(mgr ctrl.Manager) *clientReconcilerLoop {
	kClient := mgr.GetClient()
	return &clientReconcilerLoop{
		Client:      kClient,
		Scheme:      mgr.GetScheme(),
		Recorder:    mgr.GetEventRecorderFor("weka-operator"),
		KubeService: kubernetes.NewKubeService(kClient),
		Manager:     mgr,
	}
}

type clientReconcilerLoop struct {
	client.Client
	Scheme      *runtime.Scheme
	KubeService kubernetes.KubeService
	Manager     ctrl.Manager
	Recorder    record.EventRecorder
	containers  []*v1alpha1.WekaContainer
	wekaClient  *v1alpha1.WekaClient
}

func ClientReconcileSteps(mgr ctrl.Manager, wekaClient *v1alpha1.WekaClient) lifecycle.ReconciliationSteps {
	loop := NewClientReconcileLoop(mgr)
	loop.wekaClient = wekaClient

	return lifecycle.ReconciliationSteps{
		Client:       loop.Client,
		StatusObject: loop.wekaClient,
		Conditions:   &loop.wekaClient.Status.Conditions,
		Steps: []lifecycle.Step{
			{Run: loop.getCurrentContainers},
			{
				Run: loop.HandleDeletion,
				Predicates: lifecycle.Predicates{
					wekaClient.IsMarkedForDeletion,
				},
				ContinueOnPredicatesFalse: true,
				FinishOnSuccess:           true,
			},
			{Run: loop.ensureFinalizer},
			{Run: loop.EnsureClientsWekaContainers},
			{Run: loop.HandleSpecUpdates},
			{Run: loop.HandleUpgrade},
		},
	}
}

func (c *clientReconcilerLoop) HandleDeletion(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	if !controllerutil.ContainsFinalizer(c.wekaClient, WekaFinalizer) {
		return nil
	}

	if err := c.finalizeClient(ctx); err != nil {
		return err
	}

	controllerutil.RemoveFinalizer(c.wekaClient, WekaFinalizer)
	if err := c.Update(ctx, c.wekaClient); err != nil {
		logger.Error(err, "Error removing finalizer")
		return errors.Wrap(err, "failed to update wekaClient")
	}

	logger.Info("Deleting wekaClient")
	return nil
}

func (c *clientReconcilerLoop) RecordEvent(eventtype *string, reason string, message string) error {
	if c.wekaClient == nil {
		return fmt.Errorf("current client is nil")
	}
	if eventtype == nil {
		normal := v1.EventTypeNormal
		eventtype = &normal
	}

	c.Recorder.Event(c.wekaClient, *eventtype, reason, message)
	return nil
}

func (c *clientReconcilerLoop) ensureFinalizer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	if c.wekaClient.GetFinalizers() != nil {
		return nil
	}

	logger.Info("Adding Finalizer for weka client")
	if ok := controllerutil.AddFinalizer(c.wekaClient, WekaFinalizer); !ok {
		return nil
	}

	err := c.Update(ctx, c.wekaClient)
	if err != nil {
		return errors.Wrap(err, "failed to update wekaClient with finalizer")
	}
	return nil
}

func (c *clientReconcilerLoop) finalizeClient(ctx context.Context) error {
	// make sure to delete all weka containers
	for _, container := range c.containers {
		if container.IsMarkedForDeletion() {
			continue
		}
		err := c.Delete(ctx, container)
		if err != nil {
			return errors.Wrap(err, "failed to delete weka container")
		}
	}

	if len(c.containers) > 0 {
		return lifecycle.NewWaitError(errors.New("waiting for client weka containers to be deleted"))
	}
	return nil
}

func (c *clientReconcilerLoop) getCurrentContainers(ctx context.Context) error {
	currentContainers := discovery.GetClientContainers(ctx, c.Client, c.wekaClient)
	c.containers = currentContainers
	return nil
}

func (c *clientReconcilerLoop) EnsureClientsWekaContainers(ctx context.Context) error {
	nodes, err := c.getApplicableNodes(ctx)
	if err != nil {
		return err
	}

	if len(nodes) == 0 {
		// No nodes to deploy on
		return nil
	}

	nodeToContainer := make(map[string]string)
	for _, container := range c.containers {
		nodeName := string(container.Spec.NodeAffinity)
		nodeToContainer[nodeName] = container.Name
	}

	for _, node := range nodes {
		if _, ok := nodeToContainer[node.Name]; ok {
			continue
		}

		wekaContainer, err := c.buildClientWekaContainer(ctx, node.Name)
		if err != nil {
			return errors.Wrap(err, "failed to build client weka container")
		}
		err = ctrl.SetControllerReference(c.wekaClient, wekaContainer, c.Scheme)
		if err != nil {
			return errors.Wrap(err, "failed to set controller reference")
		}

		found := &v1alpha1.WekaContainer{}
		err = c.Get(ctx, client.ObjectKey{Namespace: wekaContainer.Namespace, Name: wekaContainer.Name}, found)
		if err != nil && apierrors.IsNotFound(err) {
			// TODO: Wasteful approach right now, each client fetches separately
			// We should have some small time-based cache here
			// try using informer (?)
			err := c.resolveJoinIps(ctx)
			if err != nil {
				return errors.Wrap(err, "failed to resolve join ips")
			}
			// Always re-applying, either we had JoinIps set by user, or we have resolving re-populating them
			wekaContainer.Spec.JoinIps = c.wekaClient.Spec.JoinIps

			err = c.Create(ctx, wekaContainer)
			if err != nil {
				return errors.Wrap(err, "failed to create weka container")
			}
		} else if err == nil {
			// container already exists, but we did not have it in our nodeToContainer map
			// try to update labels
			err := c.updateClientLabels(ctx, wekaContainer, found)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *clientReconcilerLoop) updateClientLabels(ctx context.Context, expected, found *v1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	missingLabels := util2.MapMissingItems(found.Labels, expected.Labels)
	// if there are missing labels, we need to update the client
	if len(missingLabels) > 0 {
		logger.Info("Updating client missing labels", "client", found.Name)
		for k, v := range missingLabels {
			found.Labels[k] = v
		}
		err := c.Update(ctx, found)
		if err != nil {
			err = fmt.Errorf("failed to update client %s labels: %w", found.Name, err)
			return err
		}
	}
	return nil
}

func (c *clientReconcilerLoop) buildClientWekaContainer(ctx context.Context, nodeName string) (*v1alpha1.WekaContainer, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "buildClientWekaContainer", "node", nodeName)
	defer end()

	wekaClient := c.wekaClient

	network, err := resources.GetContainerNetwork(wekaClient.Spec.NetworkSelector)
	if err != nil {
		return nil, err
	}

	numCores := wekaClient.Spec.CoresNumber
	if numCores == 0 {
		numCores = 1
	}

	additionalSecrets := map[string]string{}

	whCaCert := ""
	if wekaClient.Spec.WekaHome != nil {
		whCaCert = wekaClient.Spec.WekaHome.CacertSecret
		if whCaCert == "" {
			whCaCert = config.Config.WekaHome.CacertSecret
		}
	}

	if whCaCert != "" {
		additionalSecrets["wekahome-cacert"] = whCaCert
	}

	tolerations := util.ExpandTolerations([]v1.Toleration{}, wekaClient.Spec.Tolerations, wekaClient.Spec.RawTolerations)
	clientName, err := c.getClientContainerName(ctx, nodeName)
	if err != nil {
		logger.Error(err, "Failed to create client container name, too long", "clientName", clientName)
		return nil, err
	}

	portRange := wekaClient.Spec.PortRange

	// make sure that PortRange is set if one of the ports is 0
	if wekaClient.Spec.AgentPort == 0 || wekaClient.Spec.Port == 0 {
		if portRange == nil {
			portRange = &v1alpha1.PortRange{
				BasePort: defaultPortRangeBase,
			}
		}
	}

	containerLabels := map[string]string{
		"app":                 "weka-client",
		"weka.io/client-name": wekaClient.ObjectMeta.Name,
		"weka.io/mode":        v1alpha1.WekaContainerModeClient,
	}
	labels := util2.MergeMaps(wekaClient.ObjectMeta.GetLabels(), containerLabels)

	container := &v1alpha1.WekaContainer{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "weka.weka.io/v1alpha1",
			Kind:       "WekaContainer",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      clientName,
			Namespace: wekaClient.Namespace,
			Labels:    labels,
		},
		Spec: v1alpha1.WekaContainerSpec{
			NodeAffinity:        v1alpha1.NodeName(nodeName),
			Port:                wekaClient.Spec.Port,
			AgentPort:           wekaClient.Spec.AgentPort,
			PortRange:           portRange,
			Image:               wekaClient.Spec.Image,
			ImagePullSecret:     wekaClient.Spec.ImagePullSecret,
			WekaContainerName:   fmt.Sprintf("%sclient", util.GetLastGuidPart(wekaClient.GetUID())),
			Mode:                v1alpha1.WekaContainerModeClient,
			NumCores:            numCores,
			CpuPolicy:           wekaClient.Spec.CpuPolicy,
			CoreIds:             wekaClient.Spec.CoreIds,
			Network:             network,
			Hugepages:           1500 * numCores,
			HugepagesSize:       "2Mi",
			WekaSecretRef:       v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{Key: wekaClient.Spec.WekaSecretRef}},
			DriversDistService:  wekaClient.Spec.DriversDistService,
			JoinIps:             wekaClient.Spec.JoinIps,
			TracesConfiguration: wekaClient.Spec.TracesConfiguration,
			Tolerations:         tolerations,
			AdditionalMemory:    wekaClient.Spec.AdditionalMemory,
			AdditionalSecrets:   additionalSecrets,
			UpgradePolicyType:   wekaClient.Spec.UpgradePolicy.Type,
			DriversLoaderImage:  wekaClient.Spec.DriversLoaderImage,
		},
	}
	return container, nil
}

func (c *clientReconcilerLoop) getClientContainerName(ctx context.Context, nodeName string) (string, error) {
	clientName := fmt.Sprintf("%s-%s", c.wekaClient.ObjectMeta.Name, nodeName)
	if len(clientName) <= 63 {
		return clientName, nil
	}

	nodeObj := &v1.Node{}
	err := c.Get(ctx, client.ObjectKey{Name: nodeName}, nodeObj)
	if err != nil {
		return "", errors.Wrap(err, "failed to get node")
	}
	if nodeObj == nil {
		return "", errors.New("node not found")
	}
	clientName = fmt.Sprintf("%s-%s", c.wekaClient.ObjectMeta.Name, nodeObj.UID)
	if len(clientName) > 63 {
		name := c.wekaClient.ObjectMeta.Name[:62-len(nodeObj.UID)]
		clientName = fmt.Sprintf("%s-%s", name, nodeObj.UID)
	}
	return clientName, nil
}

func (c *clientReconcilerLoop) resolveJoinIps(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	emptyTarget := v1alpha1.ObjectReference{}
	if c.wekaClient.Spec.TargetCluster == emptyTarget {
		return nil
	}

	cluster, err := discovery.GetCluster(ctx, c.Client, c.wekaClient.Spec.TargetCluster)
	if err != nil {
		return err
	}

	joinIps, err := discovery.GetJoinIps(ctx, c.Client, cluster)
	if err != nil {
		return err
	}
	logger.Info("Resolved join ips", "joinIps", joinIps)

	c.wekaClient.Spec.JoinIps = joinIps
	// not commiting on purpose. If it will be - let it be. Just ad-hocy create for initial client create use. It wont be needed later
	// and new reconcilation loops will refresh it each time
	return nil
}

func (c *clientReconcilerLoop) HandleSpecUpdates(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	updatableSpec := NewUpdatableClientSpec(&c.wekaClient.Spec)
	specHash, err := util2.HashStruct(updatableSpec)
	if err != nil {
		return err
	}

	if specHash != c.wekaClient.Status.LastAppliedSpec {
		logger.Info("Client spec has changed, updating containers")
		for _, container := range c.containers {
			err := c.updateContainerIfChanged(ctx, container, updatableSpec)
			if err != nil {
				return err
			}
		}

		logger.Info("Updating last applied spec", "currentSpecHash", specHash, "lastAppliedSpecHash", c.wekaClient.Status.LastAppliedSpec)
		c.wekaClient.Status.LastAppliedSpec = specHash
		err = c.Status().Update(ctx, c.wekaClient)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *clientReconcilerLoop) updateContainerIfChanged(ctx context.Context, container *v1alpha1.WekaContainer, newClientSpec *UpdatableClientSpec) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	changed := false

	if container.Spec.DriversDistService != newClientSpec.DriversDistService {
		container.Spec.DriversDistService = newClientSpec.DriversDistService
		changed = true
	}

	if container.Spec.ImagePullSecret != newClientSpec.ImagePullSecret {
		container.Spec.ImagePullSecret = newClientSpec.ImagePullSecret
		changed = true
	}

	if container.Spec.WekaSecretRef.SecretKeyRef.Key != newClientSpec.WekaSecretRef {
		container.Spec.WekaSecretRef = v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{Key: newClientSpec.WekaSecretRef}}
		changed = true
	}

	if container.Spec.AdditionalMemory != newClientSpec.AdditionalMemory {
		container.Spec.AdditionalMemory = newClientSpec.AdditionalMemory
		changed = true
	}

	if container.Spec.UpgradePolicyType != newClientSpec.UpgradePolicy.Type {
		container.Spec.UpgradePolicyType = newClientSpec.UpgradePolicy.Type
		changed = true
	}

	if container.Spec.DriversLoaderImage != newClientSpec.DriversLoaderImage {
		container.Spec.DriversLoaderImage = newClientSpec.DriversLoaderImage
		changed = true
	}

	if container.Spec.Port != newClientSpec.Port {
		container.Spec.Port = newClientSpec.Port
		changed = true
	}

	if container.Spec.AgentPort != newClientSpec.AgentPort {
		container.Spec.AgentPort = newClientSpec.AgentPort
		changed = true
	}

	if container.Spec.PortRange == nil && newClientSpec.PortRange != nil {
		container.Spec.PortRange = newClientSpec.PortRange
		changed = true
	}

	if container.Spec.PortRange != nil && newClientSpec.PortRange == nil {
		container.Spec.PortRange = nil
		changed = true
	}

	if container.Spec.PortRange != nil && newClientSpec.PortRange != nil && !isPortRangeEqual(*container.Spec.PortRange, *newClientSpec.PortRange) {
		container.Spec.PortRange.BasePort = newClientSpec.PortRange.BasePort
		container.Spec.PortRange.PortRange = newClientSpec.PortRange.PortRange
		changed = true
	}

	if container.Spec.NumCores != newClientSpec.CoresNumber {
		if newClientSpec.CoresNumber < container.Spec.NumCores {
			logger.Error(errors.New("coresNum cannot be decreased"), "coresNum cannot be decreased, ignoring the change")
		} else {
			container.Spec.NumCores = newClientSpec.CoresNumber
			changed = true
		}
	}

	tolerations := util.ExpandTolerations([]v1.Toleration{}, newClientSpec.Tolerations, newClientSpec.RawTolerations)
	if !reflect.DeepEqual(container.Spec.Tolerations, tolerations) {
		container.Spec.Tolerations = tolerations
		changed = true
	}

	if changed {
		err := c.Update(ctx, container)
		if err != nil {
			return errors.Wrap(err, "failed to update weka container")
		}
		logger.Info("Updated weka container", "container", container.Name)
	}
	return nil
}

func (c *clientReconcilerLoop) getApplicableNodes(ctx context.Context) ([]v1.Node, error) {
	nodes, err := c.KubeService.GetNodes(ctx, c.wekaClient.Spec.NodeSelector)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get applicable nodes by labels")
	}
	return nodes, nil
}

func (c *clientReconcilerLoop) HandleUpgrade(ctx context.Context) error {
	uController := NewUpgradeController(c.Client, c.containers, c.wekaClient.Spec.Image)
	if uController.AreUpgraded() {
		return nil
	}

	switch c.wekaClient.Spec.UpgradePolicy.Type {
	case v1alpha1.UpgradePolicyTypeAllAtOnce:
		return uController.AllAtOnceUpgrade(ctx)
	case v1alpha1.UpgradePolicyTypeRolling:
		return uController.RollingUpgrade(ctx)
	default:
		// we are relying on container to treat self-upgrade as manual(i.e not replacing pod) by propagating mode into it
		return uController.AllAtOnceUpgrade(ctx)
	}
}

func isPortRangeEqual(a, b v1alpha1.PortRange) bool {
	return a.BasePort == b.BasePort && a.PortRange == b.PortRange
}

type UpdatableClientSpec struct {
	DriversDistService string
	ImagePullSecret    string
	WekaSecretRef      string
	AdditionalMemory   int
	UpgradePolicy      v1alpha1.UpgradePolicy
	DriversLoaderImage string
	Port               int
	AgentPort          int
	PortRange          *v1alpha1.PortRange
	CoresNumber        int
	Tolerations        []string
	RawTolerations     []v1.Toleration
}

func NewUpdatableClientSpec(spec *v1alpha1.WekaClientSpec) *UpdatableClientSpec {
	return &UpdatableClientSpec{
		DriversDistService: spec.DriversDistService,
		ImagePullSecret:    spec.ImagePullSecret,
		WekaSecretRef:      spec.WekaSecretRef,
		AdditionalMemory:   spec.AdditionalMemory,
		UpgradePolicy:      spec.UpgradePolicy,
		DriversLoaderImage: spec.DriversLoaderImage,
		Port:               spec.Port,
		AgentPort:          spec.AgentPort,
		PortRange:          spec.PortRange,
		CoresNumber:        spec.CoresNumber,
		Tolerations:        spec.Tolerations,
		RawTolerations:     spec.RawTolerations,
	}
}
