package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	allocator2 "github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/condition"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/resources"
	weka "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"strings"
	"sync"
	"time"
)

func NewContainerReconcileLoop(mgr ctrl.Manager) *containerReconcilerLoop {
	config := mgr.GetConfig()
	kClient := mgr.GetClient()
	execService := exec.NewExecService(config)
	return &containerReconcilerLoop{
		Client:      kClient,
		Scheme:      mgr.GetScheme(),
		Logger:      mgr.GetLogger().WithName("controllers").WithName("Container"),
		KubeService: kubernetes.NewKubeService(mgr.GetClient()),
		ExecService: execService,
		Manager:     mgr,
	}
}

// LockMap is a structure that holds a map of locks
type LockMap struct {
	locks sync.Map // Concurrent map to store the locks
}

// GetLock retrieves or creates a lock for a given identifier
func (lm *LockMap) GetLock(id string) *sync.Mutex {
	actual, _ := lm.locks.LoadOrStore(id, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

type containerReconcilerLoop struct {
	client.Client
	Scheme           *runtime.Scheme
	Logger           logr.Logger
	KubeService      kubernetes.KubeService
	ExecService      exec.ExecService
	Manager          ctrl.Manager
	container        *weka.WekaContainer
	pod              *v1.Pod
	nodeAffinityLock LockMap
	node             *v1.Node
}

func (r *containerReconcilerLoop) FetchContainer(ctx context.Context, req ctrl.Request) error {
	container := &weka.WekaContainer{}
	err := r.Get(ctx, req.NamespacedName, container)
	if err != nil {
		container = nil
		if !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	r.container = container
	return nil
}

func ContainerReconcileSteps(mgr ctrl.Manager, container *weka.WekaContainer) lifecycle.ReconciliationSteps {
	loop := NewContainerReconcileLoop(mgr)
	loop.container = container

	return lifecycle.ReconciliationSteps{
		Client:           loop.Client,
		ConditionsObject: loop.container,
		Conditions:       &loop.container.Status.Conditions,
		Steps: []lifecycle.Step{
			{
				Run: loop.HandleDeletion,
				Predicates: lifecycle.Predicates{
					container.IsMarkedForDeletion,
				},
				ContinueOnPredicatesFalse: true,
				FinishOnSuccess:           true,
			},
			{Run: loop.initState},
			{Run: loop.deleteIfNoNode},
			{Run: loop.ensureFinalizer},
			{Run: loop.ensureBootConfigMapInTargetNamespace},
			{Run: loop.refreshPod},
			//{Run: loop.setNodeAffinity},
			{
				Run: loop.ensurePod,
				Predicates: lifecycle.Predicates{
					loop.PodNotSet,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run:       loop.enforceNodeAffinity,
				Condition: condition.CondContainerAffinitySet,
				Predicates: lifecycle.Predicates{
					func() bool {
						return loop.container.IsAllocatable() && loop.container.IsBackend()
					},
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.CleanupUnschedulable,
				Predicates: lifecycle.Predicates{
					func() bool {
						if loop.pod == nil {
							panic("pod is nil, it must be set at this point")
						}
						return loop.pod.Status.Phase == v1.PodPending
					},
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.cleanupFinished,
				Predicates: lifecycle.Predicates{
					func() bool {
						return loop.pod.Status.Phase == v1.PodSucceeded
					},
				},
				ContinueOnPredicatesFalse: true,
			},
			{Run: loop.resetEnsureDriversForNewPod},
			{Run: loop.WaitForRunning},
			{
				Run:       loop.WriteResources,
				Condition: condition.CondContainerResourcesWritten,
				Predicates: lifecycle.Predicates{
					container.IsAllocatable,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition: condition.CondEnsureDrivers,
				Run:       loop.EnsureDrivers, // TODO: Complex, refactor as operation!
				Predicates: lifecycle.Predicates{
					container.IsWekaContainer,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.DriverLoaderFinished,
				Predicates: lifecycle.Predicates{
					func() bool {
						return container.Spec.Mode == weka.WekaContainerModeDriversLoader
					},
				},
				ContinueOnPredicatesFalse: true,
				FinishOnSuccess:           true,
			},
			{
				Run: loop.reconcileManagementIP, // TODO: #shouldRefresh?
				Predicates: lifecycle.Predicates{
					lifecycle.IsEmptyString(container.Status.ManagementIP),
					container.IsBackend,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.fetchResults,
				Predicates: lifecycle.Predicates{
					loop.container.IsOneOff,
				},
				FinishOnSuccess:           true,
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.reconcileWekaLocalStatus,
				Predicates: lifecycle.Predicates{
					container.IsWekaContainer,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition: condition.CondJoinedCluster,
				Run:       loop.reconcileClusterStatus,
				Predicates: lifecycle.Predicates{
					container.IsWekaContainer,
				},
				ContinueOnPredicatesFalse: true,
				CondMessage:               "Container joined cluster",
			},
			{
				Condition:   condition.CondDrivesAdded,
				Run:         loop.EnsureDrives,
				CondMessage: fmt.Sprintf("Added %d drives", container.Spec.NumDrives),
				Predicates: lifecycle.Predicates{
					container.IsDriveContainer,
					func() bool {
						return loop.container.Spec.NumDrives > 0
					},
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition:   condition.CondJoinedS3Cluster,
				Run:         loop.JoinS3Cluster,
				CondMessage: "Joined s3 cluster",
				Predicates: lifecycle.Predicates{
					container.IsS3Container,
					container.HasJoinIps,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.handleImageUpdate,
				Predicates: lifecycle.Predicates{
					lifecycle.IsNotFunc(container.IsClientContainer),
					func() bool {
						return container.Spec.Image != container.Status.LastAppliedImage
					},
				},
				ContinueOnPredicatesFalse: true,
			},
		},
	}
}

func (r *containerReconcilerLoop) HandleDeletion(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	err := r.finalizeContainer(ctx)
	if err != nil {
		return err
	}
	controllerutil.RemoveFinalizer(r.container, WekaFinalizer)
	err = r.Update(ctx, r.container)
	if err != nil {
		logger.Error(err, "Error removing finalizer")
		return errors.Wrap(err, "Failed to remove finalizer")
	}
	return nil
}

func (r *containerReconcilerLoop) initState(ctx context.Context) error {
	if r.container.Status.Conditions == nil {
		r.container.Status.Conditions = []metav1.Condition{}
	}

	changes := false
	if !r.container.DriversReady() && r.container.SupportsEnsureDriversCondition() {
		changes = true
		meta.SetStatusCondition(&r.container.Status.Conditions,
			metav1.Condition{Type: condition.CondEnsureDrivers, Status: metav1.ConditionFalse, Message: "Init", Reason: "Init"},
		)
	}

	if r.container.Status.LastAppliedImage == "" {
		r.container.Status.LastAppliedImage = r.container.Spec.Image
		changes = true
	}

	if changes {
		err := r.Status().Update(ctx, r.container)
		if err != nil {
			return err
		}
	}
	return nil
}

// Implement the remaining methods (createPod, reconcileDriversStatus, reconcileManagementIP, etc.)
// by adapting the logic from the original container_controller.go file.

func (r *containerReconcilerLoop) ensureBootConfigMapInTargetNamespace(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureBootConfigMapInTargetNamespace")
	defer end()

	bundledConfigMap := &v1.ConfigMap{}
	podNamespace, err := util.GetPodNamespace()
	if err != nil {
		logger.Error(err, "Error getting pod namespace")
		return err
	}
	key := client.ObjectKey{Namespace: podNamespace, Name: bootScriptConfigName}
	if err := r.Get(ctx, key, bundledConfigMap); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Error(err, "Bundled config map not found")
			return err
		}
		logger.Error(err, "Error getting bundled config map")
		return err
	}

	bootScripts := &v1.ConfigMap{}
	err = r.Get(ctx, client.ObjectKey{Namespace: r.container.Namespace, Name: bootScriptConfigName}, bootScripts)
	if err != nil {
		if apierrors.IsNotFound(err) {
			bootScripts.Namespace = r.container.Namespace
			bootScripts.Name = bootScriptConfigName
			bootScripts.Data = bundledConfigMap.Data
			if err := r.Create(ctx, bootScripts); err != nil {
				if apierrors.IsAlreadyExists(err) {
					logger.Info("Boot scripts config map already exists in designated namespace")
				} else {
					logger.Error(err, "Error creating boot scripts config map")
				}
			}
			logger.Info("Created boot scripts config map in designated namespace")
		}
	}

	if !util.IsEqualConfigMapData(bootScripts, bundledConfigMap) {
		bootScripts.Data = bundledConfigMap.Data
		if err := r.Update(ctx, bootScripts); err != nil {
			logger.Error(err, "Error updating boot scripts config map")
			return err
		}
		logger.InfoWithStatus(codes.Ok, "Updated and reconciled boot scripts config map in designated namespace")

	}
	return nil
}

func (r *containerReconcilerLoop) refreshPod(ctx context.Context) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "refreshPod")
	defer end()

	pod := &v1.Pod{}
	key := client.ObjectKey{Name: r.container.Name, Namespace: r.container.Namespace}
	if err := r.Get(ctx, key, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	r.pod = pod
	return nil
}

func (r *containerReconcilerLoop) reconcileManagementIP(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container
	pod := r.pod

	executor, err := util.NewExecInPod(pod)
	if err != nil {
		return err
	}

	var getIpCmd string
	if container.Spec.Network.EthDevice != "" {
		if container.Spec.Ipv6 {
			getIpCmd = fmt.Sprintf("ip -6 addr show dev %s | grep 'inet6 ' | awk '{print $2}' | cut -d/ -f1", container.Spec.Network.EthDevice)
		} else {
			getIpCmd = fmt.Sprintf("ip addr show dev %s | grep 'inet ' | awk '{print $2}' | cut -d/ -f1", container.Spec.Network.EthDevice)
		}
	} else {
		if container.Spec.Ipv6 {
			getIpCmd = fmt.Sprintf("ip -6 addr show $(ip -6 route show default | awk '{print $5}' | head -n1) | grep 'inet6 ' | grep global | awk '{print $2}' | cut -d/ -f1")
		} else {
			getIpCmd = fmt.Sprintf("ip route show default | grep src | awk '/default/ {print $9}' | head -n1")
		}
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "GetManagementIpAddress", []string{"bash", "-ce", getIpCmd})
	if err != nil {
		logger.Error(err, "Error executing command", "stderr", stderr.String())
		return err
	}
	ipAddress := strings.TrimSpace(stdout.String())
	logger.WithValues("management_ip", ipAddress).Info("Got management IP")
	if container.Status.ManagementIP != ipAddress {
		container.Status.ManagementIP = ipAddress
		if err := r.Status().Update(ctx, container); err != nil {
			logger.Error(err, "Error updating status")
			return err
		}
		return nil
	}
	return nil
}

func (r *containerReconcilerLoop) reconcileClusterStatus(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container
	pod := r.pod

	executor, err := util.NewExecInPod(pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return err
	}

	containerName := container.Spec.WekaContainerName

	cmd := fmt.Sprintf("weka local run wapi -H localhost:%d/jrpc -W container-get-identity --container-name %s --json", container.GetAgentPort(), containerName)
	if container.Spec.JoinIps != nil {
		cmd = fmt.Sprintf("wekaauthcli local run wapi -H localhost:%d/jrpc -W container-get-identity --container-name %s --json", container.GetAgentPort(), containerName)
	}

	stdout, _, err := executor.ExecNamed(ctx, "WekaLocalContainerGetIdentity", []string{"bash", "-ce", cmd})
	if err != nil {
		return lifecycle.NewWaitError(err)
	}
	logger.Debug("Parsing weka local container-get-identity")
	response := resources.WekaLocalContainerGetIdentityResponse{}
	err = json.Unmarshal(stdout.Bytes(), &response)
	if err != nil {
		logger.Error(err, "Error parsing weka local status")
		return lifecycle.NewWaitError(err)
	}

	if response.Value == nil {
		return lifecycle.NewWaitError(errors.New("no value in response from weka local container-get-identity"))
	}
	if response.Value.ClusterId == "" || response.Value.ClusterId == "00000000-0000-0000-0000-000000000000" {
		return lifecycle.NewWaitError(errors.New("cluster not ready"))
	}

	container.Status.ClusterContainerID = &response.Value.ContainerId
	container.Status.ClusterID = response.Value.ClusterId
	logger.InfoWithStatus(
		codes.Ok,
		"Cluster created and its GUID and container ID are updated in WekaContainer status",
		"cluster_guid", response.Value.ClusterId,
		"container_id", response.Value.ContainerId,
	)
	if err := r.Status().Update(ctx, container); err != nil {
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) ensureFinalizer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureFinalizer")
	defer end()

	container := r.container

	if ok := controllerutil.AddFinalizer(container, WekaFinalizer); !ok {
		return nil
	}

	logger.Info("Adding Finalizer for weka container")
	err := r.Update(ctx, container)
	if err != nil {
		logger.Error(err, "Failed to update wekaCluster with finalizer")
		return err
	}
	return nil
}
func (r *containerReconcilerLoop) finalizeContainer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "finalizeContainer")
	defer end()

	// TODO: Break apart/wrap as OP

	container := r.container

	err := r.ensureTombstone(ctx)
	if err != nil {
		logger.Error(err, "Error ensuring tombstone")
		return err
	}

	//tombstone first, delete pod next
	//tombstone will have to ensure that no pod exists by itself

	// ensure no pod exists
	err = r.ensureNoPod(ctx)
	if err != nil {
		return err
	}

	err = allocator2.DeallocateContainer(ctx, container, r.Client)
	if err != nil {
		logger.Error(err, "Error deallocating container")
		return err
	} else {
		logger.Info("Container deallocated")
	}
	// deallocate from allocmap

	return nil
}

func (r *containerReconcilerLoop) ensureTombstone(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureTombstone")
	defer end()

	container := r.container

	// skip tombstone creation for containers without persistent storage
	if !container.HasPersistentStorage() {
		logger.Debug("Container has no persistent storage, skipping tombstone creation", "container", container.Name)
		return nil
	}

	nodeAffinity := container.GetNodeAffinity()
	// This code needed only for back compatibility
	// TODO: Fix and remove by having reconcile loop that will update status from pod
	// And changing allocation logic to update status for future iterations
	if nodeAffinity == "" {
		// attempting to find persistent location of the container based on actual pod
		err := r.refreshPod(ctx)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("no affinity on container and actual pod not found to set affinity")
				return nil
			}
		}
		if r.pod == nil {
			return nil // no pod, no affinity
		}
		if r.pod.Spec.NodeName != "" {
			nodeAffinity = weka.NodeName(r.pod.Spec.NodeName)
		}
	}

	nodeInfo, err := r.GetNodeInfo(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("node is deleted, no need for tombstone")
			return nil
		}
		// better to define specific error type for this, and helper function that would unwrap steps-execution exceptions
		// as an option, we should look into preserving original error without unwrapping. i.e abort+wait are encapsulated control cycles
		// but generic ReconcilationError wrapping error is sort of pointless
		if strings.Contains(err.Error(), "error reconciling object during phase GetNode: Node") && strings.Contains(err.Error(), "not found") {
			logger.Info("node is deleted, no need for tombstone")
			return nil
		}
		logger.Error(err, "Error getting node discovery")
		return err
	}

	tombstone := &weka.Tombstone{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("wekacontainer-%s", container.GetUID()),
			Namespace: container.Namespace,
		},
		Spec: weka.TombstoneSpec{
			CrType:          "WekaContainer",
			CrId:            string(container.UID),
			NodeAffinity:    nodeAffinity,
			PersistencePath: nodeInfo.GetHostsideContainerPersistence(),
			ContainerName:   container.Name,
		},
	}

	err = r.Create(ctx, tombstone)
	if err != nil {
		if !apierrors.IsAlreadyExists(err) {
			logger.Error(err, "Error creating tombstone")
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) GetNodeInfo(ctx context.Context) (*discovery.DiscoveryNodeInfo, error) {
	container := r.container
	nodeAffinity := container.GetNodeAffinity()
	if container.GetNodeAffinity() == "" {
		// HACK: Since we don't really know the node affinity, we will try to discover it by labels
		// Assuming, that supplied labels represent unique type of machines
		// this puts a requirement on user to separate machines by labels, which is common approach
		nodes, err := r.KubeService.GetNodes(ctx, container.Spec.NodeSelector)
		if err != nil {
			return nil, err
		}
		if len(nodes) == 0 {
			return nil, errors.New("no matching nodes found")
		}
		nodeAffinity = weka.NodeName(nodes[0].Name)
	}
	discoverNodeOp := operations.NewDiscoverNodeOperation(
		r.Manager,
		nodeAffinity,
		container,
		container.ToOwnerObject(),
	)
	err := operations.ExecuteOperation(ctx, discoverNodeOp)
	if err != nil {
		return nil, err
	}

	return discoverNodeOp.GetResult(), nil
}

func (r *containerReconcilerLoop) ensureNoPod(ctx context.Context) error {
	//TODO: Can we search pods by ownership?

	container := r.container

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureNoPod")
	defer end()

	pod := &v1.Pod{}
	err := r.Get(ctx, client.ObjectKey{Name: container.Name, Namespace: container.Namespace}, pod)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		logger.Error(err, "Error getting pod")
		return err
	}

	if pod.Status.Phase == v1.PodRunning {
		executor, err := util.NewExecInPod(pod)
		if err != nil {
			logger.Error(err, "Error creating executor")
			return err

		}
		// setting for forceful termination ,as we are in container delete flow
		_, _, err = executor.ExecNamed(ctx, "AllowForceStop", []string{"bash", "-ce", "touch /tmp/.allow-force-stop"})
		if err != nil {
			if !strings.Contains(err.Error(), "container not found") {
				return err
			}
		}
	}

	err = r.Delete(ctx, pod)
	if err != nil {
		logger.Error(err, "Error deleting pod")
		return err
	}
	logger.AddEvent("Pod deleted")
	return lifecycle.NewWaitError(errors.New("Pod deleted, reconciling for retry"))
}

func (r *containerReconcilerLoop) setNodeAffinity(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container
	actualPod := r.pod

	setAffinity := container.GetNodeAffinity()
	if actualPod == nil && setAffinity == "" {
		nodes, err := r.KubeService.GetNodes(ctx, container.Spec.NodeSelector)
		if err != nil {
			logger.Error(err, "Error getting nodes")
			return err
		}

		if len(nodes) == 0 {
			return errors.New("No nodes found")
		}

		// arbitrary select first node
		// TODO: Select lowest utilziation instead
		setAffinity = weka.NodeName(nodes[0].ObjectMeta.Name)
		container.Status.NodeAffinity = setAffinity
		err = r.Status().Update(ctx, container)
		if err != nil {
			logger.Error(err, "Error updating container status")
			return err
		}
	}

	if actualPod == nil {
		if setAffinity != "" {
			return nil
		}
	}

	setAffinity = container.GetNodeAffinity()

	if weka.NodeName(actualPod.Spec.NodeName) != setAffinity {
		// Note: This makes an assumption on "never re-allocate" WekaContainer, i.e if there is a pod - pod knows better
		container.Status.NodeAffinity = weka.NodeName(actualPod.Spec.NodeName)
		err := r.Status().Update(ctx, container)
		if err != nil {
			logger.Error(err, "Error updating container status")
			return err
		}
	}

	return nil
}

func (r *containerReconcilerLoop) ensurePod(ctx context.Context) error {
	container := r.container

	nodeInfo := &discovery.DiscoveryNodeInfo{}
	if !container.IsDiscoveryContainer() {
		var err error
		nodeInfo, err = r.GetNodeInfo(ctx)
		if err != nil {
			return err
		}
	}

	desiredPod, err := resources.NewPodFactory(container, nodeInfo).Create(ctx)
	if err != nil {
		return errors.Wrap(err, "Failed to create pod spec")
	}

	if err := ctrl.SetControllerReference(container, desiredPod, r.Scheme); err != nil {
		return errors.Wrapf(err, "Error setting controller reference")
	}

	if err := r.Create(ctx, desiredPod); err != nil {
		return errors.Wrap(err, "Failed to create pod")
	}
	r.pod = desiredPod
	err = r.refreshPod(ctx)
	if err != nil {
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) CleanupUnschedulable(ctx context.Context) error {
	kubeService := r.KubeService

	pod := r.pod
	container := r.container

	unschedulable := false
	unschedulableSince := time.Time{}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == v1.PodScheduled && condition.Status == v1.ConditionFalse && condition.Reason == "Unschedulable" {
			unschedulable = true
			unschedulableSince = condition.LastTransitionTime.Time
		}
	}

	if !unschedulable {
		return nil // cleanin up only unschedulable
	}

	if pod.Spec.NodeName != "" {
		return nil // cleaning only such that scheduled by node affinity
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "CleanupIfNeeded")
	defer end()

	_, err := kubeService.GetNode(ctx, types.NodeName(pod.Spec.NodeName))
	if !apierrors.IsNotFound(err) {
		return nil // node still exists, handling only not found node
	}
	panic("here")

	// We are safe to delete clients after a configurable while
	// TODO: Make configurable, for now we delete after 5 minutes since downtime
	// relying onlastTransitionTime of Unschedulable condition
	rescheduleAfter := 30 * time.Second
	if time.Since(unschedulableSince) > rescheduleAfter {
		logger.Info("Deleting unschedulable pod")
		err := r.Delete(ctx, container)
		if err != nil {
			logger.Error(err, "Error deleting client container")
			return err
		}
		return errors.New("Pod is outdated and will be deleted")
	}
	return nil
}

func (r *containerReconcilerLoop) WaitForRunning(ctx context.Context) error {
	pod := r.pod

	if pod.Status.Phase == v1.PodRunning {
		return nil
	}

	return lifecycle.NewWaitError(errors.New("Pod is not running"))

}

func (r *containerReconcilerLoop) cleanupFinished(ctx context.Context) error {
	pod := r.pod

	if pod.Status.Phase != v1.PodSucceeded {
		// should we also check for Failed status?
		return nil
	}

	err := r.ensureNoPod(ctx)
	if err != nil {
		return err
	}

	return lifecycle.NewWaitError(errors.New("Pod is finished and will be deleted"))
}

func (r *containerReconcilerLoop) resetEnsureDriversForNewPod(ctx context.Context) error {
	container := r.container
	pod := r.pod
	// reset drivers condition if pod started after last transition time. Basically every restart will cause re-go after this
	// and considering it is every restart, we might/should for simpler solution here and recognize re-build at more appropriate place
	// another option, where this code suits better - fetch node status and compare its uptime to when condition was set
	if meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondEnsureDrivers) {
		for _, containerStatus := range pod.Status.ContainerStatuses {
			if containerStatus.Name == "weka-container" && containerStatus.State.Running != nil {
				for _, cond := range container.Status.Conditions {
					if cond.Type == condition.CondEnsureDrivers {
						if containerStatus.State.Running.StartedAt.After(cond.LastTransitionTime.Time) {
							meta.SetStatusCondition(&container.Status.Conditions, metav1.Condition{
								Type:   condition.CondEnsureDrivers,
								Status: metav1.ConditionUnknown, Reason: "Reset", Message: "Drivers are not ensured",
							})
							err := r.Status().Update(ctx, container)
							if err != nil {
								return lifecycle.NewWaitError(err)
							}
						}
					}
				}
			}
		}
	}
	return nil
}

func (r *containerReconcilerLoop) EnsureDrivers(ctx context.Context) error {
	container := r.container
	err := r.reconcileDriversStatus(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "No such file or directory") {
			return lifecycle.NewWaitError(err)
		}
		if strings.Contains(err.Error(), "Drivers not loaded") {
			return lifecycle.NewWaitError(err)
		}
		return err
	}
	meta.SetStatusCondition(&container.Status.Conditions, metav1.Condition{
		Type:   condition.CondEnsureDrivers,
		Status: metav1.ConditionTrue, Reason: "Success", Message: "Drivers are ensured",
	})
	err = r.Status().Update(ctx, container)
	if err != nil {
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) reconcileDriversStatus(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "reconcileDriversStatus")
	defer end()

	container := r.container
	pod := r.pod

	if container.IsServiceContainer() {
		return nil
	}

	executor, err := util.NewExecInPod(pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return err
	}
	stdout, stderr, err := executor.ExecNamed(ctx, "CheckDriversLoaded", []string{"bash", "-ce", "cat /tmp/weka-drivers.log"})
	if err != nil {
		return errors.Wrap(err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		logger.InfoWithStatus(codes.Ok, "Drivers already loaded")
		return nil
	}

	if container.Spec.DriversDistService != "" {
		logger.Info("Drivers not loaded, ensuring drivers dist service")
		err2 := r.ensureDriversLoader(ctx)
		if err2 != nil {
			r.Logger.Error(err2, "Error ensuring drivers loader", "container", container)
		}
	}

	return errors.New("Drivers not loaded")
}

// Refactor drivers handling as operation
func (r *containerReconcilerLoop) ensureDriversLoader(ctx context.Context) error {
	container := r.container
	pod := r.pod
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureDriversLoader", "container", container.Name)
	defer end()

	// namespace := pod.Namespace
	namespace, err := util.GetPodNamespace()
	if err != nil {
		logger.Error(err, "GetPodNamespace")
		return err
	}
	serviceAccountName := os.Getenv("WEKA_OPERATOR_MAINTENANCE_SA_NAME")
	if serviceAccountName == "" {
		return fmt.Errorf("cannot create driver loader container, WEKA_OPERATOR_MAINTENANCE_SA_NAME is not defined")
	}
	name, err := r.getDriverLoaderName(ctx, container)
	if err != nil {
		logger.Error(err, "error naming driver loader pod")
		return err
	}
	loaderContainer := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: weka.WekaContainerSpec{
			Image:               container.Spec.Image,
			Mode:                weka.WekaContainerModeDriversLoader,
			ImagePullSecret:     container.Spec.ImagePullSecret,
			Hugepages:           0,
			NodeAffinity:        container.GetNodeAffinity(),
			DriversDistService:  container.Spec.DriversDistService,
			TracesConfiguration: container.Spec.TracesConfiguration,
			Tolerations:         container.Spec.Tolerations,
			ServiceAccountName:  serviceAccountName,
		},
	}

	found := &weka.WekaContainer{}
	err = r.Get(ctx, client.ObjectKey{Name: loaderContainer.Name, Namespace: loaderContainer.ObjectMeta.Namespace}, found)
	l := logger.WithValues("container", loaderContainer.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			l.Info("Creating drivers loader pod", "node_name", pod.Spec.NodeName, "namespace", loaderContainer.Namespace)
			err = r.Create(ctx, loaderContainer)
			if err != nil {
				l.Error(err, "Error creating drivers loader pod")
				return err
			}
		}
	}
	if found != nil {
		// logger.InfoWithStatus(codes.Ok, "Drivers loader pod already exists")
		// Could be debug? we dont have good debug right now, and this one is spamming
		return nil // TODO: Update handling?
	}
	// Should we have an owner? Or should we just delete it once done? We cant have owner in different namespace
	// It would be convenient, if container would just exit.
	// Maybe, we should just replace this with completely different entry point and consolidate everything under single script
	// Agent does us no good. Container that runs on-time and just finished and removed afterwards would be simpler
	loaderContainer.Status.Status = "Active"
	if err := r.Status().Update(ctx, loaderContainer); err != nil {
		l.Error(err, "Failed to update status of container")
		return err

	}
	return nil
}

func (r *containerReconcilerLoop) getDriverLoaderName(ctx context.Context, container *weka.WekaContainer) (string, error) {
	node := container.GetNodeAffinity()
	if node == "" {
		return "", errors.New("node affinity not set")
	}
	name := fmt.Sprintf("weka-drivers-loader-%s", node)
	if len(name) <= 63 {
		return name, nil
	}

	nodeObj := &v1.Node{}
	err := r.Get(ctx, client.ObjectKey{Name: string(node)}, nodeObj)
	if err != nil {
		return "", errors.Wrap(err, "failed to get node")
	}
	if nodeObj == nil {
		return "", errors.New("node not found")
	}

	name = fmt.Sprintf("weka-drivers-loader-%s", nodeObj.UID)
	if len(name) > 63 {
		return "", errors.New("driver loader pod name too long")
	}
	return name, nil
}

func (r *containerReconcilerLoop) DriverLoaderFinished(ctx context.Context) error {
	actualPod := r.pod
	container := r.container
	err := r.checkIfLoaderFinished(ctx, actualPod)
	if err != nil {
		return lifecycle.NewWaitError(err)
	} else {
		// if drivers loaded we can delete this weka container
		return r.Delete(ctx, container)
	}
}

func (r *containerReconcilerLoop) checkIfLoaderFinished(ctx context.Context, pod *v1.Pod) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "checkIfLoaderFinished")
	defer end()

	logger.Info("Checking if loader finished")

	executor, err := util.NewExecInPod(pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return err
	}
	cmd := "cat /tmp/weka-drivers-loader"
	stdout, stderr, err := executor.ExecNamed(ctx, "CheckDriversLoaded", []string{"bash", "-ce", cmd})
	if err != nil {
		if strings.Contains(stderr.String(), "No such file or directory") {
			return errors.New("Loader not finished")
		}
		logger.Error(err, "Error checking if loader finished", "stderr", stderr.String)
		return err
	}
	if strings.TrimSpace(stdout.String()) == "drivers_loaded" {
		logger.InfoWithStatus(codes.Ok, "Loader finished")
		return nil
	}
	logger.InfoWithStatus(codes.Error, "Loader not finished")
	return errors.New(fmt.Sprintf("Loader not finished, unknown status %s", stdout.String()))
}

