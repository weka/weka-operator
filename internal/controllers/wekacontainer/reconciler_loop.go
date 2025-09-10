package wekacontainer

import (
	"context"
	"sync"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

func NewContainerReconcileLoop(r *ContainerController, restClient rest.Interface) *containerReconcilerLoop {
	//TODO: We creating new client on every loop, we should reuse from reconciler, i.e pass it by reference
	mgr := r.Manager
	config := mgr.GetConfig()
	kClient := mgr.GetClient()
	execService := exec.NewExecService(restClient, config)
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
		Recorder:       mgr.GetEventRecorderFor("wekaContainer-controller"),
		RestClient:     restClient,
		ThrottlingMap:  r.ThrottlingMap,
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
	RestClient       rest.Interface
	Scheme           *runtime.Scheme
	KubeService      kubernetes.KubeService
	ExecService      exec.ExecService
	Recorder         record.EventRecorder
	Manager          ctrl.Manager
	container        *weka.WekaContainer
	pod              *v1.Pod
	nodeAffinityLock LockMap
	node             *v1.Node
	wekaClient       *weka.WekaClient
	targetCluster    *weka.WekaCluster
	MetricsService   kubernetes.KubeMetricsService
	// field used in cases when we can assume current container
	// is in deletion process or its node is not available
	clusterContainers []*weka.WekaContainer
	// values shared between steps
	activeMounts  *int
	ThrottlingMap throttling.Throttler
	cluster       *weka.WekaCluster
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

func ContainerReconcileSteps(r *ContainerController, container *weka.WekaContainer) lifecycle.StepsEngine {
	restClient := r.RestClient

	loop := NewContainerReconcileLoop(r, restClient)
	loop.container = container

	k8sObject := &lifecycle.K8sObject{
		Client:     loop.Client,
		Object:     loop.container,
		Conditions: &loop.container.Status.Conditions,
	}

	if container.Spec.Mode == weka.WekaContainerModeNodeAgent {
		return lifecycle.StepsEngine{
			StateKeeper: k8sObject,
			Throttler:   r.ThrottlingMap.WithPartition("container/" + loop.container.Name),
			Steps:       NodeAgentFlow(loop),
		}
	}

	// Choose the appropriate steps based on container state
	var steps []lifecycle.Step
	switch container.Spec.State {
	case weka.ContainerStatePaused:
		steps = PausedStateFlow(loop)
	case weka.ContainerStateDestroying:
		steps = DestroyingStateFlow(loop)
	case weka.ContainerStateDeleting:
		steps = DeletingStateFlow(loop)
	default: // ContainerStateActive is the default
		steps = ActiveStateFlow(loop)
	}

	return lifecycle.StepsEngine{
		StateKeeper: k8sObject,
		Throttler:   r.ThrottlingMap.WithPartition("container/" + loop.container.Name),
		Steps:       steps,
	}
}
