package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type ReconcilerStateMachine interface {
	RunLoop(ctx context.Context) (ctrl.Result, error)
	Phases() []ReconciliationPhase

	GetCluster() *wekav1alpha1.WekaCluster
	GetClusterService() services.WekaClusterService
	GetAllocationService() services.AllocationService
	GetCredentialsService() services.CredentialsService
	GetClient() client.Client
	GetRecorder() record.EventRecorder

	SetCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster, condType string, status metav1.ConditionStatus, reason string, message string) error
}

func NewReconcilerStateMachine(
	wekaClusterService services.WekaClusterService,
	allocationService services.AllocationService,
	credentialsService services.CredentialsService,
	client client.Client,
	recorder record.EventRecorder,
	cluster *wekav1alpha1.WekaCluster,
) ReconcilerStateMachine {
	return &reconcilerStateMachine{
		WekaClusterService: wekaClusterService,
		AllocationService:  allocationService,
		CredentialsService: credentialsService,
		Client:             client,
		Recorder:           recorder,
		Cluster:            cluster,
	}
}

type reconcilerStateMachine struct {
	WekaClusterService services.WekaClusterService
	AllocationService  services.AllocationService
	CredentialsService services.CredentialsService
	Client             client.Client
	Recorder           record.EventRecorder
	Cluster            *wekav1alpha1.WekaCluster
	PhasesList         *[]ReconciliationPhase
}

type ReconciliationPhase interface {
	Handle(ctx context.Context) (ctrl.Result, error)
	String() string
}

// InvalidStateError is an error type that is used to indicate that phases were
// applied in an invalid order
type InvalidStateError struct {
	Phase   string
	Message string
}

func (e *InvalidStateError) Error() string {
	return fmt.Sprintf("Invalid state: %s, %s", e.Phase, e.Message)
}

func (r *reconcilerStateMachine) RunLoop(ctx context.Context) (ctrl.Result, error) {
	ctx, logger, done := instrumentation.GetLogSpan(ctx, "ReconcilerStateMachine RunLoop")
	defer done()

	var finalResult ctrl.Result
	var finalError error
	for _, phase := range r.Phases() {
		logger.SetValues("phase", phase.String())

		result, error := phase.Handle(ctx)
		if error != nil {
			finalError = multierror.Append(finalError, error)
		}
		if result.Requeue {
			finalResult = result
		}
	}

	return finalResult, finalError
}

func (r *reconcilerStateMachine) Phases() []ReconciliationPhase {
	if r.PhasesList != nil {
		return *r.PhasesList
	}
	return []ReconciliationPhase{
		&PhaseDeletionState{ReconcilerStateMachine: r},
		&PhaseInitState{ReconcilerStateMachine: r},
		&SecretsNotCreatedCondition{ReconcilerStateMachine: r},
		&PodsNotCreatedCondition{ReconcilerStateMachine: r},
		&ClusterNotCreatedCondition{ReconcilerStateMachine: r},
		&ContainersNotJoinedCondition{ReconcilerStateMachine: r},
		&DrivesNotAddedCondition{ReconcilerStateMachine: r},
		&IoNotStartedCondition{ReconcilerStateMachine: r},
		&SecretsNotAppliedCondition{ReconcilerStateMachine: r},
		&CondDefaultFsCreatedCondition{ReconcilerStateMachine: r},
		&CondS3ClusterCreatedCondition{ReconcilerStateMachine: r},
	}
}

func (r *reconcilerStateMachine) GetCluster() *wekav1alpha1.WekaCluster {
	return r.Cluster
}

func (r *reconcilerStateMachine) GetClusterService() services.WekaClusterService {
	return r.WekaClusterService
}

func (r *reconcilerStateMachine) GetAllocationService() services.AllocationService {
	return r.AllocationService
}

func (r *reconcilerStateMachine) GetCredentialsService() services.CredentialsService {
	return r.CredentialsService
}

func (r *reconcilerStateMachine) GetClient() client.Client {
	return r.Client
}

func (r *reconcilerStateMachine) GetRecorder() record.EventRecorder {
	return r.Recorder
}

// Init State ---------------------------------------------------------------

type PhaseInitState struct {
	ReconcilerStateMachine
}