func (r *containerReconcilerLoop) fetchResults(ctx context.Context) error {
	container := r.container
	if !container.IsOneOff() {
		return nil
	}

	if container.Status.ExecutionResult != nil {
		return nil
	}

	executor, err := r.ExecService.GetExecutor(ctx, container)
	if err != nil {
		return nil
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "FetchResults", []string{"cat", "/weka-runtime/results.json"})
	if err != nil {
		return errors.New(fmt.Sprintf("Error fetching results, stderr: %s", stderr.String()))
	}

	// update container to set execution result on container object
	result := string(stdout.Bytes())
	container.Status.ExecutionResult = &result
	err = r.Status().Update(ctx, container)
	if err != nil {
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) EnsureDrives(ctx context.Context) error {
	container := r.container
	pod := r.pod
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureDrives", "cluster_guid", container.Status.ClusterID, "container_id", container.Status.ClusterID)
	defer end()

	executor, err := util.NewExecInPod(pod)
	if err != nil {
		return err
	}

	wekaService := services.NewWekaService(r.ExecService, container)

	driveListoptions := services.DriveListOptions{
		ContainerId: container.Status.ClusterContainerID,
	}
	numAdded, err := wekaService.ListDrives(ctx, driveListoptions)
	if err != nil {
		return err
	}
	if len(numAdded) == container.Spec.NumDrives {
		logger.InfoWithStatus(codes.Ok, "All drives are already added")
		return nil
	}

	allowedMisses := r.container.Spec.NumDrives - len(numAdded)

	kDrives, err := r.getKernelDrives(ctx, executor)
	if err != nil {
		return err
	}
	// TODO: Not validating part of added drives and trying all over
	for _, drive := range container.Status.Allocations.Drives {
		//for driveCursor < len(container.Spec.RemovePotentialDrives) {
		l := logger.WithValues("drive_name", drive)
		l.Info("Attempting to configure drive")

		if _, ok := kDrives[drive]; !ok {
			allowedMisses--
			if allowedMisses < 0 {
				return errors.New("Not enough drives found")
			}
		}

		l.Info("Verifying drive signature")
		cmd := fmt.Sprintf("hexdump -v -e '1/1 \"%%.2x\"' -s 8 -n 16 %s", kDrives[drive].Partition)
		stdout, stderr, err := executor.ExecNamed(ctx, "GetPartitionSignature", []string{"bash", "-ce", cmd})
		if err != nil {
			// Force resign was here and needs new place. probably under wekaContainer delete, or tomsbtone delete, or claim delete
			return nil
		}

		if stdout.String() != "90f0090f90f0090f90f0090f90f0090f" {
			l.Info("Drive has Weka signature on it, verifying ownership")
			exists, err := r.isExistingCluster(ctx, stdout.String())
			if err != nil {
				return err
			}
			if exists {
				return errors.New("Drive belongs to existing cluster")
			} else {
				l.WithValues("another_cluster_guid", stdout.String()).Info("Drive belongs to non-existing cluster, resigning")
				err2 := r.claimDrive(ctx, container, executor, kDrives[drive].Partition)
				if err2 != nil {
					return err2
				}
				err2 = r.forceResignDrive(ctx, executor, kDrives[drive].DevicePath) // This changes UUID, effectively making claim obsolete
				if err2 != nil {
					return err2
				}
			}
		}

		l.Info("Claiming drive")

		err = r.claimDrive(ctx, container, executor, kDrives[drive].Partition)
		if err != nil {
			return err
		}
		l.Info("Adding drive into system")
		// TODO: We need to login here. Maybe handle it on wekaauthcli level?
		cmd = fmt.Sprintf("weka cluster drive add %d %s", *container.Status.ClusterContainerID, kDrives[drive].DevicePath)
		_, stderr, err = executor.ExecNamed(ctx, "WekaClusterDriveAdd", []string{"bash", "-ce", cmd})
		if err != nil {
			if !strings.Contains(stderr.String(), "Device is already in use") {
				l.WithValues("stderr", stderr.String(), "command", cmd).Error(err, "Error adding drive into system")
				return errors.Wrap(err, stderr.String())
			} else {
				l.Info("Drive already added into system")
			}
		} else {
			l.Info("Drive added into system", "drive", drive)
		}
	}
	logger.InfoWithStatus(codes.Ok, "Drives added")
	return nil
}

func (r *containerReconcilerLoop) getKernelDrives(ctx context.Context, executor util.Exec) (map[string]operations.DriveInfo, error) {
	stdout, _, err := executor.ExecNamed(ctx, "FetchKernelDrives",
		[]string{"bash", "-ce", "cat /opt/weka/k8s-runtime/drives.json"})
	if err != nil {
		return nil, err
	}
	var drives []operations.DriveInfo
	err = json.Unmarshal(stdout.Bytes(), &drives)
	if err != nil {
		return nil, err
	}
	serialIdMap := make(map[string]operations.DriveInfo)
	for _, drive := range drives {
		serialIdMap[drive.SerialId] = drive
	}

	return serialIdMap, nil
}

func (r *containerReconcilerLoop) initialDriveSign(ctx context.Context, executor util.Exec, drive string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "initialDriveSign")
	defer end()

	logger.Info("Signing cloud drive", "drive", drive)
	cmd := fmt.Sprintf("weka local exec -- /weka/tools/weka_sign_drive %s", drive) // no-force and claims should keep us safe
	_, stderr, err := executor.ExecNamed(ctx, "WekaSignDrive", []string{"bash", "-ce", cmd})
	if err != nil {
		logger.Error(err, "Error pre-signing drive", "drive", drive, "stderr", stderr.String())
		return err
	}
	logger.InfoWithStatus(codes.Ok, "Cloud drives signed")
	return nil
}

