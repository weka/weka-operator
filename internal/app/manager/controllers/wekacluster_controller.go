package controllers

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/domain"
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

// WekaClusterReconciler reconciles a WekaCluster object
type WekaClusterReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Manager  ctrl.Manager
	Recorder record.EventRecorder

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

func (r *WekaClusterReconciler) UpdateStatus(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error {
	return r.Status().Update(ctx, cluster)
}

func (r *WekaClusterReconciler) Reconcile(initContext context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(initContext, "WekaClusterReconcile", "namespace", req.Namespace, "cluster_name", req.Name)
	defer end()

	// Fetch the WekaCluster instance
	wekaClusterService, err := r.CrdManager.GetClusterService(ctx, req)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "GetClusterService failed")
		}
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
		err = r.handleDeletion(ctx, wekaClusterService)
		if err != nil {
			logger.Error(err, "Failed to handle deletion")
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, nil
		}
		logger.SetPhase("CLUSTER_IS_BEING_DELETED")
		return ctrl.Result{}, nil
	}

	// generate login credentials
	state := &lifecycle.ClusterState{
		ReconciliationState: lifecycle.ReconciliationState[*wekav1alpha1.WekaCluster]{
			Subject:    wekaCluster,
			Conditions: &wekaCluster.Status.Conditions,
		},
	}

	steps := &lifecycle.ReconciliationSteps{
		Reconciler: r.Client,
		State:      &state.ReconciliationState,
		Steps: []lifecycle.Step{
			{
				Condition:             "ClusterSecretsCreated",
				Predicates:            []lifecycle.PredicateFunc{}, // default value
				SkipOwnConditionCheck: false,                       // default value
				Reconcile:             state.ClusterSecretsCreated(r.SecretsService),
			},
			{
				Condition:             condition.CondPodsCreated,
				SkipOwnConditionCheck: true,
				Reconcile:             state.PodsCreated(r.CrdManager),
			},
			{
				Condition: condition.CondPodsReady,
				Reconcile: state.PodsReady(),
			},
			{
				Condition: condition.CondClusterCreated,
				Reconcile: state.ClusterCreated(wekaClusterService),
			},
			{
				Condition: condition.CondJoinedCluster,
				Reconcile: state.ContainersJoinedCluster(wekaClusterService, r.Client),
			},

			{
				Condition: condition.CondDrivesAdded,
				Reconcile: state.DrivesAdded(),
			},
			{
				Condition: condition.CondIoStarted,
				Reconcile: state.StartIo(r.ExecService),
			},
			{
				Condition: condition.CondClusterSecretsApplied,
				Predicates: []lifecycle.PredicateFunc{
					lifecycle.IsTrue(condition.CondIoStarted),
				},
				Reconcile: state.ApplyClusterSecrets(wekaClusterService, r.Client),
			},
			{
				Condition: condition.CondDefaultFsCreated,
				Reconcile: state.DefaultFsCreated(wekaClusterService),
			},
		},
	}
	if err := steps.Reconcile(ctx); err != nil {
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

	logger.SetPhase("CLUSTER_READY")

	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondS3ClusterCreated) {
		logger.SetPhase("CONFIGURING_DEFAULT_FS")
		containers := r.SelectS3Containers(containers)
		if len(containers) > 0 {
			err := r.ensureS3Cluster(ctx, wekaCluster, containers)
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

	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondClusterCSISecretsCreated) {
		err := r.SecretsService.EnsureCSILoginCredentials(ctx, wekaClusterService)
		if err != nil {
			return ctrl.Result{}, err
		}
		err = r.SetCondition(ctx, wekaCluster, condition.CondClusterCSISecretsCreated, metav1.ConditionTrue, "Init", "Created CSI secrets")
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondClusterCSISecretsApplied) {
		err := r.applyCSILoginCredentials(ctx, wekaCluster, containers)
		if err != nil {
			return ctrl.Result{}, err
		}
		err = r.SetCondition(ctx, wekaCluster, condition.CondClusterCSISecretsApplied, metav1.ConditionTrue, "Init", "Applied CSI secrets")
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	logger.SetPhase("CLUSTER_READY")

	err = r.HandleUpgrade(ctx, wekaCluster)
	if err != nil {
		// TODO: separate unknown from expected reconcilation errors for info/error logging,
		// right now err is swallowed as meaningless for known cases
		logger.Info("upgrade in process", "lastErr", err)
		return ctrl.Result{RequeueAfter: time.Second * 3}, nil
	}

	err = r.HandleUpgrade(ctx, wekaCluster)
	if err != nil {
		// TODO: separate unknown from expected reconcilation errors for info/error logging,
		// right now err is swallowed as meaningless for known cases
		logger.Info("upgrade in process", "lastErr", err)
		return ctrl.Result{RequeueAfter: time.Second * 3}, nil
	}
	return ctrl.Result{}, nil
}

func (r *WekaClusterReconciler) handleDeletion(ctx context.Context, clusterService services.WekaClusterService) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "handleDeletion")
	defer end()
	wekaCluster := clusterService.GetCluster()
	if controllerutil.ContainsFinalizer(wekaCluster, WekaFinalizer) {
		logger.Info("Performing Finalizer Operations for wekaCluster before delete CR")

		// Perform all operations required before remove the finalizer and allow
		// the Kubernetes API to remove the custom resource.
		err := r.finalizeWekaCluster(ctx, clusterService)
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
		wekaCluster.Status.LastAppliedImage = wekaCluster.Spec.Image

		err := r.Status().Update(ctx, wekaCluster)
		if err != nil {
			logger.Error(err, "failed to init states")
		}

		if updated := controllerutil.AddFinalizer(wekaCluster, WekaFinalizer); updated {
			logger.Info("Adding Finalizer for weka cluster")
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

func (r *WekaClusterReconciler) finalizeWekaCluster(ctx context.Context, clusterService services.WekaClusterService) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "finalizeWekaCluster")
	defer end()

	cluster := clusterService.GetCluster()

	err := clusterService.EnsureNoS3Containers(ctx)
	if err != nil {
		return err
	}

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

func (r *WekaClusterReconciler) isContainersReady(ctx context.Context, containers []*wekav1alpha1.WekaContainer) (bool, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "isContainersReady")
	defer end()

	for _, container := range containers {
		if container.GetDeletionTimestamp() != nil {
			logger.Debug("Container is being deleted, rejecting cluster create", "container_name", container.Name)
			return false, errors.New("Container " + container.Name + " is being deleted, rejecting cluster create")
		}
		if container.Status.ManagementIP == "" {
			logger.Debug("Container is not ready yet or has no valid management IP", "container_name", container.Name)
			return false, nil
		}

		if container.Status.Status != "Running" {
			logger.Debug("Container is not running yet", "container_name", container.Name)
			return false, nil
		}
	}
	logger.InfoWithStatus(codes.Ok, "Containers are ready")
	return true, nil
}