func (r *PhaseInitState) Handle(ctx context.Context) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "PhaseInitState Handle")
	defer end()

	wekaCluster := r.GetCluster()

	if meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondInitialized) {
		return ctrl.Result{}, nil
	}
	logger.Info("Initializing controller state")

	if !controllerutil.ContainsFinalizer(wekaCluster, WekaFinalizer) {

		logger.Info("Adding Finalizer for weka cluster")
		if ok := controllerutil.AddFinalizer(wekaCluster, WekaFinalizer); !ok {
			logger.Info("Failed to add finalizer for wekaCluster")
			return ctrl.Result{Requeue: true}, errors.New("Failed to add finalizer for wekaCluster")
		}

		if err := r.GetClient().Update(ctx, wekaCluster); err != nil {
			logger.Error(err, "Failed to update custom resource to add finalizer")
			return ctrl.Result{Requeue: true}, err
		}

		if err := r.GetClient().Get(ctx, client.ObjectKey{Namespace: wekaCluster.Namespace, Name: wekaCluster.Name}, wekaCluster); err != nil {
			logger.Error(err, "Failed to re-fetch data")
			return ctrl.Result{Requeue: true}, err
		}
		logger.Info("Finalizer added for wekaCluster", "conditions", len(wekaCluster.Status.Conditions))
	}

	wekaCluster.Status.InitStatus()
	meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondInitialized,
		Status: metav1.ConditionTrue, Reason: "Init", Message: "Initialization Started",
	})
	err := r.GetClient().Status().Update(ctx, wekaCluster)
	if err != nil {
		logger.Error(err, "failed to init states")
		return ctrl.Result{Requeue: true}, err
	}

	logger.SetPhase("CLUSTER_RECONCILE_INITIALIZED")
	return ctrl.Result{}, err
}

func (r *PhaseInitState) String() string {
	return "InitState"
}

// Deletion State -----------------------------------------------------------

type PhaseDeletionState struct {
	ReconcilerStateMachine
}

func (r *PhaseDeletionState) Handle(ctx context.Context) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "PhaseDeletionState Handle")
	defer end()

	if r.GetCluster().GetDeletionTimestamp() == nil {
		return ctrl.Result{}, nil
	}

	wekaCluster := r.GetCluster()
	if controllerutil.ContainsFinalizer(wekaCluster, WekaFinalizer) {
		logger.Info("Performing Finalizer Operations for wekaCluster before delete CR")

		// Perform all operations required before remove the finalizer and allow
		// the Kubernetes API to remove the custom resource.
		err := r.DoFinalizerOperationsForwekaCluster(ctx, wekaCluster)
		if err != nil {
			return ctrl.Result{Requeue: true}, err
		}

		logger.Info("Removing Finalizer for wekaCluster after successfully perform the operations")
		if ok := controllerutil.RemoveFinalizer(wekaCluster, WekaFinalizer); !ok {
			err := errors.New("Failed to remove finalizer for wekaCluster")
			return ctrl.Result{Requeue: true}, err
		}

		if err := r.GetClient().Update(ctx, wekaCluster); err != nil {
			logger.Error(err, "Failed to remove finalizer for wekaCluster")
			return ctrl.Result{Requeue: true}, err
		}

	}
	logger.SetPhase("CLUSTER_IS_BEING_DELETED")

	return ctrl.Result{}, nil
}

func (r *PhaseDeletionState) String() string {
	return "DeletionState"
}

func (r *PhaseDeletionState) DoFinalizerOperationsForwekaCluster(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "DoFinalizerOperationsForwekaCluster")
	defer end()

	if cluster.Spec.Topology == "" {
		return nil
	}
	topology, err := domain.Topologies[cluster.Spec.Topology](ctx, r.GetClient(), cluster.Spec.NodeSelector)
	if err != nil {
		return err
	}
	allocator := domain.NewAllocator(topology)
	allocations, allocConfigMap, err := r.GetAllocationService().GetOrInitAllocMap(ctx)
	if err != nil {
		logger.Error(err, "Failed to get alloc map")
		return err
	}

	changed := allocator.DeallocateCluster(domain.OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace}, allocations)
	if changed {
		if err := r.GetAllocationService().UpdateAllocationsConfigmap(ctx, allocations, allocConfigMap); err != nil {
			logger.Error(err, "Failed to update alloc map")
			return err
		}
	}
	r.GetRecorder().Event(cluster, "Warning", "Deleting",
		fmt.Sprintf("Custom Resource %s is being deleted from the namespace %s",
			cluster.Name,
			cluster.Namespace))
	return nil
}

