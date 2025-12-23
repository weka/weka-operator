package wekaclient

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-lib/pkg/workers"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"github.com/weka/weka-k8s-api/util"
	appsv1 "k8s.io/api/apps/v1"
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
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/operations/csi"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/controllers/upgrade"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	util2 "github.com/weka/weka-operator/pkg/util"
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
		ExecService:   r.ExecService,
	}
}

type clientReconcilerLoop struct {
	client.Client
	Scheme         *runtime.Scheme
	KubeService    kubernetes.KubeService
	Manager        ctrl.Manager
	Recorder       record.EventRecorder
	containers     []*weka.WekaContainer
	wekaClient     *weka.WekaClient
	ThrottlingMap  throttling.Throttler
	nodes          []v1.Node
	toleratedNodes map[string]struct{}
	// keep in state for future steps referencing
	upgradeInProgress bool
	ExecService       exec.ExecService
	targetCluster     *weka.WekaCluster
	// true if there is a different existing client managing the CSI deployment
	csiDeploymentOwnedByDifferentClient bool
}

func ClientReconcileSteps(r *ClientController, wekaClient *weka.WekaClient) lifecycle.StepsEngine {
	loop := NewClientReconcileLoop(r)
	loop.wekaClient = wekaClient

	k8sObject := &lifecycle.K8sObject{
		Client:     loop.Client,
		Object:     loop.wekaClient,
		Conditions: &loop.wekaClient.Status.Conditions,
	}

	return lifecycle.StepsEngine{
		StateKeeper: k8sObject,
		Throttler:   r.ThrottlingMap.WithPartition(string("client/" + loop.wekaClient.GetUID())),
		Steps: []lifecycle.Step{
			&lifecycle.SimpleStep{Run: loop.getCurrentContainers},
			&lifecycle.SimpleStep{Run: loop.setApplicableNodes},
			&lifecycle.SimpleStep{Run: loop.setToleratedNodes},
			&lifecycle.SimpleStep{
				Run: loop.updateMetrics,
				Throttling: &throttling.ThrottlingSettings{
					Interval: time.Minute,
				},
			},
			&lifecycle.SimpleStep{
				Run: loop.FetchTargetCluster,
				Predicates: lifecycle.Predicates{
					func() bool {
						emptyRef := weka.ObjectReference{}
						return wekaClient.Spec.TargetCluster != emptyRef && wekaClient.Spec.TargetCluster.Name != ""
					},
					lifecycle.BoolValue(config.Config.Csi.Enabled),
				},
			},
			&lifecycle.SimpleStep{
				Run: loop.CheckExistingCsiControllerOwner,
				Predicates: lifecycle.Predicates{
					lifecycle.BoolValue(config.Config.Csi.Enabled),
				},
			},
			&lifecycle.SimpleStep{
				Run: loop.HandleDeletion,
				Predicates: lifecycle.Predicates{
					wekaClient.IsMarkedForDeletion,
				},
				FinishOnSuccess: true,
			},
			&lifecycle.SimpleStep{
				Run: loop.ensureFinalizer,
				Predicates: lifecycle.Predicates{
					func() bool { return wekaClient.GetFinalizers() == nil },
				},
			},
			&lifecycle.SimpleStep{
				Run: loop.deleteContainersOnNodeSelectorMismatch,
				Predicates: lifecycle.Predicates{
					lifecycle.BoolValue(config.Config.CleanupClientsOnNodeSelectorMismatch),
				},
			},
			&lifecycle.SimpleStep{
				Run: loop.deleteContainersOnTolerationsMismatch,
				Predicates: lifecycle.Predicates{
					lifecycle.BoolValue(config.Config.CleanupContainersOnTolerationsMismatch),
				},
			},
			&lifecycle.SimpleStep{
				Run: loop.FetchTargetCluster,
				Predicates: lifecycle.Predicates{
					func() bool {
						emptyRef := weka.ObjectReference{}
						return wekaClient.Spec.TargetCluster != emptyRef && wekaClient.Spec.TargetCluster.Name != "" && loop.targetCluster == nil
					},
				},
			},
			&lifecycle.SimpleStep{Run: loop.EnsureClientsWekaContainers},
			&lifecycle.SimpleStep{
				State:              &lifecycle.State{Name: condition.CondCsiDeployed},
				SkipStepStateCheck: true,
				Run:                loop.DeployCsiPlugin,
				Predicates: lifecycle.Predicates{
					loop.clientManagesCsiDeployment,
				},
				ContinueOnError: true,
			},
			&lifecycle.SimpleStep{
				Run: loop.CheckCsiConfigChanged,
				Predicates: lifecycle.Predicates{
					lifecycle.IsTrueCondition(condition.CondCsiDeployed, &wekaClient.Status.Conditions),
				},
			},
			&lifecycle.SimpleStep{
				Run: loop.UpdateCsiController,
				Predicates: lifecycle.Predicates{
					lifecycle.IsTrueCondition(condition.CondCsiDeployed, &wekaClient.Status.Conditions),
					loop.clientManagesCsiDeployment,
					func() bool {
						return wekaClient.Spec.CsiConfig == nil || !wekaClient.Spec.CsiConfig.DisableControllerCreation
					},
				},
				ContinueOnError: true,
			},
			&lifecycle.SimpleStep{
				Run: loop.UpdateCsiNodeDaemonSet,
				Predicates: lifecycle.Predicates{
					lifecycle.IsTrueCondition(condition.CondCsiDeployed, &wekaClient.Status.Conditions),
					loop.clientManagesCsiDeployment,
				},
				ContinueOnError: true,
			},
			&lifecycle.SimpleStep{Run: loop.HandleSpecUpdates},
			&lifecycle.SimpleStep{Run: loop.HandleUpgrade},
			&lifecycle.SimpleStep{
				Run: loop.setStatusRunning,
				Predicates: lifecycle.Predicates{
					// upgrade step migth not fail (with ExpectedError) so check if upgrade is in progress
					func() bool {
						return !loop.upgradeInProgress && wekaClient.Status.Status != weka.WekaClientStatusRunning
					},
				},
			},
		},
	}
}