func (r *WekaClusterReconciler) ensureDefaultFs(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureDefaultFs")
	defer end()

	wekaService := services.NewWekaService(r.ExecService, container)
	status, err := wekaService.GetWekaStatus(ctx)
	if err != nil {
		return err
	}

	err = wekaService.CreateFilesystemGroup(ctx, "default")
	if err != nil {
		if !errors.As(err, &services.FilesystemGroupExists{}) {
			return err
		}
	}

	// This defaults are not meant to be configurable, as instead weka should not require them.
	// Until then, user configuratino post cluster create

	thinProvisionedLimits := status.Capacity.TotalBytes / 2 // half a total capacity allocated for thin provisioning
	fsReservedCapacity := status.Capacity.TotalBytes / 100
	var configFsSize int64 = 3 * 1024 * 1024 * 1024

	err = wekaService.CreateFilesystem(ctx, ".config_fs", "default", services.FSParams{
		TotalCapacity:             strconv.FormatInt(thinProvisionedLimits, 10),
		ThickProvisioningCapacity: strconv.FormatInt(configFsSize, 10),
		ThinProvisioningEnabled:   true,
	})
	if err != nil {
		if !errors.As(err, &services.FilesystemExists{}) {
			return err
		}
	}

	err = wekaService.CreateFilesystem(ctx, "default-s3", "default", services.FSParams{
		TotalCapacity:             strconv.FormatInt(thinProvisionedLimits, 10),
		ThickProvisioningCapacity: strconv.FormatInt(fsReservedCapacity, 10),
		ThinProvisioningEnabled:   true,
	})
	if err != nil {
		if !errors.As(err, &services.FilesystemExists{}) {
			return err
		}
	}

	err = wekaService.CreateFilesystem(ctx, "default", "default", services.FSParams{
		TotalCapacity:             strconv.FormatInt(thinProvisionedLimits, 10),
		ThickProvisioningCapacity: strconv.FormatInt(fsReservedCapacity, 10),
		ThinProvisioningEnabled:   true,
	})
	if err != nil {
		if !errors.As(err, &services.FilesystemExists{}) {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "default filesystem ensured")
	return nil
}

func (r *WekaClusterReconciler) ensureS3Cluster(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureS3Cluster")
	defer end()

	container := containers[0]
	wekaService := services.NewWekaService(r.ExecService, container)
	containerIds := []int{}
	for _, c := range containers {
		containerIds = append(containerIds, *c.Status.ClusterContainerID)
	}

	err := wekaService.CreateS3Cluster(ctx, services.S3Params{
		EnvoyPort:          container.Spec.S3Params.EnvoyPort,
		EnvoyAdminPort:     container.Spec.S3Params.EnvoyAdminPort,
		S3Port:             container.Spec.S3Params.S3Port,
		ContainerIds:       containerIds,
		EnvoyContainerName: fmt.Sprintf("%senvoy", cluster.GetLastGuidPart()),
		MinioContainerName: fmt.Sprintf("%ss3be", cluster.GetLastGuidPart()),
	})
	if err != nil {
		if !errors.As(err, &services.S3ClusterExists{}) {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "S3 cluster ensured")
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

	wekaClusterService := services.NewWekaClusterService(r.Manager, cluster)
	container := r.SelectActiveContainer(containers)
	username, password, err := wekaClusterService.GetUsernameAndPassword(ctx, cluster.Namespace, cluster.GetClientSecretName())

	wekaService := services.NewWekaService(r.ExecService, container)
	err = wekaService.EnsureUser(ctx, username, password, "regular")
	if err != nil {
		logger.Error(err, "Failed to ensure user")
		return err
	}
	logger.SetStatus(codes.Ok, "Client login credentials applied")
	return nil
}

func (r *WekaClusterReconciler) HandleUpgrade(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleUpgrade")
	defer end()

	updateContainer := func(container *wekav1alpha1.WekaContainer) error {
		if container.Status.LastAppliedImage != cluster.Spec.Image {
			if container.Spec.Image != cluster.Spec.Image {
				container.Spec.Image = cluster.Spec.Image
				if err := r.Update(ctx, container); err != nil {
					return err
				}
			}
		}
		return nil
	}
	areUpgraded := func(containers []*wekav1alpha1.WekaContainer) bool {
		for _, container := range containers {
			if container.Status.LastAppliedImage != container.Spec.Image {
				return false
			}
		}
		return true
	}

	allAtOnceUpgrade := func(containers []*wekav1alpha1.WekaContainer) error {
		for _, container := range containers {
			if err := updateContainer(container); err != nil {
				return err
			}
		}
		if !areUpgraded(containers) {
			return errors.New("containers upgrade not finished yet")
		}
		return nil
	}

	rollingUpgrade := func(containers []*wekav1alpha1.WekaContainer) error {
		for _, container := range containers {
			if container.Status.LastAppliedImage != container.Spec.Image {
				return errors.New("container upgrade not finished yet")
			}
		}

		for _, container := range containers {
			if container.Spec.Image != cluster.Spec.Image {
				err := updateContainer(container)
				if err != nil {
					return err
				}
				return errors.New("container upgrade not finished yet")
			}
		}
		return nil
	}

	if cluster.Spec.Image != cluster.Status.LastAppliedImage {
		logger.Info("Image upgrade sequence")
		service, err := r.CrdManager.GetClusterService(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}})
		if err != nil {
			return err
		}
		driveContainers, err := service.GetOwnedContainers(ctx, wekav1alpha1.WekaContainerModeDrive)
		if err != nil {
			return err
		}
		err = allAtOnceUpgrade(driveContainers)
		if err != nil {
			return err
		}

		computeContainers, err := service.GetOwnedContainers(ctx, wekav1alpha1.WekaContainerModeCompute)
		if err != nil {
			return err
		}
		err = allAtOnceUpgrade(computeContainers)
		if err != nil {
			return err
		}

		s3Containers, err := service.GetOwnedContainers(ctx, wekav1alpha1.WekaContainerModeS3)
		if err != nil {
			return err
		}
		err = rollingUpgrade(s3Containers)

		cluster.Status.LastAppliedImage = cluster.Spec.Image
		if err := r.Status().Update(ctx, cluster); err != nil {
			return err
		}
	}

	return nil
}

func (r *WekaClusterReconciler) applyCSILoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "applyCSILoginCredentials")
	defer end()

	container := r.SelectActiveContainer(containers)
	clusterService := services.NewWekaClusterService(r.Manager, cluster)
	username, password, err := clusterService.GetUsernameAndPassword(ctx, cluster.Namespace, cluster.GetCSISecretName())
	if err != nil {
		return err
	}

	wekaService := services.NewWekaService(r.ExecService, container)
	err = wekaService.EnsureUser(ctx, username, password, "clusteradmin")
	if err != nil {
		logger.Error(err, "Failed to ensure user")
		return err
	}
	logger.SetStatus(codes.Ok, "CSI login credentials applied")
	return nil
}