// Sub-States roughly correspond to the conditions in the wekaCluster status

type SecretsNotCreatedCondition struct {
	ReconcilerStateMachine
}

func (c *SecretsNotCreatedCondition) Handle(ctx context.Context) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "SecretsNotCreatedCondition")
	defer end()

	wekaCluster := c.GetCluster()

	if !meta.IsStatusConditionTrue(c.GetCluster().Status.Conditions, condition.CondClusterSecretsCreated().String()) {
		if err := c.GetCredentialsService().EnsureLoginCredentials(ctx, wekaCluster); err != nil {
			logger.Error(err, "ensureLoginCredentials")
			return ctrl.Result{Requeue: true}, err
		}

		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondClusterSecretsCreated().String(),
			Status: metav1.ConditionTrue, Reason: "Init", Message: "Cluster secrets are created",
		})
		_ = c.GetClient().Status().Update(ctx, wekaCluster)
		return ctrl.Result{}, nil
	} else {
		logger.SetPhase("CLUSTER_SECRETS_ALREADY_CREATED")
		return ctrl.Result{}, nil
	}
}

func (c *SecretsNotCreatedCondition) String() string {
	return "SecretsNotCreatedCondition"
}

type PodsNotCreatedCondition struct {
	ReconcilerStateMachine
}

func (c *PodsNotCreatedCondition) String() string {
	return "PodsNotCreatedCondition"
}

func (c *PodsNotCreatedCondition) Handle(ctx context.Context) (ctrl.Result, error) {
	wekaCluster := c.GetCluster()
	containers, err := c.GetClusterService().EnsureWekaContainers(ctx, wekaCluster)
	if err != nil {
		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondPodsCreated,
			Status: metav1.ConditionFalse, Reason: "Error", Message: err.Error(),
		})
		_ = c.GetClient().Status().Update(ctx, wekaCluster)
		return ctrl.Result{Requeue: true}, err
	}
	meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondPodsCreated,
		Status: metav1.ConditionTrue, Reason: "Init", Message: "All pods are created",
	})
	_ = c.GetClient().Status().Update(ctx, wekaCluster)

	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondPodsReady) {
		if ready, err := c.GetClusterService().IsContainersReady(ctx, containers); !ready {
			return ctrl.Result{Requeue: true}, err
		}

		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondPodsReady,
			Status: metav1.ConditionTrue, Reason: "Init", Message: "All weka containers are ready for clusterization",
		})
		_ = c.GetClient().Status().Update(ctx, wekaCluster)
	}

	return ctrl.Result{}, nil
}

type ClusterNotCreatedCondition struct {
	ReconcilerStateMachine
}

func (c *ClusterNotCreatedCondition) String() string {
	return "ClusterNotCreatedCondition"
}

func (c *ClusterNotCreatedCondition) Handle(ctx context.Context) (ctrl.Result, error) {
	wekaCluster := c.GetCluster()
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleClusterNotCreated")
	defer end()

	containers, err := c.GetClusterService().EnsureWekaContainers(ctx, wekaCluster)
	if err != nil {
		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondClusterCreated,
			Status: metav1.ConditionFalse, Reason: "Error", Message: err.Error(),
		})
		_ = c.GetClient().Status().Update(ctx, wekaCluster)
		return ctrl.Result{Requeue: true}, err
	}

	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondClusterCreated) {

		if err := c.GetClusterService().Create(ctx, wekaCluster, containers); err != nil {
			logger.Error(err, "Failed to create cluster")

			meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
				Type:   condition.CondClusterCreated,
				Status: metav1.ConditionFalse, Reason: "Error", Message: err.Error(),
			})
			_ = c.GetClient().Status().Update(ctx, wekaCluster)
			return ctrl.Result{Requeue: true}, err
		}
		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondClusterCreated,
			Status: metav1.ConditionTrue, Reason: "Init", Message: "Cluster is formed",
		})
		_ = c.GetClient().Status().Update(ctx, wekaCluster)
	}
	return ctrl.Result{}, nil
}

