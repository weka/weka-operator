package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/weka/weka-operator/pkg/workers"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/util"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	util2 "github.com/weka/weka-operator/pkg/util"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const defaultPortRangeBase = 45000

func NewClientReconcileLoop(r *ClientController) *clientReconcilerLoop {
	mgr := r.Manager
	kClient := mgr.GetClient()
	return &clientReconcilerLoop{
		Client:        kClient,
		Scheme:        mgr.GetScheme(),
		Recorder:      mgr.GetEventRecorderFor("weka-operator"),
		KubeService:   kubernetes.NewKubeService(kClient),
		Manager:       mgr,
		ThrottlingMap: r.ThrottlingMap,
	}
}

type clientReconcilerLoop struct {
	client.Client
	Scheme        *runtime.Scheme
	KubeService   kubernetes.KubeService
	Manager       ctrl.Manager
	Recorder      record.EventRecorder
	containers    []*weka.WekaContainer
	wekaClient    *weka.WekaClient
	ThrottlingMap *util2.ThrottlingSyncMap
	nodes         []v1.Node
}

func ClientReconcileSteps(r *ClientController, wekaClient *weka.WekaClient) lifecycle.ReconciliationSteps {
	loop := NewClientReconcileLoop(r)
	loop.wekaClient = wekaClient

	return lifecycle.ReconciliationSteps{
		Throttler:    r.ThrottlingMap.WithPartition(string("client/" + loop.wekaClient.GetUID())),
		Client:       loop.Client,
		StatusObject: loop.wekaClient,
		Conditions:   &loop.wekaClient.Status.Conditions,
		Steps: []lifecycle.Step{
			{Run: loop.getCurrentContainers},
			{Run: loop.setApplicableNodes},
			{
				Run:       loop.updateMetrics,
				Throttled: time.Minute,
			},
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

	toDelete := []*weka.WekaContainer{}
	for _, container := range c.containers {
		if container.IsMarkedForDeletion() {
			continue
		}
		toDelete = append(toDelete, container)
	}

	// patch all client containers state to destroying
	results := workers.ProcessConcurrently(ctx, toDelete, 32, func(ctx context.Context, container *weka.WekaContainer) error {
		if !container.IsMarkedForDeletion() {
			patch := map[string]interface{}{
				"spec": map[string]interface{}{
					"state": weka.ContainerStateDestroying,
				},
			}

			patchBytes, err := json.Marshal(patch)
			if err != nil {
				return err
			}

			err = c.Patch(ctx, container, client.RawPatch(types.MergePatchType, patchBytes))
			if err != nil {
				return err
			}
		}
		return nil
	})
	if results.AsError() != nil {
		return results.AsError()
	}

	if len(c.containers) > 0 {
		return lifecycle.NewWaitErrorWithDuration(errors.New("waiting for client weka containers to be deleted"), time.Second*15)
	}
	return nil
}

func (c *clientReconcilerLoop) getCurrentContainers(ctx context.Context) error {
	currentContainers := discovery.GetClientContainers(ctx, c.Client, c.wekaClient)
	c.containers = currentContainers
	return nil
}

func (c *clientReconcilerLoop) EnsureClientsWekaContainers(ctx context.Context) error {
	if len(c.nodes) == 0 {
		// No nodes to deploy on
		return nil
	}

	nodeToContainer := make(map[string]string)
	for _, container := range c.containers {
		nodeName := string(container.Spec.NodeAffinity)
		nodeToContainer[nodeName] = container.Name
	}

	toCreate := []*weka.WekaContainer{}
	for _, node := range c.nodes {
		if _, ok := nodeToContainer[node.Name]; ok {
			continue
		} else {
			wekaContainer, err := c.buildClientWekaContainer(ctx, node.Name)
			if err != nil {
				return errors.Wrap(err, "failed to build client weka container")
			}
			toCreate = append(toCreate, wekaContainer)
		}
	}

	results := workers.ProcessConcurrently(ctx, toCreate, 32, func(ctx context.Context, wekaContainer *weka.WekaContainer) error {
		err := ctrl.SetControllerReference(c.wekaClient, wekaContainer, c.Scheme)
		if err != nil {
			return errors.Wrap(err, "failed to set controller reference")
		}

		found := &weka.WekaContainer{}
		err = c.Get(ctx, client.ObjectKey{Namespace: wekaContainer.Namespace, Name: wekaContainer.Name}, found)
		if err != nil && apierrors.IsNotFound(err) {
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
		return nil
	})
	return results.AsError()
}

func (c *clientReconcilerLoop) updateClientLabels(ctx context.Context, expected, found *weka.WekaContainer) error {
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

func (c *clientReconcilerLoop) buildClientWekaContainer(ctx context.Context, nodeName string) (*weka.WekaContainer, error) {
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
			portRange = &weka.PortRange{
				BasePort: defaultPortRangeBase,
			}
		}
	}

	containerLabels := factory.RequiredWekaClientLabels(wekaClient.ObjectMeta.Name)
	labels := util2.MergeMaps(wekaClient.ObjectMeta.GetLabels(), containerLabels)

	container := &weka.WekaContainer{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "weka.weka.io/v1alpha1",
			Kind:       "WekaContainer",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      clientName,
			Namespace: wekaClient.Namespace,
			Labels:    labels,
		},
		Spec: weka.WekaContainerSpec{
			NodeAffinity:        weka.NodeName(nodeName),
			Port:                wekaClient.Spec.Port,
			AgentPort:           wekaClient.Spec.AgentPort,
			PortRange:           portRange,
			Image:               wekaClient.Spec.Image,
			ImagePullSecret:     wekaClient.Spec.ImagePullSecret,
			WekaContainerName:   fmt.Sprintf("%sclient", util.GetLastGuidPart(wekaClient.GetUID())),
			Mode:                weka.WekaContainerModeClient,
			NumCores:            numCores,
			CpuPolicy:           wekaClient.Spec.CpuPolicy,
			CoreIds:             wekaClient.Spec.CoreIds,
			Network:             network,
			Hugepages:           getClientHugePages(numCores),
			HugepagesSize:       "2Mi",
			WekaSecretRef:       v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{Key: wekaClient.Spec.WekaSecretRef}},
			DriversDistService:  wekaClient.Spec.DriversDistService,
			JoinIps:             wekaClient.Spec.JoinIps,
			TracesConfiguration: wekaClient.Spec.TracesConfiguration,
			Tolerations:         tolerations,
			AdditionalMemory:    wekaClient.Spec.AdditionalMemory,
			AdditionalSecrets:   additionalSecrets,
			UpgradePolicyType:   wekaClient.Spec.UpgradePolicy.Type,
			AllowHotUpgrade:     wekaClient.Spec.AllowHotUpgrade,
			DriversLoaderImage:  wekaClient.Spec.DriversLoaderImage,
			Overrides: &weka.WekaContainerSpecOverrides{
				MachineIdentifierNodeRef: wekaClient.Spec.GetOverrides().MachineIdentifierNodeRef,
			},
		},
	}
	return container, nil
}