func (r *containerReconcilerLoop) getDriveUUID(ctx context.Context, executor util.Exec, drive string) (string, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getDriveUUID")
	defer end()

	cmd := fmt.Sprintf("blkid -o value -s PARTUUID %s", drive)
	stdout, stderr, err := executor.ExecNamed(ctx, "GetDriveUUID", []string{"bash", "-ce", cmd})
	if err != nil {
		logger.WithValues("stderr", stderr.String()).Error(err, "Error getting drive UUID")
		return "", errors.Wrap(err, stderr.String())
	}
	serial := strings.TrimSpace(stdout.String())
	if serial == "" {
		logger.Error(err, "UUID not found for drive")
		return "", errors.New("uuid not found")
	}
	logger.InfoWithStatus(codes.Ok, "UUID found for drive")
	return serial, nil
}

func (r *containerReconcilerLoop) isDrivePresigned(ctx context.Context, executor util.Exec, drive string) (bool, error) {
	stdout, stderr, err := executor.ExecNamed(ctx, "CheckDriveIsPresigned", []string{"bash", "-ce", "blkid -s PART_ENTRY_TYPE -o value -p " + getSignatureDevice(drive)})
	if err != nil {
		r.Logger.Error(err, "Error checking if drive is presigned", "drive", drive, "stderr", stderr.String(), "stdout", stdout.String())
		return false, errors.Wrap(err, stderr.String())
	}
	const WEKA_SIGNATURE = "993ec906-b4e2-11e7-a205-a0a8cd3ea1de"
	return strings.TrimSpace(stdout.String()) == WEKA_SIGNATURE, nil
}

