package controllers

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/factories"
	"github.com/weka/weka-operator/internal/app/manager/factory"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	"github.com/pkg/errors"
	"github.com/thoas/go-funk"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

type ReconciliationError struct {
	Err     error
	Cluster *wekav1alpha1.WekaCluster
	Step    lifecycle.Step
}

func (e ReconciliationError) Error() string {
	return fmt.Sprintf("error reconciling cluster %s during phase %s: %v", e.Cluster.Name, e.Step.Condition, e.Err)
}

// WekaClusterReconciler reconciles a WekaCluster object
type WekaClusterReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	Manager              ctrl.Manager
	Recorder             record.EventRecorder
	AllocationService    services.AllocationService
	CredentialsService   services.CredentialsService
	WekaContainerFactory factories.WekaContainerFactory

	CrdManager     services.CrdManager
	SecretsService services.SecretsService
	ExecService    services.ExecService

	WekaContainerFactory factory.WekaContainerFactory
}

func NewWekaClusterController(mgr ctrl.Manager) *WekaClusterReconciler {
	client := mgr.GetClient()
	config := mgr.GetConfig()
	scheme := mgr.GetScheme()
	execService := services.NewExecService(config)
	return &WekaClusterReconciler{
		Client:   client,
		Scheme:   scheme,
		Manager:  mgr,
		Recorder: mgr.GetEventRecorderFor("wekaCluster-controller"),

		CrdManager:           services.NewCrdManager(mgr),
		SecretsService:       services.NewSecretsService(client, scheme, execService),
		ExecService:          execService,
		WekaContainerFactory: factory.NewWekaContainerFactory(scheme),

		AllocationService:    services.NewAllocationService(client),
		CredentialsService:   services.NewCredentialsService(client, scheme, config),
		WekaContainerFactory: factories.NewWekaContainerFactory(scheme),
	}
}

func (r *WekaClusterReconciler) SetCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster,
	condType string, status metav1.ConditionStatus, reason string, message string,
) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "SetCondition", "condition_type", condType, "condition_status", string(status))
	defer end()

	logger.Info("Setting condition",
		"condition_type", condType,
		"condition_status", string(status),
	)

	condRecord := metav1.Condition{
		Type:    condType,
		Status:  status,
		Reason:  reason,
		Message: message,
	}

	meta.SetStatusCondition(&cluster.Status.Conditions, condRecord)
	err := r.Status().Update(ctx, cluster)
	if err != nil {
		return err
	}

	return nil
}