func getClientHugePages(cores int) int {
	return cores * 1500
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

	emptyTarget := weka.ObjectReference{}
	if c.wekaClient.Spec.TargetCluster == emptyTarget {
		return nil
	}

	joinIps, err := services.ClustersJoinIps.GetJoinIps(ctx, c.wekaClient.Spec.TargetCluster.Name, c.wekaClient.Spec.TargetCluster.Namespace)
	if err != nil {
		logger.Error(err, "Need to refresh join ips", "cluster", c.wekaClient.Spec.TargetCluster.Name)
		return err
	}

	c.wekaClient.Spec.JoinIps = joinIps
	// not commiting on purpose. If it will be - let it be. Just ad-hocy create for initial client create use. It wont be needed later
	// and new reconcilation loops will refresh it each time
	return nil
}

func (c *clientReconcilerLoop) HandleSpecUpdates(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	updatableSpec := NewUpdatableClientSpec(&c.wekaClient.Spec, &c.wekaClient.ObjectMeta)
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

		logger.Info("Updating last applied spec", "newSpecHash", specHash, "lastAppliedSpecHash", c.wekaClient.Status.LastAppliedSpec)
		c.wekaClient.Status.LastAppliedSpec = specHash
		err = c.Status().Update(ctx, c.wekaClient)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *clientReconcilerLoop) updateContainerIfChanged(ctx context.Context, container *weka.WekaContainer, newClientSpec *UpdatableClientSpec) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	patch := client.MergeFrom(container.DeepCopy())
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
		// TODO: validate that we are not updating on non-single IP interfaces, specifically not on EKS AWS DPDK
		if newClientSpec.CoresNumber < container.Spec.NumCores {
			logger.Error(errors.New("coresNum cannot be decreased"), "coresNum cannot be decreased, ignoring the change")
		} else {
			container.Spec.NumCores = newClientSpec.CoresNumber
			container.Spec.Hugepages = getClientHugePages(newClientSpec.CoresNumber)
			changed = true
		}
	}

	newTolerations := util.ExpandTolerations([]v1.Toleration{}, newClientSpec.Tolerations, newClientSpec.RawTolerations)
	oldTolerations := util.NormalizeTolerations(container.Spec.Tolerations)
	if !reflect.DeepEqual(oldTolerations, newTolerations) {
		container.Spec.Tolerations = newTolerations
		changed = true
	}

	// desired labels = existing labels + cluster labels + required labels
	// priority-wise, required labels have the highest priority
	requiredLables := factory.RequiredWekaClientLabels(c.wekaClient.ObjectMeta.Name)
	newLabels := util2.MergeMaps(container.Labels, c.wekaClient.ObjectMeta.GetLabels())
	newLabels = util2.MergeMaps(newLabels, requiredLables)
	if !util2.NewHashableMap(newLabels).Equals(util2.NewHashableMap(container.Labels)) {
		container.Labels = newLabels
		changed = true
	}

	if changed {
		err := c.Patch(ctx, container, patch)
		if err != nil {
			return errors.Wrap(err, "failed to patch weka container")
		}
		logger.Debug("Client container updated", "container", container.Name)
	}
	return nil
}

