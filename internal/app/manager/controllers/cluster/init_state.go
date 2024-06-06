package cluster

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"go.opentelemetry.io/otel/trace"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type initState struct {
	*ClusterState
	ClusterStatusInit string
	WekaFinalizer     string
}

func (state *ClusterState) InitState(clusterStatusInit string, wekaFinalizer string) lifecycle.StepFunc {
	initState := &initState{
		ClusterState: state,

		ClusterStatusInit: clusterStatusInit,
		WekaFinalizer:     wekaFinalizer,
	}
	return initState.StepFn
}

func (state *initState) StepFn(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "InitState")
	defer end()

	req := state.Request

	// Fetch the WekaCluster instance
	wekaClusterService, err := state.CrdManager.GetClusterService(ctx, req)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "GetClusterService failed")
		}
		return err
	}
	state.WekaClusterService = wekaClusterService

	wekaCluster := wekaClusterService.GetCluster()
	if wekaCluster == nil {
		return nil
	}

	ctx, err = state.GetProvisionContext(ctx, wekaCluster)
	if err != nil {
		logger.SetError(err, "Failed to get shared cluster context")
		return err
	}

	ctx, logger, end = instrumentation.GetLogSpan(ctx, "WekaClusterReconcileLoop", "cluster_uid", string(wekaCluster.GetUID()))
	defer end()

	logger.SetValues("cluster_uid", string(wekaCluster.GetUID()))
	logger.Info("Reconciling WekaCluster")
	logger.SetPhase("CLUSTER_RECONCILE_STARTED")

	err = state.initState(ctx, wekaCluster)
	if err != nil {
		logger.Error(err, "Failed to initialize state")
		return err
	}

	state.Subject = wekaCluster
	state.Conditions = &wekaCluster.Status.Conditions
	logger.SetPhase("CLUSTER_RECONCILE_INITIALIZED")
	return nil
}

func (state *initState) GetProvisionContext(initContext context.Context, wekaCluster *wekav1alpha1.WekaCluster) (context.Context, error) {
	ctx, logger, end := instrumentation.GetLogSpan(initContext, "GetProvisionContext")
	defer end()

	if wekaCluster.Status.Status == state.ClusterStatusInit && wekaCluster.Status.TraceId == "" {
		span := trace.SpanFromContext(ctx)
		logger.Info("creating new span inside context", "traceId", span.SpanContext().TraceID().String())
		wekaCluster.Status.TraceId = span.SpanContext().TraceID().String()
		// since this is init, we need to open new span with the original traceId
		remoteContext := instrumentation.NewContextWithTraceID(ctx, nil, wekaCluster.Status.TraceId)
		ctx, logger, end = instrumentation.GetLogSpan(remoteContext, "WekaClusterProvision")
		defer end()
		logger.AddEvent("New shared Span")
		wekaCluster.Status.SpanID = logger.Span.SpanContext().SpanID().String()

		err := state.Client.Status().Update(ctx, wekaCluster)
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

func (state *initState) initState(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "initState")
	defer end()

	if !controllerutil.ContainsFinalizer(wekaCluster, state.WekaFinalizer) {

		wekaCluster.Status.InitStatus()
		wekaCluster.Status.LastAppliedImage = wekaCluster.Spec.Image

		err := state.Client.Status().Update(ctx, wekaCluster)
		if err != nil {
			logger.Error(err, "failed to init states")
		}

		if updated := controllerutil.AddFinalizer(wekaCluster, state.WekaFinalizer); updated {
			logger.Info("Adding Finalizer for weka cluster")
			if err := state.Client.Update(ctx, wekaCluster); err != nil {
				logger.Error(err, "Failed to update custom resource to add finalizer")
				return err
			}

			if err := state.Client.Get(ctx, client.ObjectKey{Namespace: wekaCluster.Namespace, Name: wekaCluster.Name}, wekaCluster); err != nil {
				logger.Error(err, "Failed to re-fetch data")
				return err
			}
			logger.Info("Finalizer added for wekaCluster", "conditions", len(wekaCluster.Status.Conditions))
		}

	}
	return nil
}
