package wekacluster

import (
	"context"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
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
)

// WekaClusterReconciler reconciles a WekaCluster object
type WekaClusterReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Manager    ctrl.Manager
	RestClient rest.Interface

	SecretsService services.SecretsService

	ThrottlingMap throttling.Throttler
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
		StateKeeper: k8sObject,
		Throttler:   r.ThrottlingMap.WithPartition("wekacluster/" + string(loop.cluster.GetUID())),
		Steps:       loop.GetAllSteps(),
	}

	return stepsEngine.RunAsReconcilerResponse(ctx)
}

func (r *WekaClusterReconciler) GetProvisionContext(initContext context.Context, wekaCluster *weka.WekaCluster) (context.Context, error) {
	ctx, logger, end := instrumentation.GetLogSpan(initContext, "GetProvisionContext")
	defer end()

	if wekaCluster.Status.Status == weka.WekaClusterStatusInit && wekaCluster.Status.TraceId == "" {
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
		For(&weka.WekaCluster{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: config.Config.MaxWorkers.WekaCluster}).
		Complete(wrappedReconcile)
}
