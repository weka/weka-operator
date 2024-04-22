package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/thoas/go-funk"
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	"github.com/weka/weka-operator/internal/app/manager/factories"
	"github.com/weka/weka-operator/internal/app/manager/factory"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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

func (r *WekaClusterReconciler) SetConditionWithRetries(ctx context.Context, cluster *wekav1alpha1.WekaCluster,
	condType string, status metav1.ConditionStatus, reason string, message string,
) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "SetCondition", "condition_type", condType, "condition_status", string(status))
	defer end()

	logger.WithValues(
		"condition_type", condType,
		"condition_status", string(status),
	).Info("Setting condition")

	condRecord := metav1.Condition{
		Type:    condType,
		Status:  status,
		Reason:  reason,
		Message: message,
	}
	for i := 0; i < 3; i++ {
		meta.SetStatusCondition(&cluster.Status.Conditions, condRecord)
		err := r.Status().Update(ctx, cluster)
		if err != nil {
			logger.Debug("Failed to update wekaCluster status", "err", err)
			if i == 2 {
				logger.Error(err, "Failed to update wekaCluster status after 3 retries")
				return errors.Wrap(err, "Failed to update wekaCluster status")
			}
			// need to re-fetch the cluster since we have a stale version of object
			err = r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, cluster)
			if err != nil {
				if apierrors.IsNotFound(err) {
					logger.Error(err, "wekaCluster resource not found although expected")
				}
				// Error reading the object - requeue the request.
				logger.Error(err, "Failed to fetch new version of object")
			}
			continue
		}
		logger.SetStatus(codes.Ok, "Condition set")
		break
	}
	return nil
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

	ctx, logger, end = instrumentation.GetLogSpan(ctx, "WekaClusterReconcileLoop", "cluster_uid", string(wekaCluster.GetUID()))
	defer end()

	logger.SetValues("cluster_uid", string(wekaCluster.GetUID()))
	logger.Info("Reconciling WekaCluster")
	logger.SetPhase("CLUSTER_RECONCILE_STARTED")

	err = r.initState(ctx, wekaCluster)
	if err != nil {
		logger.Error(err, "Failed to initialize state")
		return ctrl.Result{}, err
	}
	logger.SetPhase("CLUSTER_RECONCILE_INITIALIZED")

	if wekaCluster.GetDeletionTimestamp() != nil {
		err = r.handleDeletion(ctx, wekaCluster)
		if err != nil {
			logger.Error(err, "Failed to handle deletion")
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, err
		}
		logger.SetPhase("CLUSTER_IS_BEING_DELETED")
		return ctrl.Result{}, nil
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

	// Note: All use of conditions is only as hints for skipping actions and a visibility, not strictly a state machine
	// All code should be idempotent and not rely on conditions for correctness, hence validation of succesful update of conditions is not done
	var containers []*wekav1alpha1.WekaContainer

	containers, err = r.CrdManager.EnsureWekaContainers(ctx, wekaCluster)
	if err != nil {
		logger.Error(err, "ensureWekaContainers", "cluster", wekaCluster.Name)
		return ctrl.Result{RequeueAfter: time.Second * 3}, nil
	}

	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondPodsReady) {
		logger.Debug("Checking if all containers are ready")
		if ready, err := r.WekaClusterService.IsContainersReady(ctx, containers); !ready {
			logger.SetPhase("CONTAINERS_NOT_READY")
			if err != nil {
				logger.Error(err, "containers are not ready")
			}
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 5}, nil
		}
		logger.SetPhase("CONTAINERS_ARE_READY")
		_ = r.SetCondition(ctx, wekaCluster, condition.CondPodsReady, metav1.ConditionTrue, "Init", "All weka containers are ready for clusterization")
	} else {
		logger.SetPhase("CONTAINERS_ARE_READY")
	}