func (c *clientReconcilerLoop) getApplicableNodes(ctx context.Context) ([]v1.Node, error) {
	nodes, err := c.KubeService.GetNodes(ctx, c.wekaClient.Spec.NodeSelector)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get applicable nodes by labels")
	}

	clientTolerations := util.ExpandTolerations([]v1.Toleration{}, c.wekaClient.Spec.Tolerations, c.wekaClient.Spec.RawTolerations)

	var filteredNodes []v1.Node
	for _, node := range nodes {
		if node.Spec.Unschedulable {
			continue
		}
		if !util2.CheckTolerations(node.Spec.Taints, clientTolerations) {
			continue
		}
		filteredNodes = append(filteredNodes, node)
	}

	return filteredNodes, nil
}

func (c *clientReconcilerLoop) HandleUpgrade(ctx context.Context) error {
	uController := NewUpgradeController(c.Client, c.containers, c.wekaClient.Spec.Image)
	if uController.AreUpgraded() {
		return nil
	}

	switch c.wekaClient.Spec.UpgradePolicy.Type {
	case weka.UpgradePolicyTypeAllAtOnce:
		return uController.AllAtOnceUpgrade(ctx)
	case weka.UpgradePolicyTypeRolling:
		return uController.RollingUpgrade(ctx)
	default:
		// we are relying on container to treat self-upgrade as manual(i.e not replacing pod) by propagating mode into it
		return uController.AllAtOnceUpgrade(ctx)
	}
}

func (c *clientReconcilerLoop) updateMetrics(ctx context.Context) error {
	if c.wekaClient.Status.Stats == nil {
		c.wekaClient.Status.Stats = &weka.ClientMetrics{}
	}

	changed := false

	stats := c.wekaClient.Status.Stats
	if int64(stats.Containers.Desired) != int64(len(c.nodes)) {
		stats.Containers.Desired = weka.IntMetric(len(c.nodes))
		changed = true
	}

	if int64(stats.Containers.Created) != int64(len(c.containers)) {
		stats.Containers.Created = weka.IntMetric(len(c.containers))
		changed = true
	}

	totalActive := 0
	for _, container := range c.containers {
		if container.Status.Status == ContainerStatusRunning && container.Status.ClusterContainerID != nil {
			totalActive++
		}
	}
	if int64(stats.Containers.Active) != int64(totalActive) {
		stats.Containers.Active = weka.IntMetric(totalActive)
		changed = true
	}

	if changed {
		c.wekaClient.Status.PrinterColumns.Containers = weka.StringMetric(stats.Containers.String())
		err := c.Status().Update(ctx, c.wekaClient)
		if err != nil {
			return errors.Wrap(err, "failed to update wekaClient status")
		}
	}
	return nil
}

func (c *clientReconcilerLoop) setApplicableNodes(ctx context.Context) error {
	nodes, err := c.getApplicableNodes(ctx)
	if err != nil {
		return err
	}
	c.nodes = nodes
	return nil
}

func isPortRangeEqual(a, b weka.PortRange) bool {
	return a.BasePort == b.BasePort && a.PortRange == b.PortRange
}

type UpdatableClientSpec struct {
	DriversDistService string
	ImagePullSecret    string
	WekaSecretRef      string
	AdditionalMemory   int
	UpgradePolicy      weka.UpgradePolicy
	AllowHotUpgrade    bool
	DriversLoaderImage string
	Port               int
	AgentPort          int
	PortRange          *weka.PortRange
	CoresNumber        int
	Tolerations        []string
	RawTolerations     []v1.Toleration
	Labels             *util2.HashableMap
}

func NewUpdatableClientSpec(spec *weka.WekaClientSpec, meta *metav1.ObjectMeta) *UpdatableClientSpec {
	labels := util2.NewHashableMap(meta.Labels)

	return &UpdatableClientSpec{
		DriversDistService: spec.DriversDistService,
		ImagePullSecret:    spec.ImagePullSecret,
		WekaSecretRef:      spec.WekaSecretRef,
		AdditionalMemory:   spec.AdditionalMemory,
		UpgradePolicy:      spec.UpgradePolicy,
		AllowHotUpgrade:    spec.AllowHotUpgrade,
		DriversLoaderImage: spec.DriversLoaderImage,
		Port:               spec.Port,
		AgentPort:          spec.AgentPort,
		PortRange:          spec.PortRange,
		CoresNumber:        spec.CoresNumber,
		Tolerations:        spec.Tolerations,
		RawTolerations:     spec.RawTolerations,
		Labels:             labels,
	}
}