func (r *containerReconcilerLoop) claimDrive(ctx context.Context, container *weka.WekaContainer, executor util.Exec, drive string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "claimDrive", "drive", drive)
	defer end()

	logger.Info("Claiming drive")
	driveUuid, err := r.getDriveUUID(ctx, executor, drive)
	if err != nil {
		logger.Error(err, "Error getting drive UUID")
		return err
	}
	logger.SetValues("drive_uuid", driveUuid)
	logger.Info("Claimed drive by UUID")

	claim := weka.DriveClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: container.Namespace,
			Name:      fmt.Sprintf("%s", driveUuid),
		},
		Spec: weka.DriveClaimSpec{
			DriveUuid: driveUuid,
			Device:    drive,
		},
		Status: weka.DriveClaimStatus{},
	}

	err = ctrl.SetControllerReference(container, &claim, r.Scheme)
	if err != nil {
		logger.SetError(err, "Error setting owner reference")
		return err
	}
	logger.Info("Drive was set with owner, creating drive claim")

	err = r.Create(ctx, &claim)
	if err != nil {
		// get eixsting
		existingClaim := weka.DriveClaim{}
		err = r.Get(ctx, client.ObjectKey{Namespace: container.Namespace, Name: fmt.Sprintf("%s", driveUuid)}, &existingClaim)
		if err != nil {
			logger.SetError(err, "Error getting existing claim")
			return err
		}
		if existingClaim.OwnerReferences[0].UID != container.UID {
			err = errors.New("drive already claimed by another container")
			logger.SetError(err, "drive already claimed")
			return err
		}
		return nil
	}
	return nil
}

