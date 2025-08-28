package wekacluster

import (
	"context"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"go.opentelemetry.io/otel/trace"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/pkg/util"
)

const (
	WekaFinalizer     = "weka.weka.io/finalizer"
	ClusterStatusInit = "Init"
)

// WekaClusterReconciler reconciles a WekaCluster object
type WekaClusterReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Manager    ctrl.Manager
	RestClient rest.Interface

	SecretsService services.SecretsService

	DetectedZombies map[util.NamespacedObject]time.Time
	ThrottlingMap   throttling.Throttler
}

func NewWekaClusterController(mgr ctrl.Manager, restClient rest.Interface) *WekaClusterReconciler {
	client := mgr.GetClient()
	config := mgr.GetConfig()
	scheme := mgr.GetScheme()
	execService := exec.NewExecService(restClient, config)

	ret := &WekaClusterReconciler{
		Client:         client,
		Scheme:         scheme,
		Manager:        mgr,
		RestClient:     restClient,
		SecretsService: services.NewSecretsService(client, scheme, execService),
		ThrottlingMap:  throttling.NewSyncMapThrottler(),
	}
	return ret
}

func (r *WekaClusterReconciler) RunGC(ctx context.Context) {
	go r.GCLoop(ctx)
}

func (r *WekaClusterReconciler) Reconcile(initContext context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(initContext, "WekaClusterReconcile", "namespace", req.Namespace, "cluster_name", req.Name)
	defer end()

	ctx, cancel := context.WithTimeout(ctx, config.Config.Timeouts.ReconcileTimeout)
	defer cancel()

	loop := NewWekaClusterReconcileLoop(r)

	err := loop.FetchCluster(ctx, req)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Cluster not found, ignoring reconcile")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// NOTE: throttling map should be initialized before we define ReconciliationSteps,
	// otherwise the throttling will not work as expected
	if loop.cluster.Status.Timestamps == nil {
		loop.cluster.Status.Timestamps = make(map[string]v1.Time)
	}

	//// TODO: This seems buggy, lots of lost spans, dropped for now, needs re-visit
	//ctx, err = r.GetProvisionContext(ctx, loop.cluster)
	//if err != nil {
	//	logger.SetError(err, "Failed to get shared cluster context")
	//	return ctrl.Result{}, err
	//}

	ctx, logger, end = instrumentation.GetLogSpan(ctx, "WekaClusterReconcileLoop", "cluster_uid", string(loop.cluster.GetUID()))
	defer end()

	logger.Info("Reconciling WekaCluster")
	defer logger.Info("Reconciliation of WekaCluster finished")

	k8sObject := &lifecycle.K8sObject{
		Client:     r.Client,
		Object:     loop.cluster,
		Conditions: &loop.cluster.Status.Conditions,
	}
	stepsEngine := lifecycle.StepsEngine{
		Object:    k8sObject,
		Throttler: r.ThrottlingMap.WithPartition("wekacluster/" + string(loop.cluster.GetUID())),
		Steps: []lifecycle.Step{
			&lifecycle.SingleStep{
				Run: loop.getCurrentContainers,
			},
			&lifecycle.SingleStep{
				//TODO: A better place? A new mode to allow continuing to run on failures? Ideally we want to have this async
				Run:  lifecycle.ForceNoError(loop.UpdateContainersCounters),
				Name: "UpdateContainersCounters", // explicit for throttling
				Predicates: []lifecycle.PredicateFunc{
					lifecycle.BoolValue(config.Config.Metrics.Containers.Enabled),
				},
				Throttling: &throttling.ThrottlingSettings{
					Interval: config.Config.Metrics.Containers.PollingRate,
				},
			},
			&lifecycle.SingleStep{
				//TODO: A better place? A new mode to allow continuing to run on failures? Ideally we want to have this async
				Run:  lifecycle.ForceNoError(loop.UpdateWekaStatusMetrics),
				Name: "UpdateWekaStatusMetrics", // explicit for throttling
				Predicates: []lifecycle.PredicateFunc{
					func() bool {
						for _, c := range loop.cluster.Status.Conditions {
							if c.Type == condition.CondClusterCreated {
								if time.Since(c.LastTransitionTime.Time) > time.Second*5 {
									return true
								}
							}
						}
						return false
					},
					lifecycle.BoolValue(config.Config.Metrics.Clusters.Enabled),
					lifecycle.IsNotFunc(loop.cluster.IsTerminating),
				},
				Throttling: &throttling.ThrottlingSettings{
					Interval: config.Config.Metrics.Clusters.PollingRate,
				},
			},
			&lifecycle.SingleStep{
				Run:  lifecycle.ForceNoError(loop.EnsureClusterMonitoringService),
				Name: "EnsureClusterMonitoringService",
				Predicates: []lifecycle.PredicateFunc{
					lifecycle.BoolValue(config.Config.Metrics.Clusters.Enabled),
				},
				Throttling: &throttling.ThrottlingSettings{
					Interval: config.Config.Metrics.Clusters.PollingRate,
				},
			},
			&lifecycle.SingleStep{
				Run: loop.HandleGracefulDeletion,
				Predicates: []lifecycle.PredicateFunc{
					loop.ClusterIsInGracefulDeletion,
				},
				FinishOnSuccess: true,
			},
			&lifecycle.SingleStep{
				Run: loop.HandleDeletion,
				Predicates: []lifecycle.PredicateFunc{
					loop.cluster.IsMarkedForDeletion,
					lifecycle.IsNotFunc(loop.ClusterIsInGracefulDeletion),
				},
				FinishOnSuccess: true,
			},
			&lifecycle.SingleStep{
				Run: loop.InitState,
			},
			&lifecycle.SingleStep{
				Condition:   condition.CondClusterSecretsCreated,
				Run:         loop.EnsureLoginCredentials,
				CondMessage: "Cluster secrets are created",
			},
			&lifecycle.SingleStep{
				Condition:             condition.CondPodsCreated,
				Run:                   loop.EnsureWekaContainers,
				SkipOwnConditionCheck: true,
			},
			&lifecycle.SingleStep{
				Run: loop.HandleSpecUpdates,
			},
			&lifecycle.SingleStep{
				Run: loop.updateContainersOnNodeSelectorMismatch,
				Predicates: []lifecycle.PredicateFunc{
					lifecycle.BoolValue(config.Config.CleanupBackendsOnNodeSelectorMismatch),
				},
				Throttling: &throttling.ThrottlingSettings{
					Interval:          config.Consts.SelectorMismatchCleanupInterval,
					EnsureStepSuccess: true,
				},
			},
			&lifecycle.SingleStep{
				Run: loop.deleteContainersOnTolerationsMismatch,
				Predicates: []lifecycle.PredicateFunc{
					lifecycle.BoolValue(config.Config.CleanupContainersOnTolerationsMismatch),
				},
			},
			&lifecycle.SingleStep{
				Condition:             condition.CondContainerResourcesAllocated,
				Run:                   loop.AllocateResources,
				SkipOwnConditionCheck: true,
			},
			&lifecycle.SingleStep{
				Condition: condition.CondPodsReady,
				Run:       loop.InitialContainersReady,
			},
			&lifecycle.SingleStep{
				Condition: condition.CondClusterCreated,
				Run:       loop.FormCluster,
			},
			&lifecycle.SingleStep{
				Condition: condition.CondPostClusterFormedScript,
				Run:       loop.RunPostFormClusterScript,
				Predicates: []lifecycle.PredicateFunc{
					loop.HasPostFormClusterScript,
					lifecycle.IsNotFunc(loop.cluster.IsExpand),
				},
			},
			&lifecycle.SingleStep{
				Run: loop.refreshContainersJoinIps,
			},
			&lifecycle.SingleStep{
				Condition: condition.CondJoinedCluster,
				Run:       loop.WaitForContainersJoin,
			},
			//TODO: Also will prevent from partial healthy operatable cluster
			&lifecycle.SingleStep{
				Condition: condition.CondDrivesAdded,
				Run:       loop.WaitForDrivesAdd,
			},
			&lifecycle.SingleStep{
				Condition: condition.CondIoStarted,
				Run:       loop.StartIo,
				Predicates: []lifecycle.PredicateFunc{
					lifecycle.IsNotFunc(loop.cluster.IsExpand),
				},
			},
			&lifecycle.SingleStep{
				Condition: condition.CondClusterSecretsApplied,
				Run:       loop.ApplyCredentials,
				Predicates: []lifecycle.PredicateFunc{
					lifecycle.IsNotFunc(loop.cluster.IsExpand),
				},
			},
			&lifecycle.SingleStep{
				Condition: condition.CondAdminUserDeleted,
				Run:       loop.DeleteAdminUser,
				Predicates: []lifecycle.PredicateFunc{
					func() bool {
						for _, c := range loop.cluster.Status.Conditions {
							if c.Type == condition.CondClusterSecretsApplied {
								if time.Since(c.LastTransitionTime.Time) > time.Minute*5 {
									return true
								}
							}
						}
						return false
					},
					lifecycle.IsNotFunc(loop.cluster.IsExpand),
				},
			},
			&lifecycle.SingleStep{
				Run:       loop.ConfigureKms,
				Condition: condition.CondClusterKMSConfigured,
				Predicates: []lifecycle.PredicateFunc{
					loop.ShouldConfigureKms,
				},
			},
			&lifecycle.SingleStep{
				Condition: condition.CondDefaultFsCreated,
				Run:       loop.EnsureDefaultFS,
				Predicates: []lifecycle.PredicateFunc{
					lifecycle.IsNotFunc(loop.cluster.IsExpand),
				},
			},
			&lifecycle.SingleStep{
				Run: loop.DestroyS3Cluster,
				Predicates: []lifecycle.PredicateFunc{
					loop.ShouldDestroyS3Cluster,
				},
			},
			&lifecycle.SingleStep{
				Condition: condition.CondS3ClusterCreated,
				Predicates: []lifecycle.PredicateFunc{
					loop.HasS3Containers,
				},
				Run: loop.EnsureS3Cluster,
			},
			&lifecycle.SingleStep{
				Condition: condition.ConfNfsConfigured,
				Predicates: []lifecycle.PredicateFunc{
					lifecycle.IsNotFunc(loop.cluster.IsExpand),
					loop.HasNfsContainers,
				},
				Run: loop.EnsureNfs,
			},
			&lifecycle.SingleStep{
				Condition: condition.CondClusterClientSecretsCreated,
				Run:       loop.ensureClientLoginCredentials,
			},
			&lifecycle.SingleStep{
				Condition: condition.CondClusterClientSecretsApplied,
				Run:       loop.applyClientLoginCredentials,
			},
			&lifecycle.SingleStep{
				Condition: condition.CondClusterCsiSecretsCreated,
				Run:       loop.EnsureCsiLoginCredentials,
			},
			&lifecycle.SingleStep{
				Run: loop.EnsureCsiLoginCredentials,
				Throttling: &throttling.ThrottlingSettings{
					Interval:          config.Consts.CsiLoginCredentialsUpdateInterval,
					EnsureStepSuccess: true,
				},
			},
			&lifecycle.SingleStep{
				Condition: condition.CondClusterCsiSecretsApplied,
				Run:       loop.applyCsiLoginCredentials,
			},
			&lifecycle.SingleStep{
				Condition: condition.WekaHomeConfigured,
				Run:       loop.configureWekaHome,
			},
			&lifecycle.SingleStep{
				Condition: condition.CondClusterReady,
				Run:       loop.MarkAsReady,
			},
			&lifecycle.SingleStep{
				Run: loop.handleUpgrade,
			},
		},
	}

	return stepsEngine.RunAsReconcilerResponse(ctx)
}