func (c *clientReconcilerLoop) clientManagesCsiDeployment() bool {
	return config.Config.Csi.Enabled && !c.csiDeploymentOwnedByDifferentClient
}

func (c *clientReconcilerLoop) HandleDeletion(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	if !controllerutil.ContainsFinalizer(c.wekaClient, consts.WekaFinalizer) {
		return nil
	}

	if err := c.updateStatusIfNotEquals(ctx, weka.WekaClientStatusDestroying); err != nil {
		return err
	}

	if err := c.finalizeClient(ctx); err != nil {
		return err
	}

	controllerutil.RemoveFinalizer(c.wekaClient, consts.WekaFinalizer)
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

	logger.Info("Adding Finalizer for weka client")
	if ok := controllerutil.AddFinalizer(c.wekaClient, consts.WekaFinalizer); !ok {
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

	err := workers.ProcessConcurrently(ctx, toDelete, len(toDelete), func(ctx context.Context, container *weka.WekaContainer) error {
		return services.SetContainerStateDestroying(ctx, container, c.Client)
	}).AsError()

	if err != nil {
		return errors.Wrap(err, "failed to mark containers destroying")
	}

	if len(c.containers) > 0 {
		return lifecycle.NewWaitErrorWithDuration(errors.New("waiting for client weka containers to be deleted"), time.Second*15)
	}

	if c.clientManagesCsiDeployment() {
		err = c.UndeployCsiPlugin(ctx)
		if err != nil {
			return errors.Wrap(err, "failed to undeploy CSI plugin")
		}
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

	nodeToContainer := make(map[string]*weka.WekaContainer)
	for _, container := range c.containers {
		nodeName := string(container.Spec.NodeAffinity)
		nodeToContainer[nodeName] = container
	}

	toUpdate := []*weka.WekaContainer{}
	for _, node := range c.nodes {
		// skip nodes that do not tolerate client tolerations
		if _, ok := c.toleratedNodes[node.Name]; !ok {
			continue
		}
		// skip nodes that already have a container
		if _, ok := nodeToContainer[node.Name]; ok {
			continue
		} else {
			wekaContainer, err := c.buildClientWekaContainer(ctx, node.Name)
			if err != nil {
				return errors.Wrap(err, "failed to build client weka container")
			}
			toUpdate = append(toUpdate, wekaContainer)
		}
	}

	return workers.ProcessConcurrently(ctx, toUpdate, 32, func(ctx context.Context, wekaContainer *weka.WekaContainer) error {
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
	}).AsError()
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

	// Apply default portRange if dynamic port allocation is needed
	portRange := getDefaultedPortRange(wekaClient.Spec.Port, wekaClient.Spec.AgentPort, wekaClient.Spec.PortRange)

	labels := factory.BuildClientContainerLabels(wekaClient)

	// Apply default secret ref if not specified
	secretName := getDefaultedWekaSecretRef(wekaClient.Spec.WekaSecretRef, wekaClient.Spec.TargetCluster.Name)

	wekaContainerName := resources.GetWekaClientContainerName(wekaClient)

	container := &weka.WekaContainer{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "weka.weka.io/v1alpha1",
			Kind:       "WekaContainer",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        clientName,
			Namespace:   wekaClient.Namespace,
			Labels:      labels,
			Annotations: wekaClient.GetAnnotations(),
		},
		Spec: weka.WekaContainerSpec{
			NodeAffinity:        weka.NodeName(nodeName),
			Port:                wekaClient.Spec.Port,
			AgentPort:           wekaClient.Spec.AgentPort,
			PortRange:           portRange,
			Image:               wekaClient.Spec.Image,
			ImagePullSecret:     wekaClient.Spec.ImagePullSecret,
			WekaContainerName:   wekaContainerName,
			Mode:                weka.WekaContainerModeClient,
			NumCores:            c.getClientCores(),
			CpuPolicy:           wekaClient.Spec.CpuPolicy,
			CoreIds:             wekaClient.Spec.CoreIds,
			Network:             wekaClient.Spec.Network,
			Hugepages:           c.getClientHugePages(),
			HugepagesOffset:     c.getHugepagesOffset(),
			HugepagesSize:       "2Mi",
			WekaSecretRef:       v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{Key: secretName}},
			DriversDistService:  wekaClient.Spec.DriversDistService,
			DriversBuildId:      wekaClient.Spec.GetOverrides().DriversBuildId,
			JoinIps:             wekaClient.Spec.JoinIps,
			TracesConfiguration: wekaClient.Spec.TracesConfiguration,
			Tolerations:         tolerations,
			AdditionalMemory:    wekaClient.Spec.AdditionalMemory,
			AdditionalSecrets:   additionalSecrets,
			UpgradePolicyType:   wekaClient.Spec.UpgradePolicy.Type,
			AllowHotUpgrade:     wekaClient.Spec.AllowHotUpgrade,
			DriversLoaderImage:  wekaClient.Spec.GetOverrides().DriversLoaderImage,
			Overrides: &weka.WekaContainerSpecOverrides{
				MachineIdentifierNodeRef: wekaClient.Spec.GetOverrides().MachineIdentifierNodeRef,
				ForceDrain:               wekaClient.Spec.GetOverrides().ForceDrain,
				SkipActiveMountsCheck:    wekaClient.Spec.GetOverrides().SkipActiveMountsCheck,
				UmountOnHost:             wekaClient.Spec.GetOverrides().UmountOnHost,
			},
			AutoRemoveTimeout:     wekaClient.Spec.AutoRemoveTimeout,
			Resources:             wekaClient.Spec.Resources,
			PVC:                   resources.GetPvcConfig(wekaClient.Spec.GlobalPVC),
			NoAffinityConstraints: wekaClient.Spec.GetOverrides().DropAffinityConstraints,
		},
	}

	if wekaClient.Spec.ServiceAccountName != "" {
		container.Spec.ServiceAccountName = wekaClient.Spec.ServiceAccountName
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
	if nodeObj.Name == "" {
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

	targetCluster := c.targetCluster
	if targetCluster == nil {
		return nil
	}

	joinIps, err := services.ClustersCachedInfo.GetJoinIps(ctx, string(targetCluster.GetUID()), targetCluster.Name, targetCluster.Namespace)
	if err != nil {
		logger.Error(err, "Need to refresh join ips", "cluster", targetCluster.Name)
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

	updatableSpec := NewUpdatableClientSpec(c.wekaClient)
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

	if container.Spec.DriversBuildId != newClientSpec.DriversBuildId {
		container.Spec.DriversBuildId = newClientSpec.DriversBuildId
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
		if container.Spec.Overrides == nil {
			container.Spec.Overrides = &weka.WekaContainerSpecOverrides{}
		}
		if newClientSpec.UpgradePolicy.Type == weka.UpgradePolicyTypeAllAtOnceForce {
			container.Spec.Overrides.UpgradeForceReplace = true
		} else {
			container.Spec.Overrides.UpgradeForceReplace = false
		}
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

	if container.Spec.TracesConfiguration != newClientSpec.TracesConfiguration {
		container.Spec.TracesConfiguration = newClientSpec.TracesConfiguration
		changed = true
	}

	if container.Spec.AutoRemoveTimeout != newClientSpec.AutoRemoveTimeout {
		container.Spec.AutoRemoveTimeout = newClientSpec.AutoRemoveTimeout
		changed = true
	}

	if container.Spec.GetOverrides().ForceDrain != newClientSpec.ForceDrain {
		if container.Spec.Overrides == nil {
			container.Spec.Overrides = &weka.WekaContainerSpecOverrides{}
		}
		container.Spec.GetOverrides().ForceDrain = newClientSpec.ForceDrain
		changed = true
	}

	if container.Spec.GetOverrides().SkipActiveMountsCheck != newClientSpec.SkipActiveMountsCheck {
		if container.Spec.Overrides == nil {
			container.Spec.Overrides = &weka.WekaContainerSpecOverrides{}
		}
		container.Spec.GetOverrides().SkipActiveMountsCheck = newClientSpec.SkipActiveMountsCheck
		changed = true
	}

	if container.Spec.GetOverrides().UmountOnHost != newClientSpec.UmountOnHost {
		if container.Spec.Overrides == nil {
			container.Spec.Overrides = &weka.WekaContainerSpecOverrides{}
		}
		container.Spec.GetOverrides().UmountOnHost = newClientSpec.UmountOnHost
		changed = true
	}

	if container.Spec.NumCores != newClientSpec.CoresNumber {
		// TODO: validate that we are not updating on non-single IP interfaces, specifically not on EKS AWS DPDK
		if newClientSpec.CoresNumber < container.Spec.NumCores {
			logger.Error(errors.New("coresNum cannot be decreased"), "coresNum cannot be decreased, ignoring the change")
		} else {
			container.Spec.NumCores = c.getClientCores()
			container.Spec.Hugepages = c.getClientHugePages()
			changed = true
		}
	}

	if container.Spec.CpuPolicy != newClientSpec.CpuPolicy {
		container.Spec.CpuPolicy = newClientSpec.CpuPolicy
		changed = true
	}

	if !container.Spec.Network.Equal(&newClientSpec.Network) {
		container.Spec.Network = newClientSpec.Network
		changed = true
	}

	newTolerations := util.ExpandTolerations([]v1.Toleration{}, newClientSpec.Tolerations, newClientSpec.RawTolerations)
	oldTolerations := util.NormalizeTolerations(container.Spec.Tolerations)
	if !reflect.DeepEqual(oldTolerations, newTolerations) {
		container.Spec.Tolerations = newTolerations
		changed = true
	}

	// Propagate PVC config only if the container doesn't have one set yet
	if container.Spec.PVC == nil && newClientSpec.PvcConfig != nil {
		container.Spec.PVC = newClientSpec.PvcConfig
		changed = true
	}

	// desired labels = client's labels + required labels
	// priority-wise, required labels have the highest priority
	newLabels := factory.BuildClientContainerLabels(c.wekaClient)
	logger.Info("aligning labels", "newLabels", newLabels, "oldLabels", container.Labels)
	if !util2.NewHashableMap(newLabels).Equals(util2.NewHashableMap(container.Labels)) {
		container.Labels = newLabels
		changed = true
	}

	newAnnotations := c.wekaClient.GetAnnotations()
	oldAnnotations := container.GetAnnotations()
	if !util2.NewHashableMap(newAnnotations).Equals(util2.NewHashableMap(oldAnnotations)) {
		container.SetAnnotations(newAnnotations)
		changed = true
	}

	if container.Spec.NoAffinityConstraints != newClientSpec.DropAffinityConstraints {
		container.Spec.NoAffinityConstraints = newClientSpec.DropAffinityConstraints
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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getApplicableNodes")
	defer end()

	nodes, err := c.KubeService.GetNodes(ctx, c.wekaClient.Spec.NodeSelector)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get applicable nodes by labels")
	}

	logger.Info("Nodes matching selector", "selector", c.wekaClient.Spec.NodeSelector, "nodes", len(nodes))
	return nodes, nil
}

func (c *clientReconcilerLoop) setToleratedNodes(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "setToleratedNodes")
	defer end()

	nodes := c.nodes

	if c.toleratedNodes == nil {
		c.toleratedNodes = make(map[string]struct{})
	}

	if config.Config.SkipClientsTolerationValidation {
		logger.Debug("Skipping node taints and client tolerations check")

		// all nodes are tolerated
		for _, node := range nodes {
			c.toleratedNodes[node.Name] = struct{}{}
		}
	} else {
		clientTolerations := util.ExpandTolerations([]v1.Toleration{}, c.wekaClient.Spec.Tolerations, c.wekaClient.Spec.RawTolerations)
		// account for "expanded" NoSchedule tolerations
		clientTolerations = resources.ConditionalExpandNoScheduleTolerations(clientTolerations, !config.Config.SkipClientNoScheduleToleration)

		for _, node := range nodes {
			if node.Spec.Unschedulable {
				continue
			}
			// NOTE: we do not account for ignored taints here as we don't want to create new client containers
			// on any "unhealthy" nodes, even if the client tolerates the taint
			if !util2.CheckTolerations(node.Spec.Taints, clientTolerations, nil) {
				continue
			}
			c.toleratedNodes[node.Name] = struct{}{}
		}

		logger.Info("Nodes after taints/tolerations check", "nodes", len(c.toleratedNodes), "allNodes", len(nodes))
	}

	return nil
}

func (c *clientReconcilerLoop) GetUpgradedCount() int {
	count := 0
	for _, container := range c.containers {
		if container.Status.LastAppliedImage == c.wekaClient.Spec.Image && container.Status.LastAppliedImage == container.Spec.Image {
			count++
		}
	}
	return count
}

func (c *clientReconcilerLoop) emitClientUpgradeCustomEvent(ctx context.Context) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "emitClientUpgradeCustomEvent")
	defer end()

	logger.Info("Emitting client custom event")

	activeContainer := discovery.SelectActiveContainer(c.getClusterContainers(ctx))
	if activeContainer == nil {
		logger.Debug("Active cluster container not found, skipping Weka client event emit")
		return
	}

	count := c.GetUpgradedCount()
	key := fmt.Sprintf("upgrade-%s-%d", c.wekaClient.Spec.Image, count)
	if !c.ThrottlingMap.ShouldRun(key, &throttling.ThrottlingSettings{
		Interval:                    10 * time.Minute,
		EnsureStepSuccess:           true,
		DisableRandomPreSetInterval: true,
	}) {
		return
	}
	c.ThrottlingMap.SetNow(key)
	logger.SetValues("image", c.wekaClient.Spec.Image, "client", count)

	msg := fmt.Sprintf("Upgrading clients progress: %d:%d", count, len(c.containers))
	wekaService := services.NewWekaService(c.ExecService, activeContainer)
	err := wekaService.EmitCustomEvent(ctx, msg, utils.GetKubernetesVersion(c.Manager))
	if err != nil {
		logger.Warn("Failed to emit custom event", "event", msg)
	}
}

func (c *clientReconcilerLoop) HandleUpgrade(ctx context.Context) error {
	uController := upgrade.NewUpgradeController(c.Client, c.containers, c.wekaClient.Spec.Image)
	if uController.AreUpgraded() {
		return nil
	}

	if c.targetCluster != nil {
		c.emitClientUpgradeCustomEvent(ctx)
	}

	c.upgradeInProgress = true

	err := c.setStatusUpgrading(ctx)
	if err != nil {
		return err
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
	if int64(stats.Containers.Desired) != int64(len(c.toleratedNodes)) {
		stats.Containers.Desired = weka.IntMetric(len(c.toleratedNodes))
		changed = true
	}

	if int64(stats.Containers.Created) != int64(len(c.containers)) {
		stats.Containers.Created = weka.IntMetric(len(c.containers))
		changed = true
	}

	totalActive := 0
	for _, container := range c.containers {
		if container.Status.Status == weka.Running && container.Status.ClusterContainerID != nil {
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

func (c *clientReconcilerLoop) getClientCores() int {
	numCores := c.wekaClient.Spec.CoresNumber
	if numCores == 0 {
		numCores = 1
	}
	return numCores
}

func (c *clientReconcilerLoop) getClientHugePages() int {
	if c.wekaClient.Spec.HugePages != 0 {
		return c.wekaClient.Spec.HugePages
	}
	return c.getClientCores() * 1500
}
func (c *clientReconcilerLoop) getHugepagesOffset() int {
	if c.wekaClient.Spec.HugePagesOffset != nil {
		return *c.wekaClient.Spec.HugePagesOffset
	}
	return 200 // back compat mode/unspecified default
}

func (c *clientReconcilerLoop) deleteContainersOnNodeSelectorMismatch(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	applicableNodes := make(map[string]struct{}, len(c.nodes))
	for _, node := range c.nodes {
		applicableNodes[node.Name] = struct{}{}
	}

	toDelete := services.FilterContainersForDeletion(c.containers, func(container *weka.WekaContainer) bool {
		shouldDelete := false
		if _, ok := applicableNodes[string(container.Spec.NodeAffinity)]; !ok {
			shouldDelete = true
		}
		return shouldDelete
	})

	if len(toDelete) == 0 {
		return nil
	}
	logger.Info("Deleting containers with node selector mismatch", "toDelete", len(toDelete))
	return workers.ProcessConcurrently(ctx, toDelete, len(toDelete), func(ctx context.Context, container *weka.WekaContainer) error {
		c.Recorder.Event(container, v1.EventTypeNormal, "NodeSelectorMismatch", "Node selector mismatch, deleting container")

		return services.SetContainerStateDeleting(ctx, container, c.Client)
	}).AsError()
}

func (c *clientReconcilerLoop) deleteContainersOnTolerationsMismatch(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	toDelete := services.FilterContainersForDeletion(c.containers, func(container *weka.WekaContainer) bool {
		return container.Status.NotToleratedOnReschedule
	})

	if len(toDelete) == 0 {
		return nil
	}
	logger.Info("Deleting containers with tolerations mismatch", "toDelete", len(toDelete))
	return workers.ProcessConcurrently(ctx, toDelete, len(toDelete), func(ctx context.Context, container *weka.WekaContainer) error {
		return services.SetContainerStateDeleting(ctx, container, c.Client)
	}).AsError()
}

func (c *clientReconcilerLoop) setStatusRunning(ctx context.Context) error {
	return c.updateStatusIfNotEquals(ctx, weka.WekaClientStatusRunning)
}

func (c *clientReconcilerLoop) setStatusUpgrading(ctx context.Context) error {
	return c.updateStatusIfNotEquals(ctx, weka.WekaClientStatusUpgrading)
}

func (c *clientReconcilerLoop) updateStatusIfNotEquals(ctx context.Context, newStatus weka.WekaClientStatusEnum) error {
	if c.wekaClient.Status.Status != newStatus {
		c.wekaClient.Status.Status = newStatus
		err := c.Status().Update(ctx, c.wekaClient)
		if err != nil {
			return errors.Wrap(err, "failed to update wekaClient status")
		}
	}
	return nil
}

func isPortRangeEqual(a, b weka.PortRange) bool {
	return a.BasePort == b.BasePort && a.PortRange == b.PortRange
}

// getDefaultedPortRange applies default portRange logic if any of port or agentPort is 0 (dynamic port allocation).
// Returns a default portRange with BasePort set to defaultPortRangeBase.
func getDefaultedPortRange(port, agentPort int, portRange *weka.PortRange) *weka.PortRange {
	if port == 0 || agentPort == 0 {
		if portRange == nil {
			return &weka.PortRange{
				BasePort: defaultPortRangeBase,
			}
		}
	}
	return portRange
}

// getDefaultedWekaSecretRef applies default WekaSecretRef logic if empty.
// Returns the target cluster's secret name if WekaSecretRef is empty and TargetCluster.Name is set.
func getDefaultedWekaSecretRef(wekaSecretRef string, targetClusterName string) string {
	// if the user didn't set a secret ref, we need to set it to the target cluster's secret ref
	// this is needed for the client to be able to connect to the target cluster
	if wekaSecretRef == "" && targetClusterName != "" {
		return weka.GetClientSecretName(targetClusterName)
	}
	return wekaSecretRef
}

type UpdatableClientSpec struct {
	DriversDistService      string
	DriversBuildId          *string
	ImagePullSecret         string
	WekaSecretRef           string
	AdditionalMemory        int
	UpgradePolicy           weka.UpgradePolicy
	AllowHotUpgrade         bool
	DriversLoaderImage      string
	Port                    int
	AgentPort               int
	PortRange               *weka.PortRange
	CoresNumber             int
	Tolerations             []string
	RawTolerations          []v1.Toleration
	Labels                  *util2.HashableMap
	Annotations             *util2.HashableMap
	AutoRemoveTimeout       metav1.Duration
	ForceDrain              bool
	SkipActiveMountsCheck   bool
	UmountOnHost            bool
	PvcConfig               *weka.PVCConfig
	TracesConfiguration     *weka.TracesConfiguration
	CpuPolicy               weka.CpuPolicy
	Network                 weka.Network
	DropAffinityConstraints bool
}

func NewUpdatableClientSpec(client *weka.WekaClient) *UpdatableClientSpec {
	labels := util2.NewHashableMap(factory.BuildClientContainerLabels(client))
	spec := client.Spec
	meta := client.ObjectMeta

	return &UpdatableClientSpec{
		DriversDistService:      spec.DriversDistService,
		DriversBuildId:          spec.GetOverrides().DriversBuildId,
		ImagePullSecret:         spec.ImagePullSecret,
		WekaSecretRef:           getDefaultedWekaSecretRef(spec.WekaSecretRef, spec.TargetCluster.Name),
		AdditionalMemory:        spec.AdditionalMemory,
		UpgradePolicy:           spec.UpgradePolicy,
		AllowHotUpgrade:         spec.AllowHotUpgrade,
		DriversLoaderImage:      spec.GetOverrides().DriversLoaderImage,
		Port:                    spec.Port,
		AgentPort:               spec.AgentPort,
		PortRange:               getDefaultedPortRange(spec.Port, spec.AgentPort, spec.PortRange),
		CoresNumber:             spec.CoresNumber,
		Tolerations:             spec.Tolerations,
		RawTolerations:          spec.RawTolerations,
		Labels:                  labels,
		Annotations:             util2.NewHashableMap(meta.Annotations),
		AutoRemoveTimeout:       spec.AutoRemoveTimeout,
		ForceDrain:              spec.GetOverrides().ForceDrain,
		SkipActiveMountsCheck:   spec.GetOverrides().SkipActiveMountsCheck,
		UmountOnHost:            spec.GetOverrides().UmountOnHost,
		PvcConfig:               resources.GetPvcConfig(spec.GlobalPVC),
		TracesConfiguration:     spec.TracesConfiguration,
		CpuPolicy:               spec.CpuPolicy,
		Network:                 spec.Network,
		DropAffinityConstraints: spec.GetOverrides().DropAffinityConstraints,
	}
}

func (c *clientReconcilerLoop) FetchTargetCluster(ctx context.Context) error {
	wekaCluster := &weka.WekaCluster{}
	err := c.Get(ctx, client.ObjectKey{
		Name:      c.wekaClient.Spec.TargetCluster.Name,
		Namespace: c.wekaClient.Spec.TargetCluster.Namespace,
	}, wekaCluster)
	if err != nil {
		return errors.Wrap(err, "failed to get target cluster")
	}
	c.targetCluster = wekaCluster
	return nil
}

func (c *clientReconcilerLoop) getClusterContainers(ctx context.Context) []*weka.WekaContainer {
	return discovery.GetClusterContainers(ctx, c.Manager.GetClient(), c.targetCluster, "")
}

func (c *clientReconcilerLoop) CheckCsiConfigChanged(ctx context.Context) error {
	if !config.Config.Csi.Enabled {
		// if we are here, installation was switched off
		return c.UndeployCsiPlugin(ctx)
	}

	//TODO: check for csi config changes
	//ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	//defer end()
	//csiDriverName := c.getCsiDriverName()
	//if len(c.containers) > 0 && c.containers[0].Spec.TypedConfigs != nil && csiDriverName != c.containers[0].Spec.TypedConfigs.TypedClientConfigs.CSIDriverName {
	//	logger.Info("CSI driver name changed, undeploying CSI plugin")
	//	// cleanup with old name
	//	return c.UndeployCsiPlugin(ctx)
	//}
	return nil
}

func (c *clientReconcilerLoop) DeployCsiPlugin(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	op, err := operations.NewDeployCsiOperation(
		c.Manager.GetClient(),
		c.wekaClient,
		c.GetCSIGroup(),
		c.nodes,
		false,
	)
	if err != nil {
		return err
	}

	err = operations.ExecuteOperation(ctx, op)
	if err != nil {
		logger.Error(err, "failed to deploy CSI plugin")
		return err
	}

	return nil
}

func (c *clientReconcilerLoop) UndeployCsiPlugin(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "UndeployCsiPlugin")
	defer end()

	logger.Info("Undeploying CSI plugin")
	op, err := operations.NewDeployCsiOperation(
		c.Manager.GetClient(),
		c.wekaClient,
		c.GetCSIGroup(),
		c.nodes,
		true,
	)
	if err != nil {
		return err
	}
	err = operations.ExecuteOperation(ctx, op)
	if err != nil {
		logger.Error(err, "failed to undeploy CSI plugin")
		return err
	}

	return nil
}

func (c *clientReconcilerLoop) GetCSIGroup() string {
	if c.targetCluster != nil {
		return csi.GetGroupFromTargetCluster(c.targetCluster)
	}
	return csi.GetGroupFromClient(c.wekaClient)
}

func (c *clientReconcilerLoop) UpdateCsiController(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	deployment, err := c.getExistingCsiControllerDeployment(ctx)
	if err != nil {
		return err
	}
	if deployment == nil {
		logger.Info("CSI controller deployment not found, skipping update")
		return nil
	}

	targetDeployment, err := csi.NewCsiControllerDeployment(ctx, c.GetCSIGroup(), c.wekaClient)
	if err != nil {
		return err
	}

	currentHash, _ := deployment.Spec.Template.Annotations["weka.io/csi-controller-hash"]
	targetHash, _ := targetDeployment.Spec.Template.Annotations["weka.io/csi-controller-hash"]
	if targetHash != currentHash {
		logger.Info("CSI controller deployment spec changed, updating deployment",
			"targetHash", targetHash, "currentHash", currentHash)

		// Preserve the existing resource version and UID for proper updates
		targetDeployment.ObjectMeta.ResourceVersion = deployment.ObjectMeta.ResourceVersion
		targetDeployment.ObjectMeta.UID = deployment.ObjectMeta.UID

		operatorDeployment, err := util2.GetOperatorDeployment(ctx, c.Client)
		if err != nil {
			return errors.Wrap(err, "failed to get operator deployment")
		}

		// set owner reference to the operator deployment
		err = controllerutil.SetControllerReference(operatorDeployment, targetDeployment, c.Scheme)
		if err != nil {
			return err
		}

		ctx, _, end := instrumentation.GetLogSpan(ctx, "doUpdateCsiController")
		defer end()

		return c.Client.Update(ctx, targetDeployment)
	}

	logger.Debug("CSI controller deployment is up to date", "targetHash", targetHash, "csiImage", config.Config.Csi.WekafsImage)
	return nil
}

func (c *clientReconcilerLoop) CheckExistingCsiControllerOwner(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	deployment, err := c.getExistingCsiControllerDeployment(ctx)
	if err != nil {
		return err
	}
	if deployment == nil {
		logger.Info("CSI controller deployment not found, skipping owner check")
		return nil
	}

	isOwnedByDifferentClient, err := c.isOwnedByDifferentWekaClient(ctx, deployment)
	if err != nil {
		return err
	}

	c.csiDeploymentOwnedByDifferentClient = isOwnedByDifferentClient

	return nil
}

func (c *clientReconcilerLoop) getExistingCsiControllerDeployment(ctx context.Context) (*appsv1.Deployment, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getExistingCsiControllerDeployment")
	defer end()

	controllerDeploymentName := csi.GetCSIControllerName(c.GetCSIGroup())
	namespace, err := util2.GetPodNamespace()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get pod namespace")
	}

	deployment := &appsv1.Deployment{}
	err = c.Get(ctx, client.ObjectKey{
		Name:      controllerDeploymentName,
		Namespace: namespace,
	}, deployment)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("CSI controller deployment not found")
			return nil, nil
		}
		return nil, errors.Wrap(err, "failed to get CSI controller deployment")
	}

	return deployment, nil
}

func (c *clientReconcilerLoop) isOwnedByDifferentWekaClient(ctx context.Context, deployment *appsv1.Deployment) (bool, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "isOwnedByDifferentWekaClient")
	defer end()

	// if csi controller was deployed by different client, and this client still exists, do not interfere
	ownerWekaClientAnnotation, ok := deployment.Spec.Template.Annotations["weka.io/csi-controller-owner"]
	if ok && ownerWekaClientAnnotation != string(c.wekaClient.GetUID()) {
		logger.Debug("CSI controller deployment is owned by different WekaClient",
			"ownerWekaClient", ownerWekaClientAnnotation, "currentWekaClient", string(c.wekaClient.GetUID()))

		// check if the owner WekaClient still exists
		ownerClientName, _ := deployment.Spec.Template.Annotations["weka.io/csi-controller-owner-name"]
		ownerClientNamespace, _ := deployment.Spec.Template.Annotations["weka.io/csi-controller-owner-namespace"]

		// both annotations must be present, otherwise we want to proceed with update and take ownership
		if ownerClientName == "" || ownerClientNamespace == "" {
			logger.Info("Owner WekaClient annotations are missing, can take ownership of CSI controller deployment",
				"ownerWekaClient", ownerWekaClientAnnotation, "ownerClientName", ownerClientName, "ownerClientNamespace", ownerClientNamespace)

			return false, nil
		}

		ownerClient := &weka.WekaClient{}
		err := c.Get(ctx, client.ObjectKey{
			Name:      ownerClientName,
			Namespace: ownerClientNamespace,
		}, ownerClient)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("Owner WekaClient not found, can take ownership of CSI controller deployment",
					"ownerWekaClient", ownerWekaClientAnnotation, "ownerClientName", ownerClientName, "ownerClientNamespace", ownerClientNamespace)
				return false, nil
			}
			return false, errors.Wrap(err, "failed to get owner WekaClient")
		}

		// owner WekaClient still exists, do not interfere
		logger.Info("Owner WekaClient still exists, cannot take ownership of CSI controller deployment",
			"ownerWekaClient", ownerWekaClientAnnotation, "ownerClientName", ownerClientName, "ownerClientNamespace", ownerClientNamespace)
		return true, nil
	}

	logger.Debug("CSI controller deployment is not owned by different WekaClient")

	return false, nil
}