func getSignatureDevice(drive string) string {
	driveSignTarget := fmt.Sprintf("%s1", drive)
	if strings.Contains(drive, "/dev/disk/by-path/pci-") {
		return fmt.Sprintf("%s-part1", drive)
	}
	if strings.Contains(drive, "nvme") {
		return fmt.Sprintf("%sp1", drive)
	}
	return driveSignTarget
}

func (r *containerReconcilerLoop) forceResignDrive(ctx context.Context, executor util.Exec, drive string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "forceResignDrive")
	defer end()

	logger.Info("Resigning drive with --force")
	cmd := fmt.Sprintf("weka local exec -- /weka/tools/weka_sign_drive --force %s", drive)
	_, stderr, err := executor.ExecNamed(ctx, "WekaSignDrive", []string{"bash", "-ce", cmd})
	if err != nil {
		r.Logger.Error(err, "Error signing drive", "drive", drive, "stderr", stderr.String())
	}
	return err
}

func (r *containerReconcilerLoop) isExistingCluster(ctx context.Context, guid string) (bool, error) {
	// TODO: Query by status?
	// TODO: Cache?
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "isExistingCluster")
	defer end()

	logger.WithValues("cluster_guid", guid).Info("Verifying for existing cluster")
	clusterList := weka.WekaClusterList{}
	err := r.List(ctx, &clusterList)
	if err != nil {
		logger.Error(err, "Error listing clusters")
		return false, err
	}
	for _, cluster := range clusterList.Items {
		// strip `-` from saved cluster name
		stripped := strings.ReplaceAll(cluster.Status.ClusterID, "-", "")
		if stripped == guid {
			logger.InfoWithStatus(codes.Ok, "Cluster found")
			return true, nil
		}
	}
	logger.InfoWithStatus(codes.Ok, "Cluster not found")
	return false, nil
}