func (r *WekaClusterReconciler) GetProvisionContext(initContext context.Context, wekaCluster *wekav1alpha1.WekaCluster) (context.Context, error) {
	ctx, logger, end := instrumentation.GetLogSpan(initContext, "GetProvisionContext")
	defer end()

	if wekaCluster.Status.Status == ClusterStatusInit && wekaCluster.Status.TraceId == "" {
		span := trace.SpanFromContext(ctx)
		logger.Info("creating new span inside context", "traceId", span.SpanContext().TraceID().String())
		wekaCluster.Status.TraceId = span.SpanContext().TraceID().String()
		// since this is init, we need to open new span with the original traceId
		remoteContext := instrumentation.NewContextWithTraceID(ctx, nil, wekaCluster.Status.TraceId)
		ctx, logger, end = instrumentation.GetLogSpan(remoteContext, "WekaClusterProvision")
		defer end()
		logger.AddEvent("New shared Span")
		wekaCluster.Status.SpanID = logger.Span.SpanContext().SpanID().String()

		err := r.Status().Update(ctx, wekaCluster)
		if err != nil {
			logger.Error(err, "Failed to update traceId")
			return ctx, err
		}

		return ctx, nil
	}

	if wekaCluster.Status.TraceId == "" {
		return initContext, nil
	}

	logger.Info("reusing existing trace/span", "traceId", wekaCluster.Status.TraceId, "spanId", wekaCluster.Status.SpanID)
	retCtx := instrumentation.NewContextWithSpanID(ctx, nil, wekaCluster.Status.TraceId, wekaCluster.Status.SpanID)

	return retCtx, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WekaClusterReconciler) SetupWithManager(mgr ctrl.Manager, wrappedReconcile reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.WekaCluster{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: config.Config.MaxWorkers.WekaCluster}).
		Complete(wrappedReconcile)
}

func (r *WekaClusterReconciler) GCLoop(ctx context.Context) {
	for {
		r.gcIteration(ctx)
		time.Sleep(10 * time.Second)
	}
}

func (r *WekaClusterReconciler) gcIteration(ctx context.Context) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "WekaClusterGCLoop")
	defer end()

	err := r.GC(ctx)
	if err != nil {
		logger.Error(err, "gc failed")
	}
}