func (c *clientReconcilerLoop) UpdateCsiNodeDaemonSet(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	daemonSet, err := c.getExistingCsiNodeDaemonSet(ctx)
	if err != nil {
		return err
	}
	if daemonSet == nil {
		logger.Info("CSI node daemonset not found, skipping update")
		return nil
	}

	targetDaemonSet, err := csi.NewCsiNodeDaemonSet(ctx, c.GetCSIGroup(), c.wekaClient)
	if err != nil {
		return err
	}

	currentHash, _ := daemonSet.Spec.Template.Annotations["weka.io/csi-node-hash"]
	targetHash, _ := targetDaemonSet.Spec.Template.Annotations["weka.io/csi-node-hash"]
	if targetHash != currentHash {
		logger.Info("CSI node daemonset spec changed, updating daemonset",
			"targetHash", targetHash, "currentHash", currentHash)

		// Preserve the existing resource version and UID for proper updates
		targetDaemonSet.ObjectMeta.ResourceVersion = daemonSet.ObjectMeta.ResourceVersion
		targetDaemonSet.ObjectMeta.UID = daemonSet.ObjectMeta.UID

		operatorDeployment, err := util2.GetOperatorDeployment(ctx, c.Client)
		if err != nil {
			return errors.Wrap(err, "failed to get operator deployment")
		}

		// set owner reference to the operator deployment
		err = controllerutil.SetControllerReference(operatorDeployment, targetDaemonSet, c.Scheme)
		if err != nil {
			return err
		}

		ctx, _, end := instrumentation.GetLogSpan(ctx, "doUpdateCsiNodeDaemonSet")
		defer end()

		return c.Client.Update(ctx, targetDaemonSet)
	}

	logger.Debug("CSI node daemonset is up to date", "targetHash", targetHash, "csiImage", config.Config.Csi.WekafsImage)
	return nil
}

func (c *clientReconcilerLoop) getExistingCsiNodeDaemonSet(ctx context.Context) (*appsv1.DaemonSet, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getExistingCsiNodeDaemonSet")
	defer end()

	nodeDaemonSetName := csi.GetCSINodeDaemonSetName(c.GetCSIGroup())
	namespace, err := util2.GetPodNamespace()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get pod namespace")
	}

	daemonSet := &appsv1.DaemonSet{}
	err = c.Get(ctx, client.ObjectKey{
		Name:      nodeDaemonSetName,
		Namespace: namespace,
	}, daemonSet)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("CSI node daemonset not found")
			return nil, nil
		}
		return nil, errors.Wrap(err, "failed to get CSI node daemonset")
	}

	return daemonSet, nil
}