func (r *containerReconcilerLoop) JoinS3Cluster(ctx context.Context) error {
	wekaService := services.NewWekaService(r.ExecService, r.container)
	return wekaService.JoinS3Cluster(ctx, *r.container.Status.ClusterContainerID)
}

func (r *containerReconcilerLoop) handleImageUpdate(ctx context.Context) error {
	container := r.container
	pod := r.pod

	if container.Spec.Mode == "client" {
		// leaving client operation to user
		return nil
	}

	if container.Spec.Image != container.Status.LastAppliedImage {
		var wekaPodContainer v1.Container
		found := false
		for _, podContainer := range pod.Spec.Containers {
			if podContainer.Name == "weka-container" {
				wekaPodContainer = podContainer
				found = true
			}
		}
		if !found {
			return errors.New("Weka container not found in pod")
		}

		if wekaPodContainer.Image != container.Spec.Image {
			// delete pod
			err := r.Delete(ctx, pod)
			if err != nil {
				return err
			}
			return nil
		}

		if pod.GetDeletionTimestamp() != nil {
			return errors.New("Podis being deleted, waiting")
		}

		if pod.Status.Phase != v1.PodRunning {
			return errors.New("Pod is not running yet")
		}

		container.Status.LastAppliedImage = container.Spec.Image
		return r.Status().Update(ctx, container)
	}
	return nil
}

