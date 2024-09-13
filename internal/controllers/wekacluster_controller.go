package controllers

import (
	"context"
	"time"

	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/pkg/util"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/internal/services"
	"go.opentelemetry.io/otel/trace"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	WekaFinalizer     = "weka.weka.io/finalizer"
	ClusterStatusInit = "Init"
)

// WekaClusterReconciler reconciles a WekaCluster object
type WekaClusterReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Manager  ctrl.Manager
	Recorder record.EventRecorder

	SecretsService services.SecretsService

	DetectedZombies map[util.NamespacedObject]time.Time
}

func NewWekaClusterController(mgr ctrl.Manager) *WekaClusterReconciler {
	client := mgr.GetClient()
	config := mgr.GetConfig()
	scheme := mgr.GetScheme()
	execService := exec.NewExecService(config)

	ret := &WekaClusterReconciler{
		Client:         client,
		Scheme:         scheme,
		Manager:        mgr,
		Recorder:       mgr.GetEventRecorderFor("wekaCluster-controller"),
		SecretsService: services.NewSecretsService(client, scheme, execService),
	}

	go ret.GCLoop()
	return ret
}

func (r *WekaClusterReconciler) Reconcile(initContext context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(initContext, "WekaClusterReconcile", "namespace", req.Namespace, "cluster_name", req.Name)
	defer end()

	loop := NewWekaClusterReconcileLoop(r.Manager)

	err := loop.FetchCluster(ctx, req)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Cluster not found, ignoring reconcile")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
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

	reconSteps := lifecycle.ReconciliationSteps{
		Client:           r.Client,
		ConditionsObject: loop.cluster,
		Conditions:       &loop.cluster.Status.Conditions,
		Steps: []lifecycle.Step{
			{
				Run: loop.HandleDeletion,
				Predicates: lifecycle.Predicates{
					loop.cluster.IsMarkedForDeletion,
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Run: loop.InitState,
			},
			{
				Condition:   condition.CondClusterSecretsCreated,
				Run:         loop.EnsureLoginCredentials,
				CondMessage: "Cluster secrets are created",
			},
			{
				Condition:             condition.CondPodsCreated,
				Run:                   loop.EnsureWekaContainers,
				SkipOwnConditionCheck: true,
			},
			{
				Run: loop.HandleSpecUpdates,
			},
			{
				Condition:             condition.CondContainerResourcesAllocated,
				Run:                   loop.AllocateResources,
				SkipOwnConditionCheck: true,
			},
			{
				//TODO: We should be operating with not all containers up, so this one ultimately is incorrect condition
				Condition: condition.CondPodsReady,
				Run:       loop.AllContainersReady,
			},
			{
				Condition: condition.CondClusterCreated,
				Run:       loop.FormCluster,
			},
			//TODO: Very fat function, that will prevent us from continuing on any bad container
			{
				Condition: condition.CondJoinedCluster,
				Run:       loop.WaitForContainersJoin,
			},
			//TODO: Also will prevent from partial healthy operatable cluster
			{
				Condition: condition.CondDrivesAdded,
				Run:       loop.WaitForDrivesAdd,
			},
			{
				Condition: condition.CondIoStarted,
				Run:       loop.StartIo,
				Predicates: lifecycle.Predicates{
					lifecycle.IsNotFunc(loop.cluster.IsExpand),
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition: condition.CondClusterSecretsApplied,
				Run:       loop.ApplyCredentials,
				Predicates: lifecycle.Predicates{
					lifecycle.IsNotFunc(loop.cluster.IsExpand),
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition: condition.CondAdminUserDeleted,
				Run:       loop.DeleteAdminUser,
				Predicates: lifecycle.Predicates{
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
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition: condition.CondDefaultFsCreated,
				Run:       loop.EnsureDefaultFS,
				Predicates: lifecycle.Predicates{
					lifecycle.IsNotFunc(loop.cluster.IsExpand),
				},
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition: condition.CondS3ClusterCreated,
				Predicates: lifecycle.Predicates{
					lifecycle.IsNotFunc(loop.cluster.IsExpand),
					loop.HasS3Containers,
				},
				Run:                       loop.EnsureS3Cluster,
				ContinueOnPredicatesFalse: true,
			},
			{
				Condition: condition.CondClusterClientSecretsCreated,
				Run: func(ctx context.Context) error {
					return r.SecretsService.EnsureClientLoginCredentials(ctx, loop.cluster, loop.containers)
				},
			},
			{
				Condition: condition.CondClusterClientSecretsApplied,
				Run:       loop.applyClientLoginCredentials,
			},
			{
				Condition: condition.CondClusterCSISecretsCreated,
				Run: func(ctx context.Context) error {
					return r.SecretsService.EnsureCSILoginCredentials(ctx, loop.clusterService)
				},
			},
			{
				Condition: condition.CondClusterCSISecretsApplied,
				Run:       loop.applyCSILoginCredentials,
			},
			{
				Condition: condition.WekaHomeConfigured,
				Run:       loop.configureWekaHome,
			},
			{
				Condition: condition.CondClusterReady,
				Run:       loop.MarkAsReady,
			},
			{
				Run: loop.handleUpgrade,
			},
		},
	}

	return reconSteps.RunAsReconcilerResponse(ctx)
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
		Owns(&wekav1alpha1.WekaContainer{}).
		Complete(wrappedReconcile)
}

func (r *WekaClusterReconciler) GCLoop() {
	ctx := context.Background()
	for {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "MainGcLoop")
		//getlogspan
		defer end()

		err := r.GC(ctx)
		if err != nil {
			logger.Error(err, "gc failed")
		}
		time.Sleep(10 * time.Second)
	}
}