>>>>>>> a7e5a70 (refactor(controllers): move business logic to services)
	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondClusterCreated) {
		logger.SetPhase("CLUSTERIZING")
		err = wekaClusterService.Create(ctx, containers)
		if err != nil {
			logger.Error(err, "Failed to create cluster")
			meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
				Type:   condition.CondClusterCreated,
				Status: metav1.ConditionFalse, Reason: "Error", Message: err.Error(),
			})
			_ = r.Status().Update(ctx, wekaCluster)
			return ctrl.Result{}, err
		}
		_ = r.SetCondition(ctx, wekaCluster, condition.CondClusterCreated, metav1.ConditionTrue, "Init", "Cluster is formed")
		logger.SetPhase("CLUSTER_FORMED")
	} else {
		logger.SetPhase("CLUSTER_ALREADY_FORMED")
	}

	// Ensure all containers are up in the cluster
	logger.Debug("Ensuring all containers are up in the cluster")
	joinedContainers := 0
	for _, container := range containers {
		if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondJoinedCluster) {
			logger.Info("Container has not joined the cluster yet", "container", container.Name)
			logger.SetPhase("CONTAINERS_NOT_JOINED_CLUSTER")
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, nil
		} else {
			if wekaCluster.Status.ClusterID == "" {
				wekaCluster.Status.ClusterID = container.Status.ClusterID
				err := r.Status().Update(ctx, wekaCluster)
				if err != nil {
					return ctrl.Result{}, err
				}
				logger.Info("Container joined cluster successfully", "container_name", container.Name)
			}
			joinedContainers++
		}
	}
	if joinedContainers == len(containers) {
		logger.SetPhase("ALL_CONTAINERS_ALREADY_JOINED")
	} else {
		logger.SetPhase("CONTAINERS_JOINED_CLUSTER")
	}

	err = r.WekaClusterService.EnsureClusterContainerIds(ctx, wekaCluster, containers)
	if err != nil {
		logger.Info("not all containers are up in the cluster", "err", err)
		return ctrl.Result{Requeue: true, RequeueAfter: time.Second}, nil
	}

	// Ensure all containers are up in the cluster
	logger.Debug("Ensuring all drives are up in the cluster")
	for _, container := range containers {
		if container.Spec.Mode != "drive" {
			continue
		}
		if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondDrivesAdded) {
			logger.Info("Containers did not add drives yet", "container", container.Name)
			logger.InfoWithStatus(codes.Unset, "Containers did not add drives yet")
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, nil
		}
	}
	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondDrivesAdded) {
		err := r.SetCondition(ctx, wekaCluster, condition.CondDrivesAdded, metav1.ConditionTrue, "Init", "All drives are added")
		if err != nil {
			return ctrl.Result{}, err
		}
		logger.SetPhase("ALL_DRIVES_ADDED")
	}

	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondIoStarted) {
		logger.Info("Ensuring IO is started")
		_ = r.SetCondition(ctx, wekaCluster, condition.CondIoStarted, metav1.ConditionUnknown, "Init", "Starting IO")
		logger.Info("Starting IO")
		err = r.WekaClusterService.StartIo(ctx, wekaCluster, containers)
		if err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("IO Started, time since create:" + time.Since(wekaCluster.CreationTimestamp.Time).String())
		_ = r.SetCondition(ctx, wekaCluster, condition.CondIoStarted, metav1.ConditionTrue, "Init", "IO is started")
		logger.SetPhase("IO_IS_STARTED")
	}

	logger.SetPhase("CONFIGURING_CLUSTER_CREDENTIALS")
	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondClusterSecretsApplied) {
		err = r.CredentialsService.ApplyClusterCredentials(ctx, wekaCluster, containers)
		if err != nil {
			return ctrl.Result{}, err
		}
		_ = r.SetCondition(ctx, wekaCluster, condition.CondClusterSecretsApplied, metav1.ConditionTrue, "Init", "Applied cluster secrets")
		wekaCluster.Status.Status = "Ready"
		wekaCluster.Status.TraceId = ""
		wekaCluster.Status.SpanID = ""
		err = r.Status().Update(ctx, wekaCluster)
		if err != nil {
			return ctrl.Result{}, err
		}
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

func (r *WekaClusterReconciler) handleDeletion(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "handleDeletion")
	defer end()

	if controllerutil.ContainsFinalizer(wekaCluster, WekaFinalizer) {
		logger.Info("Performing Finalizer Operations for wekaCluster before delete CR")

		// Perform all operations required before remove the finalizer and allow
		// the Kubernetes API to remove the custom resource.
		err := r.doFinalizerOperationsForwekaCluster(ctx, wekaCluster)
		if err != nil {
			return err
		}

		logger.Info("Removing Finalizer for wekaCluster after successfully perform the operations")
		if ok := controllerutil.RemoveFinalizer(wekaCluster, WekaFinalizer); !ok {
			err := errors.New("Failed to remove finalizer for wekaCluster")
			return err
		}

		if err := r.Update(ctx, wekaCluster); err != nil {
			logger.Error(err, "Failed to remove finalizer for wekaCluster")
			return err
		}

	}
	return nil
}

func (r *WekaClusterReconciler) initState(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "initState")
	defer end()

	if !controllerutil.ContainsFinalizer(wekaCluster, WekaFinalizer) {

		wekaCluster.Status.InitStatus()

		err := r.Status().Update(ctx, wekaCluster)
		if err != nil {
			logger.Error(err, "failed to init states")
		}

		logger.Info("Adding Finalizer for weka cluster")
		if ok := controllerutil.AddFinalizer(wekaCluster, WekaFinalizer); !ok {
			logger.Info("Failed to add finalizer for wekaCluster")
			return errors.New("Failed to add finalizer for wekaCluster")
		}

		if err := r.Update(ctx, wekaCluster); err != nil {
			logger.Error(err, "Failed to update custom resource to add finalizer")
			return err
		}

		if err := r.Get(ctx, client.ObjectKey{Namespace: wekaCluster.Namespace, Name: wekaCluster.Name}, wekaCluster); err != nil {
			logger.Error(err, "Failed to re-fetch data")
			return err
		}
		logger.Info("Finalizer added for wekaCluster", "conditions", len(wekaCluster.Status.Conditions))
	}
	return nil
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

func (r *WekaClusterReconciler) doFinalizerOperationsForwekaCluster(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "doFinalizerOperationsForwekaCluster")
	defer end()
	if cluster.Spec.Topology == "" {
		logger.Info("Topology is not set, skipping deallocation")
		return nil
	}
	topology, err := domain.Topologies[cluster.Spec.Topology](ctx, r, cluster.Spec.NodeSelector)
	if err != nil {
		return err
	}
	allocator := domain.NewAllocator(topology)
	allocations, allocConfigMap, err := r.CrdManager.GetOrInitAllocMap(ctx)
	if err != nil {
		logger.Error(err, "Failed to get alloc map")
		return err
	}

	changed := allocator.DeallocateCluster(domain.OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace}, allocations)
	if changed {
		if err := r.CrdManager.UpdateAllocationsConfigmap(ctx, allocations, allocConfigMap); err != nil {
			logger.Error(err, "Failed to update alloc map")
			return err
		}
	}
	r.Recorder.Event(cluster, "Warning", "Deleting",
		fmt.Sprintf("Custom Resource %s is being deleted from the namespace %s",
			cluster.Name,
			cluster.Namespace))
	return nil
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
