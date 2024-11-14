package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	allocator2 "github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/resources"
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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	PodStatePodNotRunning = "PodNotRunning"
	PodStatePodRunning    = "PodRunning"
	WaitForDrivers        = "WaitForDrivers"
	// for drivers-build container
	Completed = "Completed"
	Building  = "Building"
)

func NewContainerReconcileLoop(mgr ctrl.Manager) *containerReconcilerLoop {
	//TODO: We creating new client on every loop, we should reuse from reconciler, i.e pass it by reference
	config := mgr.GetConfig()
	kClient := mgr.GetClient()
	execService := exec.NewExecService(config)
	metricsService, err := kubernetes.NewKubeMetricsServiceFromManager(mgr)
	if err != nil {
		mgr.GetLogger().Error(err, "Failed to create metrics service")
	}
	return &containerReconcilerLoop{
		Client:         kClient,
		Scheme:         mgr.GetScheme(),
		KubeService:    kubernetes.NewKubeService(mgr.GetClient()),
		MetricsService: metricsService,
		ExecService:    execService,
		Manager:        mgr,
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
	KubeService      kubernetes.KubeService
	ExecService      exec.ExecService
	Manager          ctrl.Manager
	container        *weka.WekaContainer
	pod              *v1.Pod
	nodeAffinityLock LockMap
	node             *v1.Node
	MetricsService   kubernetes.KubeMetricsService
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
			{
				Run: loop.handleStatePaused,
				Predicates: lifecycle.Predicates{
					container.IsPaused,
				},
				ContinueOnPredicatesFalse: true,
				FinishOnSuccess:           true,
			},
			{Run: loop.initState},
			{Run: loop.deleteIfNoNode},
			{Run: loop.ensureFinalizer},
			{Run: loop.ensureBootConfigMapInTargetNamespace},
			{Run: loop.refreshPod},
			{
				Run: loop.Noop,
				Predicates: lifecycle.Predicates{
					loop.container.IsOneOff,
					loop.ResultsAreProcessed,
				},
				FinishOnSuccess:           true,
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.ensurePod,
				Predicates: lifecycle.Predicates{
					loop.PodNotSet,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.ensurePodNotRunningState,
				Predicates: lifecycle.Predicates{
					loop.PodNotRunning,
				},
				ContinueOnPredicatesFalse: true,
			},
			{Run: loop.GetNode},
			{
				Run:       loop.enforceNodeAffinity,
				Condition: condition.CondContainerAffinitySet,
				Predicates: lifecycle.Predicates{
					func() bool {
						return loop.container.IsAllocatable() && loop.container.IsBackend() || loop.container.IsEnvoy()
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
				Run: loop.updateStatusWaitForDrivers,
				Predicates: lifecycle.Predicates{
					container.RequiresDrivers,
					loop.CondEnsureDriversNotSet,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.updateDriversBuilderStatus,
				Predicates: lifecycle.Predicates{
					container.IsDriversBuilder,
					lifecycle.IsNotFunc(container.IsDistMode), // TODO: legacy "dist" mode is currently used both for building drivers and for distribution
					lifecycle.IsNotFunc(loop.ResultsAreProcessed),
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.updateAdhocOpStatus,
				Predicates: lifecycle.Predicates{
					container.IsAdhocOpContainer,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition: condition.CondEnsureDrivers,
				Run:       loop.EnsureDrivers,
				Predicates: lifecycle.Predicates{
					container.RequiresDrivers,
					// TODO: We are basing on condition, so entering operation per container
					// TODO: While we have multiple containers on the same host, so they are doing redundant work
					// If we base logic on what pod reports(not our logic here anymore) - it wont help, as all of them will report
					// Either we move this to global entity, like WekaNodeController(yayk), or just pay the price, for now paying the price of redundant gets
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition: condition.CondResultsReceived,
				Run:       loop.fetchResults,
				Predicates: lifecycle.Predicates{
					loop.container.IsOneOff,
				},
				ContinueOnPredicatesFalse: true,
				SkipOwnConditionCheck:     true,
			},
			{
				Condition: condition.CondResultsProcessed,
				Run:       loop.processResults,
				Predicates: lifecycle.Predicates{
					loop.container.IsOneOff,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.cleanupFinishedOneOff,
				Predicates: lifecycle.Predicates{
					loop.container.IsOneOff,
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
					container.ShouldJoinCluster,
				},
				ContinueOnPredicatesFalse: true,
				CondMessage:               "Container joined cluster",
			},
			{
				Run: loop.checkNodeAnnotation,
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
					func() bool {
						return container.Spec.Image != container.Status.LastAppliedImage
					},
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.ReportMetrics,
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

func (r *containerReconcilerLoop) handleStatePaused(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	newStatus := strings.ToUpper(string(weka.ContainerStatePaused))
	if r.container.Status.Status != newStatus {
		err := r.ensureNoPod(ctx)
		if err != nil {
			return err
		}

		logger.Info("Updating status", "from", r.container.Status.Status, "to", newStatus)
		r.container.Status.Status = newStatus

		if err := r.Status().Update(ctx, r.container); err != nil {
			err = errors.Wrap(err, "Failed to update status")
			return err
		}
	}

	logger.Info("Container is paused, skipping reconciliation")
	return nil
}

func (r *containerReconcilerLoop) initState(ctx context.Context) error {
	if r.container.Status.Conditions == nil {
		r.container.Status.Conditions = []metav1.Condition{}
	}

	changes := false
	if !r.container.DriversReady() && r.container.RequiresDrivers() {
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

// Implement the remaining methods (ensurePod, reconcileDriversStatus, reconcileManagementIP, etc.)
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
	if container.Spec.Network.EthDevice == "" && ipAddress == "" {
		//TODO: support ipv6 in this case
		if container.Spec.Ipv6 {
			return fmt.Errorf("failed to get management IP, no fallback for ipv6")
		}
		// Compatible with Amazon Linux 2
		getIpCmd = "ip -4 addr show dev $(ip route show default | awk '{print $5}') | grep inet | awk '{print $2}' | cut -d/ -f1"
		stdout, stderr, err = executor.ExecNamed(ctx, "GetManagementIpAddress", []string{"bash", "-ce", getIpCmd})
		if err != nil {
			logger.Error(err, "Error executing command", "stderr", stderr.String())
			return err
		}
		ipAddress = strings.TrimSpace(stdout.String())
	}
	if ipAddress == "" {
		return fmt.Errorf("failed to get management IP")
	}
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

	showAgentPortCmd := `cat /opt/weka/k8s-runtime/vars/agent_port`
	cmd := fmt.Sprintf("weka local run wapi -H localhost:$(%s)/jrpc -W container-get-identity --container-name %s --json", showAgentPortCmd, containerName)
	if container.Spec.JoinIps != nil {
		cmd = fmt.Sprintf("wekaauthcli local run wapi -H 127.0.0.1:$(%s)/jrpc -W container-get-identity --container-name %s --json", showAgentPortCmd, containerName)
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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
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
			Labels:    container.ObjectMeta.GetLabels(),
		},
		Spec: weka.TombstoneSpec{
			CrType:          "WekaContainer",
			CrId:            string(container.UID),
			NodeAffinity:    nodeAffinity,
			PersistencePath: nodeInfo.GetHostsideContainerPersistence(),
			ContainerName:   container.Name,
			Tolerations:     container.Spec.Tolerations,
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
		nodeAffinity = ""
		// if container has affinity, try to find node that satisfies it
		if r.container.Spec.Affinity != nil {
			for _, node := range nodes {
				if kubernetes.NodeSatisfiesAffinity(&node, r.container.Spec.Affinity) {
					nodeAffinity = weka.NodeName(node.Name)
					break
				}
			}
		} else {
			nodeAffinity = weka.NodeName(nodes[0].Name)
		}

		if nodeAffinity == "" {
			return nil, errors.New("no matching nodes found")
		}
	}
	discoverNodeOp := operations.NewDiscoverNodeOperation(
		r.Manager,
		nodeAffinity,
		container,
		container.ToContainerDetails(),
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
		_, _, err = executor.ExecNamed(ctx, "AllowForceStop", []string{"bash", "-ce", "touch /tmp/.allow-force-stop && kill 1"})
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

func (r *containerReconcilerLoop) updateStatusWaitForDrivers(ctx context.Context) error {
	if r.container.Status.Status != WaitForDrivers {
		r.container.Status.Status = WaitForDrivers
		err := r.Status().Update(ctx, r.container)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) checkNodeAnnotation(ctx context.Context) error {
	node := r.node
	container := r.container

	if node == nil {
		err := errors.New("Node not found")
		return err
	}

	if node.Annotations == nil || !operations.DriversLoaded(node, container.Spec.Image, container.HasFrontend()) {
		meta.SetStatusCondition(&container.Status.Conditions, metav1.Condition{
			Type:    condition.CondEnsureDrivers,
			Status:  metav1.ConditionFalse,
			Reason:  "NoNodeAnnotation",
			Message: "Node annotation not found or it has wrong value of driver version",
		})
		err := r.Status().Update(ctx, container)
		if err != nil {
			err = errors.Wrap(err, "cannot update container status with 'no node annotation'")
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) EnsureDrivers(ctx context.Context) error {
	details := r.container.ToContainerDetails()
	if r.container.Spec.DriversLoaderImage != "" {
		details.Image = r.container.Spec.DriversLoaderImage
	}

	driversLoader := operations.NewLoadDrivers(r.Manager, r.node, *details, r.container.Spec.DriversDistService, r.container.HasFrontend(), false)
	err := operations.ExecuteOperation(ctx, driversLoader)
	if err != nil {
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) reconcileDriversStatus(ctx context.Context) error {
	if r.container.IsClientContainer() || r.container.IsS3Container() {
		return nil
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "reconcileDriversStatus")
	defer end()

	pod := r.pod

	executor, err := util.NewExecInPod(pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return err
	}
	stdout, stderr, err := executor.ExecNamed(ctx, "CheckDriversLoaded", []string{"bash", "-ce", "cat /tmp/weka-drivers.log"})
	if err != nil {
		return errors.Wrap(err, stderr.String())
	}

	missingDriverName := strings.TrimSpace(stdout.String())

	if missingDriverName == "" {
		logger.InfoWithStatus(codes.Ok, "Drivers already loaded")
		return nil
	}

	return fmt.Errorf("driver %s is not loaded", missingDriverName)
}

func (r *containerReconcilerLoop) fetchResults(ctx context.Context) error {
	container := r.container

	if container.Status.ExecutionResult != nil {
		return nil
	}

	executor, err := r.ExecService.GetExecutor(ctx, container)
	if err != nil {
		return nil
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "FetchResults", []string{"cat", "/weka-runtime/results.json"})
	if err != nil {
		return fmt.Errorf("Error fetching results, stderr: %s", stderr.String())
	}

	result := stdout.String()
	if result == "" {
		return errors.New("Empty result")
	}

	// update container to set execution result on container object
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

		if kDrives[drive].Partition == "" {
			err := fmt.Errorf("drive %v is not partitioned", kDrives[drive])
			l.Error(err, "Error configuring drive")
			return err
		}

		l = l.WithValues("partition", kDrives[drive].Partition)
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
				err2 := r.forceResignDrive(ctx, executor, kDrives[drive].DevicePath) // This changes UUID, effectively making claim obsolete
				if err2 != nil {
					return err2
				}
			}
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

func (r *containerReconcilerLoop) isDrivePresigned(ctx context.Context, executor util.Exec, drive string) (bool, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	stdout, stderr, err := executor.ExecNamed(ctx, "CheckDriveIsPresigned", []string{"bash", "-ce", "blkid -s PART_ENTRY_TYPE -o value -p " + getSignatureDevice(drive)})
	if err != nil {
		logger.Error(err, "Error checking if drive is presigned", "drive", drive, "stderr", stderr.String(), "stdout", stdout.String())
		return false, errors.Wrap(err, stderr.String())
	}
	const WEKA_SIGNATURE = "993ec906-b4e2-11e7-a205-a0a8cd3ea1de"
	return strings.TrimSpace(stdout.String()) == WEKA_SIGNATURE, nil
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
		logger.Error(err, "Error signing drive", "drive", drive, "stderr", stderr.String())
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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "handleImageUpdate")
	defer end()

	container := r.container
	pod := r.pod

	upgradeType := container.Spec.UpgradePolicyType
	if upgradeType == "" {
		upgradeType = weka.UpgradePolicyTypeManual
	}

	if container.Spec.Image != container.Status.LastAppliedImage {
		var wekaPodContainer v1.Container
		wekaPodContainer, err := r.getWekaPodContainer(pod)
		if err != nil {
			return err
		}

		if wekaPodContainer.Image != container.Spec.Image {
			logger.Info("Deleting pod to apply new image")

			executor, err := util.NewExecInPod(pod)
			if err != nil {
				return err
			}
			if container.HasFrontend() {
				stdout, stderr, err := executor.ExecNamed(ctx, "PrepareForUpgrade", []string{"bash", "-ce", "echo prepare-upgrade > /proc/wekafs/interface"})
				if err != nil {
					logger.Error(err, "Error preparing for upgrade", "stderr", stderr.String(), "stdout", stdout.String())
					return err
				}
			}
			if container.Spec.Mode == "client" && container.Spec.UpgradePolicyType == weka.UpgradePolicyTypeManual {
				// leaving client delete operation to user and we will apply lastappliedimage if pod got restarted
				return nil
			}
			// delete pod
			err = r.Delete(ctx, pod)
			if err != nil {
				return err
			}

			return nil
		}

		if pod.GetDeletionTimestamp() != nil {
			logger.Info("Pod is being deleted, waiting")
			return errors.New("Pod is being deleted, waiting")
		}

		if pod.Status.Phase != v1.PodRunning {
			logger.Info("Pod is not running yet")
			return errors.New("Pod is not running yet")
		}

		if container.Status.Status != "Running" {
			logger.Info("Container is not running yet")
			return errors.New("Container is not running yet")
		}

		container.Status.LastAppliedImage = container.Spec.Image
		return r.Status().Update(ctx, container)
	}
	return nil
}

func (r *containerReconcilerLoop) getWekaPodContainer(pod *v1.Pod) (v1.Container, error) {
	for _, podContainer := range pod.Spec.Containers {
		if podContainer.Name == "weka-container" {
			return podContainer, nil
		}
	}
	return v1.Container{}, errors.New("Weka container not found in pod")
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

func (r *containerReconcilerLoop) handleWekaLocalPsResponse(ctx context.Context, stdout []byte, psErr error) (response []resources.WekaLocalPs, err error) {
	if psErr != nil {
		return nil, psErr
	}

	container := r.container

	err = json.Unmarshal(stdout, &response)
	if err != nil {
		return
	}

	if len(response) == 0 {
		err = errors.New("Expected at least one container to be present, none found")
		return
	}

	found := false
	for _, c := range response {
		if c.Name == container.Spec.WekaContainerName {
			found = true
			break
		} else if c.Name == "envoy" && container.IsEnvoy() {
			found = true
			break
		}
	}

	if !found {
		err = errors.New("weka container not found")
		return
	}
	return
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

	statusCommand := "weka local ps -J"
	stdout, stderr, err := executor.ExecNamed(ctx, "WekaLocalPs", []string{"bash", "-ce", statusCommand})

	response, err := r.handleWekaLocalPsResponse(ctx, stdout.Bytes(), err)
	if err != nil {
		forceReload := false
		details := r.container.ToContainerDetails()

		// check if drivers should be force-reloaded
		driversErr := r.reconcileDriversStatus(ctx)
		if driversErr != nil {
			// extend error message with original error
			driversErr = fmt.Errorf("weka local ps failed: %v, stderr: %s; drivers are not loaded: %v", err, stderr.String(), driversErr)
			logger.Error(driversErr, "Drivers are not loaded")

			forceReload = true
		}

		driversLoader := operations.NewLoadDrivers(r.Manager, r.node, *details, r.container.Spec.DriversDistService, r.container.HasFrontend(), forceReload)
		loaderErr := operations.ExecuteOperation(ctx, driversLoader)
		if loaderErr != nil {
			err := fmt.Errorf("drivers are not loaded: %v; %v", driversErr, loaderErr)
			return lifecycle.NewWaitError(err)
		}

		err = fmt.Errorf("weka local ps failed: %v, stderr: %s", err, stderr.String())
		return lifecycle.NewWaitError(err)
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
		_, err := r.KubeService.GetNode(ctx, types.NodeName(affinity))
		if err != nil {
			if apierrors.IsNotFound(err) {
				deleteError := r.Client.Delete(ctx, r.container)
				if deleteError != nil {
					return deleteError
				}
				return lifecycle.NewWaitError(errors.New("Node is not found, deleting container"))
			}
		}
	}
	return nil
}

func (r *containerReconcilerLoop) GetNode(ctx context.Context) error {
	if r.pod.Spec.NodeName == "" {
		return lifecycle.NewWaitError(errors.New("pod is not assigned to node"))
	}
	node, err := r.KubeService.GetNode(ctx, types.NodeName(r.pod.Spec.NodeName))
	if err != nil {
		return err
	}
	r.node = node
	return nil
}

func (r *containerReconcilerLoop) processResults(ctx context.Context) error {
	switch {
	case r.container.IsDriversBuilder():
		return r.UploadBuiltDrivers(ctx)
	default:
		return nil
	}
}

type BuiltDriversResult struct {
	WekaVersion           string `json:"weka_version"`
	KernelSignature       string `json:"kernel_signature"`
	WekaPackNotSupported  bool   `json:"weka_pack_not_supported"`
	NoWekaDriversHandling bool   `json:"no_weka_drivers_handling"`
	Err                   string `json:"err"`
}

func (r *containerReconcilerLoop) UploadBuiltDrivers(ctx context.Context) error {
	target := r.container.Spec.UploadResultsTo
	if target == "" {
		return errors.New("uploadResultsTo is not set")
	}

	targetDistcontainer := &weka.WekaContainer{}
	// assuming same namespace
	err := r.Get(ctx, client.ObjectKey{Name: target, Namespace: r.container.Namespace}, targetDistcontainer)
	if err != nil {
		return err
	}

	complete := func() error {
		r.container.Status.Status = Completed
		return r.Status().Update(ctx, r.container)
	}

	// TODO: This is not a best solution, to download version, but, usable.
	// Should replace this with ad-hocy downloader container, that will use newer version(as the one who built), to download using shared storage

	executor, err := r.ExecService.GetExecutor(ctx, targetDistcontainer)
	if err != nil {
		return err
	}

	builderIp := r.pod.Status.PodIP
	builderPort := r.container.GetPort()

	if builderIp == "" {
		return errors.New("Builder IP is not set")
	}

	results := &BuiltDriversResult{}
	err = json.Unmarshal([]byte(*r.container.Status.ExecutionResult), results)
	if err != nil {
		return err
	}

	if results.NoWekaDriversHandling {
		// for legacy drivers handling, we don't have support for weka driver command
		// copy everything from builer's /opt/weka/dist/drivers to targetDistcontainer's /opt/weka/dist/drivers
		cmd := fmt.Sprintf("cd /opt/weka/dist/drivers/ && wget -r -nH --cut-dirs=3 --no-parent --reject=\"index.html*\" http://%s:%d/dist/v1/drivers/", builderIp, builderPort)
		stdout, stderr, err := executor.ExecNamed(ctx, "CopyDrivers",
			[]string{"bash", "-ce", cmd},
		)
		if err != nil {
			err := fmt.Errorf("failed to run command: %s, error: %s, stdout: %s, stderr: %s", cmd, err, stdout.String(), stderr.String())
			return err
		}
		return complete()
	}

	endpoint := fmt.Sprintf("https://%s:%d", builderIp, builderPort)

	// if weka pack is not supported, we don't need to download it
	if !results.WekaPackNotSupported {
		stdout, stderr, err := executor.ExecNamed(ctx, "DownloadVersion",
			[]string{"bash", "-ce",
				"weka version get --driver-only " + results.WekaVersion + " --from " + endpoint,
			},
		)
		if err != nil {
			return errors.Wrap(err, stderr.String()+stdout.String())
		}
	}

	downloadCmd := "weka driver download --without-agent --version " + results.WekaVersion + " --from " + endpoint
	if !results.WekaPackNotSupported {
		downloadCmd += " --kernel-signature " + results.KernelSignature
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "DownloadDrivers",
		[]string{"bash", "-ce", downloadCmd},
	)
	if err != nil {
		return errors.Wrap(err, stderr.String()+stdout.String())
	}

	if results.WekaPackNotSupported {
		url := fmt.Sprintf("%s/dist/v1/drivers/%s-%s.tar.gz.sha256", endpoint, results.WekaVersion, results.KernelSignature)
		cmd := "cd /opt/weka/dist/drivers/ && curl -kO " + url
		stdout, stderr, err = executor.ExecNamed(ctx, "Copy sha256 file",
			[]string{"bash", "-ce", cmd},
		)
		if err != nil {
			err := fmt.Errorf("failed to run command: %s, error: %s, stdout: %s, stderr: %s", cmd, err, stdout.String(), stderr.String())
			return err
		}
	}

	return complete()
}

func (r *containerReconcilerLoop) Noop(ctx context.Context) error {
	return nil
}

func (r *containerReconcilerLoop) ResultsAreSet() bool {
	return r.container.Status.ExecutionResult != nil
}

func (r *containerReconcilerLoop) ResultsAreProcessed() bool {
	for _, c := range r.container.Status.Conditions {
		if c.Type == condition.CondResultsProcessed && c.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *containerReconcilerLoop) cleanupFinishedOneOff(ctx context.Context) error {
	if r.container.IsDriversBuilder() {
		if r.pod != nil {
			return r.Client.Delete(ctx, r.pod)
		}
	}
	return nil
}

func (r *containerReconcilerLoop) updateDriversBuilderStatus(ctx context.Context) error {
	if r.container.Status.Status != Building {
		r.container.Status.Status = Building
		err := r.Status().Update(ctx, r.container)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) IsNotAlignedImage() bool {
	return r.container.Status.LastAppliedImage != r.container.Spec.Image
}

func (r *containerReconcilerLoop) IsManualUpgradeMode() bool {
	return r.container.Spec.UpgradePolicyType == weka.UpgradePolicyTypeManual
}

func (r *containerReconcilerLoop) ensurePodNotRunningState(ctx context.Context) error {
	if r.container.Status.Status != PodStatePodNotRunning {
		r.container.Status.Status = PodStatePodNotRunning
		err := r.Status().Update(ctx, r.container)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) updateAdhocOpStatus(ctx context.Context) error {
	if r.pod.Status.Phase == v1.PodRunning && r.container.Status.Status != PodStatePodRunning {
		r.container.Status.Status = PodStatePodRunning
		err := r.Status().Update(ctx, r.container)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) PodNotRunning() bool {
	return r.pod.Status.Phase != v1.PodRunning
}

func (r *containerReconcilerLoop) CondEnsureDriversNotSet() bool {
	return !meta.IsStatusConditionTrue(r.container.Status.Conditions, condition.CondEnsureDrivers)
}

func (r *containerReconcilerLoop) ReportMetrics(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "MetricsData", "pod_name", r.container.Name, "namespace", r.container.Namespace, "mode", r.container.Spec.Mode)
	defer end()

	if r.MetricsService == nil {
		logger.Warn("Metrics service is not set")
		return nil
	}

	metrics, err := r.MetricsService.GetPodMetrics(ctx, r.pod)
	if err != nil {
		logger.Warn("Error getting pod metrics", "error", err)
		return nil // we ignore error, as this is not mandatory functionality
	}
	logger.SetAttributes(
		attribute.Float64("cpu_usage", metrics.CpuUsage),
		attribute.Int64("memory_usage", metrics.MemoryUsage),
		attribute.Float64("cpu_request", metrics.CpuRequest),
		attribute.Int64("memory_request", metrics.MemoryRequest),
		attribute.Float64("cpu_limit", metrics.CpuLimit),
		attribute.Int64("memory_limit", metrics.MemoryLimit),
	)

	return nil
}