func (r *WekaClusterReconciler) Reconcile(initContext context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(initContext, "WekaClusterReconcile", "namespace", req.Namespace, "name", req.Name)
	defer end()

	// Fetch the WekaCluster instance
	wekaClusterService, err := r.CrdManager.GetCluster(ctx, req)
	if err != nil {
		logger.Error(err, "GetCluster failed")
		return ctrl.Result{}, err
	}

	wekaCluster := wekaClusterService.GetCluster()
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logger.SetError(err, "Failed to get wekaCluster")
			return ctrl.Result{}, err
		}
	}
	if wekaCluster == nil {
		logger.SetError(errors.New("WekaCluster not found"), "Existing WekaCluster not found")
		return ctrl.Result{}, nil
	}

	ctx, err = r.GetProvisionContext(ctx, wekaCluster)
	if err != nil {
		logger.SetError(err, "Failed to get shared cluster context")
		return ctrl.Result{}, err
	}

	logger.SetValues("cluster_uid", string(wekaCluster.GetUID()))
	logger.Info("Reconciling WekaCluster")
	logger.SetPhase("CLUSTER_RECONCILE_STARTED")

	// TODO: Make the states explicit
	stateMachine := NewReconcilerStateMachine(
		r.WekaClusterService,
		r.AllocationService,
		r.CredentialsService,
		r.Client,
		r.Recorder,
		wekaCluster,
	)
	result, err := stateMachine.RunLoop(ctx)
	if err != nil {
		logger.Error(err, "Failed to run state machine")
		return ctrl.Result{}, err
	}
	if result.Requeue {
		return ctrl.Result{Requeue: true}, nil
	}

	// generate login credentials
	steps := &reconciliationSteps{
		Reconciler: r,
		State: &lifecycle.ReconciliationState{
			Cluster: wekaCluster,
		},
		Steps: []lifecycle.Step{
			{
				Condition: "ClusterSecretsCreated",
				Preconditions: []lifecycle.PreconditionFunc{
					lifecycle.IsNotTrue(condition.CondClusterSecretsCreated),
				},
				Reconcile: lifecycle.ClusterSecretsCreated(r.SecretsService, r),
			},
			{
				Condition: condition.CondPodsCreated,
				Preconditions: []lifecycle.PreconditionFunc{
					lifecycle.IsNotTrue(condition.CondPodsCreated),
				},
				Reconcile: lifecycle.PodsCreated(r.CrdManager, r),
			},
			{
				Condition: condition.CondPodsReady,
				Preconditions: []lifecycle.PreconditionFunc{
					lifecycle.IsNotTrue(condition.CondPodsReady),
				},
				Reconcile: lifecycle.PodsReady(r),
			},
		},
	}
	if err := steps.reconcile(ctx); err != nil {
		logger.Error(err, "Failed to reconcile cluster")
		return ctrl.Result{}, err
	}

	logger.SetPhase("CLUSTER_READY")

	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondDefaultFsCreated) {
		logger.SetPhase("CONFIGURING_DEFAULT_FS")
		err := r.WekaClusterService.EnsureDefaultFs(ctx, containers[0])
		if err != nil {
			return ctrl.Result{}, err
		}
		_ = r.SetCondition(ctx, wekaCluster, condition.CondDefaultFsCreated, metav1.ConditionTrue, "Init", "Created default filesystem")
		err = r.Status().Update(ctx, wekaCluster)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondS3ClusterCreated) {
		logger.SetPhase("CONFIGURING_DEFAULT_FS")
		containers := r.SelectS3Containers(containers)
		if len(containers) > 0 {
			err := r.WekaClusterService.EnsureS3Cluster(ctx, containers)
			if err != nil {
				return ctrl.Result{}, err
			}
			err = r.SetCondition(ctx, wekaCluster, condition.CondS3ClusterCreated, metav1.ConditionTrue, "Init", "Created S3 cluster")
			if err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondClusterClientSecretsCreated) {
		err := r.SecretsService.EnsureClientLoginCredentials(ctx, wekaCluster, containers)
		if err != nil {
			return ctrl.Result{}, err
		}
		err = r.SetCondition(ctx, wekaCluster, condition.CondClusterClientSecretsCreated, metav1.ConditionTrue, "Init", "Created client secrets")
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondClusterClientSecretsApplied) {
		err := r.applyClientLoginCredentials(ctx, wekaCluster, containers)
		if err != nil {
			return ctrl.Result{}, err
		}
		err = r.SetCondition(ctx, wekaCluster, condition.CondClusterClientSecretsApplied, metav1.ConditionTrue, "Init", "Applied client secrets")
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	logger.SetPhase("CLUSTER_READY")
	return ctrl.Result{}, nil
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

func (r *WekaClusterReconciler) ensureDefaultFs(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureDefaultFs")
	defer end()

	return r.WekaClusterService.EnsureDefaultFs(ctx, container)
}

func (r *WekaClusterReconciler) SelectS3Containers(containers []*wekav1alpha1.WekaContainer) []*wekav1alpha1.WekaContainer {
	var s3Containers []*wekav1alpha1.WekaContainer
	for _, container := range containers {
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeS3 {
			s3Containers = append(s3Containers, container)
		}
	}
	return s3Containers
}

func (r *WekaClusterReconciler) SelectActiveContainer(containers []*wekav1alpha1.WekaContainer) *wekav1alpha1.WekaContainer {
	for _, container := range containers {
		if container.Status.ClusterContainerID != nil {
			return container
		}
	}
	return nil
}

func (r *WekaClusterReconciler) applyClientLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "applyClientLoginCredentials")
	defer end()

	container := r.SelectActiveContainer(containers)
	username, password, err := r.getUsernameAndPassword(ctx, cluster.Namespace, cluster.GetClientSecretName())

	wekaService := services.NewWekaService(r.ExecService, container)
	err = wekaService.EnsureUser(ctx, username, password, "regular")
	if err != nil {
		logger.Error(err, "Failed to ensure user")
		return err
	}
	logger.SetStatus(codes.Ok, "Client login credentials applied")
	return nil
}

type reconciliationSteps struct {
	Reconciler *WekaClusterReconciler
	State      *lifecycle.ReconciliationState
	Steps      []lifecycle.Step
}

func (r *reconciliationSteps) reconcile(ctx context.Context) error {
	for _, step := range r.Steps {

		failedPreconditions := funk.Filter(step.Preconditions, func(precondition lifecycle.PreconditionFunc) bool {
			return !precondition(r.State.Cluster.Status.Conditions)
		}).([]lifecycle.PreconditionFunc)
		if len(failedPreconditions) > 0 {
			continue
		}

		err := step.Reconcile(ctx, r.State)
		if err != nil {
			return &ReconciliationError{Err: err, Cluster: r.State.Cluster, Step: step}
		}
	}
	return nil
}