func (r *containerReconcilerLoop) PodNotSet() bool {
	return r.pod == nil
}

func (r *containerReconcilerLoop) WriteResources(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "WriteResources")
	defer end()

	container := r.container
	if r.container.Status.Allocations == nil {
		return lifecycle.NewWaitError(errors.New("Allocations are not set yet"))
	}

	executor, err := r.ExecService.GetExecutor(ctx, container)
	if err != nil {
		return err
	}

	resourcesJson, err := json.Marshal(r.container.Status.Allocations)
	if err != nil {
		return err
	}
	//TODO: Safer way of writing? as might be corrupted bash string

	logger.Info("writing resources", "json", string(resourcesJson))
	stdout, stderr, err := executor.ExecNamed(ctx, "WriteResources", []string{"bash", "-ce", fmt.Sprintf(`
mkdir -p /opt/weka/k8s-runtime
echo '%s' > /opt/weka/k8s-runtime/resources.json
`, string(resourcesJson))})
	if err != nil {
		logger.Error(err, "Error writing resources", "stderr", stderr.String(), "stdout", stdout.String())
	}
	return err
}

func (r *containerReconcilerLoop) enforceNodeAffinity(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()
	node := r.pod.Spec.NodeName

	if node == "" {
		return lifecycle.NewWaitError(errors.New("Node is not set yet"))
	}

	if !(r.container.IsAllocatable() && r.container.IsBackend()) {
		logger.Info("not binding container to node, as it is ephemeral", "container_name", r.container.Name)
		return nil // no need to enforce FDs/uniqueness
	}

	if !r.container.Spec.NoAffinityConstraints {
		lockname := fmt.Sprintf("%s-%s", node, r.container.Spec.Mode)
		lock := r.nodeAffinityLock.GetLock(lockname)
		lock.Lock()
		defer lock.Unlock()

		pods, err := r.KubeService.GetPods(ctx, r.container.GetNamespace(), node, r.container.GetLabels())
		if err != nil {
			return err
		}
		for _, pod := range pods {
			if pod.UID == r.pod.UID {
				continue // that's us, skipping
			}
			owner := pod.GetOwnerReferences()
			if len(owner) == 0 {
				continue // not owned pod, no idea what is it, but bypassing
			}

			var ownerContainer weka.WekaContainer
			err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: owner[0].Name}, &ownerContainer)
			if err != nil {
				return err
			}
			if ownerContainer.Status.NodeAffinity != "" {
				deleteErr := r.ensureNoPod(ctx)
				if deleteErr != nil {
					return deleteErr
				} else {
					return lifecycle.NewWaitError(errors.New("scheduling race, deleting current container"))
				}
			}
		}
		// no one else is using this node, we can safely set it
	}
	r.container.Status.NodeAffinity = weka.NodeName(node)
	logger.Info("binding to node", "node", node, "container_name", r.container.Name)
	return r.Status().Update(ctx, r.container)
}