type ContainersNotJoinedCondition struct {
	ReconcilerStateMachine
}

func (r *ContainersNotJoinedCondition) String() string {
	return "ContainersNotJoinedCondition"
}

func (r *ContainersNotJoinedCondition) Handle(ctx context.Context) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleContainersNotJoined")
	defer end()

	wekaCluster := r.GetCluster()
	containers, err := r.GetClusterService().EnsureWekaContainers(ctx, wekaCluster)
	if err != nil {
		return ctrl.Result{Requeue: true}, err
	}
	for _, container := range containers {
		if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondJoinedCluster) {
			logger.Info("Container has not joined the cluster yet", "container", container.Name)

			msg := fmt.Sprintf("Container %s has not joined the cluster yet", container.Name)
			if err := r.SetCondition(ctx, wekaCluster, condition.CondJoinedCluster, metav1.ConditionFalse, "Init", msg); err != nil {
				logger.Error(err, "SetCondition")
			}
			if err := r.GetClient().Status().Update(ctx, wekaCluster); err != nil {
				logger.Error(err, "Status update wekaCluster")
			}

			return ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, nil
		} else {
			if wekaCluster.Status.ClusterID == "" {
				wekaCluster.Status.ClusterID = container.Status.ClusterID
				err := r.GetClient().Status().Update(ctx, wekaCluster)
				if err != nil {
					return ctrl.Result{Requeue: true}, err
				}
			}
		}
	}

	err = r.GetClusterService().EnsureClusterContainerIds(ctx, wekaCluster, containers)
	if err != nil {
		logger.Info("not all containers are up in the cluster", "err", err)
		return ctrl.Result{}, nil
	}

	if err := r.SetCondition(ctx, wekaCluster, condition.CondJoinedCluster, metav1.ConditionTrue, "Init", "All containers have joined the cluster"); err != nil {
		logger.Error(err, "SetCondition")
	}
	if err := r.GetClient().Status().Update(ctx, wekaCluster); err != nil {
		logger.Error(err, "Status update wekaCluster")
	}

	return ctrl.Result{}, nil
}

type DrivesNotAddedCondition struct {
	ReconcilerStateMachine
}

func (r *DrivesNotAddedCondition) String() string {
	return "DrivesNotAddedCondition"
}

func (r *DrivesNotAddedCondition) Handle(ctx context.Context) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleDrivesNotAdded")
	defer end()

	wekaCluster := r.GetCluster()
	logger.SetValues("cluster", r.GetCluster().Name)
	containers, err := r.GetClusterService().EnsureWekaContainers(ctx, wekaCluster)
	if err != nil {
		logger.Error(err, "EnsureWekaContainers")
		return ctrl.Result{Requeue: true}, err
	}
	for _, container := range containers {
		if container.Spec.Mode != "drive" {
			continue
		}
		if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondDrivesAdded) {
			logger.Info("Containers did not add drives yet", "container", container.Name)
			return ctrl.Result{}, nil
		}
	}
	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondDrivesAdded) {
		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondDrivesAdded,
			Status: metav1.ConditionTrue, Reason: "Init", Message: "All drives are added",
		})
		err = r.GetClient().Status().Update(ctx, wekaCluster)
		if err != nil {
			logger.Error(err, "Status update wekaCluster")
			return ctrl.Result{Requeue: true}, err
		}
	}

	return ctrl.Result{}, nil
}

type IoNotStartedCondition struct {
	ReconcilerStateMachine
}

func (r *IoNotStartedCondition) String() string {
	return "IoNotStartedCondition"
}