func (r *containerReconcilerLoop) reconcileWekaLocalStatus(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container
	pod := r.pod

	executor, err := util.NewExecInPod(pod)
	if err != nil {
		return err
	}

	statusCommand := fmt.Sprintf("weka local ps -J")
	stdout, _, err := executor.ExecNamed(ctx, "WekaLocalPs", []string{"bash", "-ce", statusCommand})
	if err != nil {
		return err
	}
	response := []resources.WekaLocalPs{}
	err = json.Unmarshal(stdout.Bytes(), &response)
	if err != nil {
		return err
	}

	if len(response) == 0 {
		err := errors.New("Expected at least one container to be present, none found")
		return err
	}

	found := false
	for _, c := range response {
		if c.Name == container.Spec.WekaContainerName {
			found = true
			break
		}
	}

	if !found {
		err := errors.New("weka container not found")
		return err
	}

	status := response[0].RunStatus
	if container.Status.Status != status {
		logger.Info("Updating status", "from", container.Status.Status, "to", status)
		container.Status.Status = status
		if err := r.Status().Update(ctx, container); err != nil {
			return err
		}
		logger.WithValues("status", status).Info("Status updated")
		return nil
	}
	return nil
}

func (r *containerReconcilerLoop) deleteIfNoNode(ctx context.Context) error {
	affinity := r.container.GetNodeAffinity()
	if affinity != "" {

		node, err := r.KubeService.GetNode(ctx, types.NodeName(affinity))
		if err != nil {
			if apierrors.IsNotFound(err) {
				deleteError := r.Client.Delete(ctx, r.container)
				if deleteError != nil {
					return deleteError
				}
				return lifecycle.NewWaitError(errors.New("Node is not found, deleting container"))
			}
		}
		r.node = node
	}
	return nil
}