func (r *IoNotStartedCondition) Handle(ctx context.Context) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleIoNotStarted")
	defer end()

	if meta.IsStatusConditionTrue(r.GetCluster().Status.Conditions, condition.CondIoStarted) {
		return ctrl.Result{}, nil
	}

	prerequisites := []string{
		condition.CondClusterSecretsCreated().String(),
		condition.CondPodsCreated,
		condition.CondPodsReady,
		condition.CondClusterCreated,
		condition.CondJoinedCluster,
		condition.CondDrivesAdded,
	}

	wekaCluster := r.GetCluster()

	for _, c := range prerequisites {
		if !meta.IsStatusConditionTrue(r.GetCluster().Status.Conditions, c) {
			logger.Info("Prerequisite not met", "prerequisite", c)
			msg := fmt.Sprintf("Prerequisite %s not met", c)
			r.SetCondition(ctx, wekaCluster, condition.CondIoStarted, metav1.ConditionFalse, "Init", msg)
			if err := r.GetClient().Status().Update(ctx, wekaCluster); err != nil {
				logger.Error(err, "Status update wekaCluster")
			}
			return ctrl.Result{Requeue: true, RequeueAfter: 30 * time.Second}, nil
		}
	}
	logger.Info("Prerequisites are met, starting IO")

	containers, err := r.GetClusterService().EnsureWekaContainers(ctx, wekaCluster)
	if err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondIoStarted,
		Status: metav1.ConditionUnknown, Reason: "Init", Message: "Starting IO",
	})
	_ = r.GetClient().Status().Update(ctx, wekaCluster)
	logger.Info("Starting IO")
	if err := r.GetClusterService().StartIo(ctx, wekaCluster, containers); err != nil {
		return ctrl.Result{Requeue: true}, err
	}
	logger.Info("IO Started, time since create:" + time.Since(wekaCluster.CreationTimestamp.Time).String())
	meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondIoStarted,
		Status: metav1.ConditionTrue, Reason: "Init", Message: "IO is started",
	})
	_ = r.GetClient().Status().Update(ctx, wekaCluster)

	return ctrl.Result{}, nil
}

type SecretsNotAppliedCondition struct {
	ReconcilerStateMachine
}

func (r *SecretsNotAppliedCondition) String() string {
	return "SecretsNotAppliedCondition"
}

func (r *SecretsNotAppliedCondition) Handle(ctx context.Context) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "SecretsNotAppliedCondition")
	defer end()

	cluster := r.GetCluster()
	if meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondClusterSecretsApplied().String()) {
		return ctrl.Result{}, nil
	}

	prerequisites := []string{
		condition.CondClusterSecretsCreated().String(),
		condition.CondPodsCreated,
		condition.CondPodsReady,
		condition.CondClusterCreated,
		condition.CondJoinedCluster,
		condition.CondDrivesAdded,
		condition.CondIoStarted,
	}
	for _, c := range prerequisites {
		if !meta.IsStatusConditionTrue(r.GetCluster().Status.Conditions, c) {
			logger.Info("Prerequisite not met", "prerequisite", c)

			msg := fmt.Sprintf("Prerequisite %s not met", c)
			r.SetCondition(ctx,
				cluster,
				condition.CondClusterSecretsApplied().String(),
				metav1.ConditionFalse,
				"Init",
				msg,
			)
			if err := r.GetClient().Status().Update(ctx, cluster); err != nil {
				logger.Error(err, "Status update wekaCluster")
			}
			return ctrl.Result{Requeue: true, RequeueAfter: 30 * time.Second}, nil
		}
	}
	logger.Info("Prequsites are met, applying secrets")

	containers, err := r.GetClusterService().EnsureWekaContainers(ctx, r.GetCluster())
	if err != nil {
		return ctrl.Result{Requeue: true}, err
	}
	if err := r.GetCredentialsService().ApplyClusterCredentials(ctx, r.GetCluster(), containers); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	wekaCluster := r.GetCluster()
	meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondClusterSecretsApplied().String(),
		Status: metav1.ConditionTrue, Reason: "Init", Message: "Applied cluster secrets",
	})
	wekaCluster.Status.Status = "Ready"
	if err := r.GetClient().Status().Update(ctx, wekaCluster); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	return ctrl.Result{}, nil
}

type CondDefaultFsCreatedCondition struct {
	ReconcilerStateMachine
}

func (r *CondDefaultFsCreatedCondition) String() string {
	return condition.CondDefaultFsCreated
}

func (r *CondDefaultFsCreatedCondition) Handle(ctx context.Context) (ctrl.Result, error) {
	wekaCluster := r.GetCluster()

	// Must go after secrets applied condition
	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondClusterSecretsApplied().String()) {

		r.SetCondition(
			ctx,
			wekaCluster,
			condition.CondDefaultFsCreated,
			metav1.ConditionFalse,
			"Init",
			"Secrets not applied yet",
		)
		_ = r.GetClient().Status().Update(ctx, wekaCluster)

		return ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, nil
	}

	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondDefaultFsCreated) {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "CondDefaultFsCreatedCondition")
		defer end()

		containers, err := r.GetClusterService().EnsureWekaContainers(ctx, wekaCluster)
		if err != nil {
			logger.Error(err, "EnsureWekaContainers")
			return ctrl.Result{Requeue: true}, err
		}

		logger.SetPhase("CONFIGURING_DEFAULT_FS")
		if err := r.GetClusterService().EnsureDefaultFs(ctx, containers[0]); err != nil {
			logger.Error(err, "EnsureDefaultFs")
			return ctrl.Result{}, err
		}
		_ = r.SetCondition(ctx, wekaCluster, condition.CondDefaultFsCreated, metav1.ConditionTrue, "Init", "Created default filesystem")
		err = r.GetClient().Status().Update(ctx, wekaCluster)
		if err != nil {
			logger.Error(err, "Status update wekaCluster")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// -- S3 Cluster Condition ---------------------------------------------------

type CondS3ClusterCreatedCondition struct {
	ReconcilerStateMachine
}

func (r *CondS3ClusterCreatedCondition) String() string {
	return condition.CondS3ClusterCreated
}

func (r *CondS3ClusterCreatedCondition) Handle(ctx context.Context) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "CondS3ClusterCreatedCondition")
	defer end()

	wekaCluster := r.GetCluster()
	if meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondS3ClusterCreated) {
		return ctrl.Result{}, nil
	}

	prerequisites := []string{
		// Not sure if this is an actual prerequisite or just before it in the
		// legacy code
		condition.CondDefaultFsCreated,
	}

	for _, c := range prerequisites {
		if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, c) {
			logger.Info("Prerequisite not met", "prerequisite", c)
			msg := fmt.Sprintf("Prerequisite %s not met", c)
			if err := r.SetCondition(
				ctx, wekaCluster, condition.CondS3ClusterCreated, metav1.ConditionFalse, "Init", msg,
			); err != nil {
				logger.Error(err, "SetCondition")
			}

			if err := r.GetClient().Status().Update(ctx, wekaCluster); err != nil {
				logger.Error(err, "Status update wekaCluster")
			}

			return ctrl.Result{Requeue: true, RequeueAfter: 30 * time.Second}, nil
		}
	}
	logger.Info("Prerequisites are met, creating S3 cluster")

	logger.SetPhase("CONFIGURING_DEFAULT_FS")
	containers, err := r.GetClusterService().EnsureWekaContainers(ctx, wekaCluster)
	if err != nil {
		logger.Error(err, "EnsureWekaContainers")
		return ctrl.Result{Requeue: true}, err
	}

	containers = r.selectS3Containers(containers)
	if len(containers) > 0 {
		err := r.GetClusterService().EnsureS3Cluster(ctx, containers)
		if err != nil {
			return ctrl.Result{}, err
		}
		_ = r.SetCondition(ctx, wekaCluster, condition.CondS3ClusterCreated, metav1.ConditionTrue, "Init", "Created S3 cluster")
		err = r.GetClient().Status().Update(ctx, wekaCluster)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *CondS3ClusterCreatedCondition) selectS3Containers(containers []*wekav1alpha1.WekaContainer) []*wekav1alpha1.WekaContainer {
	var s3Containers []*wekav1alpha1.WekaContainer
	for _, container := range containers {
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeS3 {
			s3Containers = append(s3Containers, container)
		}
	}
	return s3Containers
}

// -- Utils -----------------------------------------------------------------

func (r *reconcilerStateMachine) SetCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster,
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
	err := r.GetClient().Status().Update(ctx, cluster)
	if err != nil {
		return err
	}

	return nil
}
