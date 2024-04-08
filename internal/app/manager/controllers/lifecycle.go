package controllers

import (
	"context"
	"fmt"
	"time"

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

// ----------------------------------------------------------------------------
// State Machine
//
// The state machine is a single struct that implements an interface for each
// state.  The GetXXX methods inspect the state of the cluster and return the
// corresponding state if valid.  If the state is not valid, it returns nil and
// an error.
// ----------------------------------------------------------------------------

type ReconcilerStateMachine interface {
	RunLoop(ctx context.Context) (ctrl.Result, error)
	Phases() []ReconciliationPhase

	GetInitState() (InitState, error)
	GetDeletionState() (DeletingState, error)
	GetStartingState() (StartingState, error)

	GetCluster() *wekav1alpha1.WekaCluster
	GetClusterService() services.WekaClusterService
	GetAllocationService() services.AllocationService
	GetCredentialsService() services.CredentialsService
	GetClient() client.Client
	GetRecorder() record.EventRecorder

	HandleInit(ctx context.Context) error
	HandleDeletion(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error
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
	IsReady() bool
	IsCompleted() bool
	Handle(ctx context.Context) (ctrl.Result, error)
	String() string
}

func (r *reconcilerStateMachine) RunLoop(ctx context.Context) (ctrl.Result, error) {
	ctx, logger, done := instrumentation.GetLogSpan(ctx, "ReconcilerStateMachine RunLoop")
	defer done()

	for _, phase := range r.Phases() {
		logger.SetValues("phase", phase.String())

		// Skip any steps that have already been done
		if phase.IsCompleted() {
			logger.Info("Phase already completed")
			continue
		}

		// Execute the phase if its timely (ripe) and not completed
		if !phase.IsReady() {
			logger.Info("Phase not ready")
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, nil
		}

		result, error := phase.Handle(ctx)
		if error != nil {
			return result, error
		}
		if result.Requeue {
			return result, nil
		}
	}

	return ctrl.Result{}, nil
}

func (r *reconcilerStateMachine) Phases() []ReconciliationPhase {
	if r.PhasesList != nil {
		return *r.PhasesList
	}
	return []ReconciliationPhase{
		&PhaseDeletionState{ReconcilerStateMachine: r},
		&PhaseInitState{ReconcilerStateMachine: r},
		&PhaseStartingState{ReconcilerStateMachine: r},
	}
}

func (r *reconcilerStateMachine) GetInitState() (InitState, error) {
	if controllerutil.ContainsFinalizer(r.GetCluster(), WekaFinalizer) {
		return nil, errors.New("Cluster is already initialized")
	}
	return r, nil
}

func (r *reconcilerStateMachine) GetDeletionState() (DeletingState, error) {
	if r.GetCluster().GetDeletionTimestamp() == nil {
		return nil, errors.New("Cluster is not being deleted")
	}
	return r, nil
}

// GetStartingState - The cluster is starting
// This state is currently implicit so we check for the absence of Init and
// Deletion states
func (r *reconcilerStateMachine) GetStartingState() (StartingState, error) {
	if initState, err := r.GetInitState(); initState != nil {
		return nil, err
	}
	if deletionState, err := r.GetDeletionState(); deletionState != nil {
		return nil, err
	}

	return r, nil
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

type InitState interface {
	HandleInit(ctx context.Context) error
}

func (r *reconcilerStateMachine) HandleInit(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleInit")
	defer end()

	wekaCluster := r.GetCluster()

	if !controllerutil.ContainsFinalizer(wekaCluster, WekaFinalizer) {

		wekaCluster.Status.InitStatus()

		err := r.GetClient().Status().Update(ctx, wekaCluster)
		if err != nil {
			logger.Error(err, "failed to init states")
		}

		logger.Info("Adding Finalizer for weka cluster")
		if ok := controllerutil.AddFinalizer(wekaCluster, WekaFinalizer); !ok {
			logger.Info("Failed to add finalizer for wekaCluster")
			return errors.New("Failed to add finalizer for wekaCluster")
		}

		if err := r.GetClient().Update(ctx, wekaCluster); err != nil {
			logger.Error(err, "Failed to update custom resource to add finalizer")
			return err
		}

		if err := r.GetClient().Get(ctx, client.ObjectKey{Namespace: wekaCluster.Namespace, Name: wekaCluster.Name}, wekaCluster); err != nil {
			logger.Error(err, "Failed to re-fetch data")
			return err
		}
		logger.Info("Finalizer added for wekaCluster", "conditions", len(wekaCluster.Status.Conditions))
	}

	return nil
}

type PhaseInitState struct {
	ReconcilerStateMachine
}

func (phase *PhaseInitState) IsReady() bool {
	return !controllerutil.ContainsFinalizer(phase.GetCluster(), WekaFinalizer)
}

func (phase *PhaseInitState) IsCompleted() bool {
	return controllerutil.ContainsFinalizer(phase.GetCluster(), WekaFinalizer)
}

func (r *PhaseInitState) Handle(ctx context.Context) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "PhaseInitState Handle")
	defer end()

	err := r.HandleInit(ctx)

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

func (phase *PhaseDeletionState) IsReady() bool {
	return phase.GetCluster().GetDeletionTimestamp() != nil
}

func (phase *PhaseDeletionState) IsCompleted() bool {
	return phase.GetCluster().GetDeletionTimestamp() == nil
}

func (r *PhaseDeletionState) Handle(ctx context.Context) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "PhaseDeletionState Handle")
	defer end()

	err := r.HandleDeletion(ctx, r.GetCluster())
	logger.SetPhase("CLUSTER_IS_BEING_DELETED")

	return ctrl.Result{}, err
}

func (r *PhaseDeletionState) String() string {
	return "DeletionState"
}

type DeletingState interface {
	HandleDeletion(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error
	DoFinalizerOperationsForwekaCluster(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error
}

func (r *reconcilerStateMachine) HandleDeletion(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error {
	if controllerutil.ContainsFinalizer(wekaCluster, WekaFinalizer) {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "handleDeletion")
		defer end()

		logger.Info("Performing Finalizer Operations for wekaCluster before delete CR")

		// Perform all operations required before remove the finalizer and allow
		// the Kubernetes API to remove the custom resource.
		err := r.DoFinalizerOperationsForwekaCluster(ctx, wekaCluster)
		if err != nil {
			return err
		}

		logger.Info("Removing Finalizer for wekaCluster after successfully perform the operations")
		if ok := controllerutil.RemoveFinalizer(wekaCluster, WekaFinalizer); !ok {
			err := errors.New("Failed to remove finalizer for wekaCluster")
			return err
		}

		if err := r.GetClient().Update(ctx, wekaCluster); err != nil {
			logger.Error(err, "Failed to remove finalizer for wekaCluster")
			return err
		}

	}
	return nil
}

func (r *reconcilerStateMachine) DoFinalizerOperationsForwekaCluster(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error {
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
	allocations, allocConfigMap, err := r.AllocationService.GetOrInitAllocMap(ctx)
	if err != nil {
		logger.Error(err, "Failed to get alloc map")
		return err
	}

	changed := allocator.DeallocateCluster(domain.OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace}, allocations)
	if changed {
		if err := r.AllocationService.UpdateAllocationsConfigmap(ctx, allocations, allocConfigMap); err != nil {
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

// Starting State -----------------------------------------------------------

type StartingState interface{}

type PhaseStartingState struct {
	ReconcilerStateMachine
}

func (phase *PhaseStartingState) IsReady() bool {
	startingState, err := phase.GetStartingState()
	if err != nil {
		return false
	}

	return startingState != nil
}

func (phase *PhaseStartingState) IsCompleted() bool {
	conditions := []string{
		condition.CondClusterSecretsCreated().String(),
		condition.CondPodsCreated,
		condition.CondPodsReady,
		condition.CondClusterCreated,
		condition.CondJoinedCluster,
		condition.CondDrivesAdded,
		condition.CondIoStarted,
		condition.CondEnsureDrivers,
	}
	for _, condition := range conditions {
		if !meta.IsStatusConditionTrue(phase.GetCluster().Status.Conditions, condition) {
			return false
		}
	}
	return true
}

func (r *PhaseStartingState) Handle(ctx context.Context) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "PhaseStartingState Handle")
	defer end()

	logger.SetPhase("CLUSTER_STARTING")

	// This one is more complicated.  It has a bunch of sub-states
	startingState, err := r.GetStartingState()
	if err != nil {
		return ctrl.Result{}, err
	}

	if startingState == nil {
		return ctrl.Result{}, errors.New("Cluster is not in the starting state")
	}

	// wekaCluster := r.GetCluster()
	// generate login credentials
	phases := []IStartingSubState{
		&SecretsNotCreatedCondition{PhaseStartingState: r},
		&PodsNotCreatedCondition{PhaseStartingState: r},
		&ClusterNotCreatedCondition{PhaseStartingState: r},
		&ContainersNotJoinedCondition{PhaseStartingState: r},
		&DrivesNotAddedCondition{PhaseStartingState: r},
		&IoNotStartedCondition{PhaseStartingState: r},
		&SecretsNotAppliedCondition{PhaseStartingState: r},
	}
	for _, phase := range phases {
		err := phase.Handle(ctx)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *PhaseStartingState) String() string {
	return "StartingState"
}

// Sub-States roughly correspond to the conditions in the wekaCluster status

type IStartingSubState interface {
	Handle(ctx context.Context) error
}

type SecretsNotCreatedCondition struct {
	*PhaseStartingState
}

func (c *SecretsNotCreatedCondition) Handle(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "SecretsNotCreatedCondition")
	defer end()

	wekaCluster := c.GetCluster()

	if !meta.IsStatusConditionTrue(c.GetCluster().Status.Conditions, condition.CondClusterSecretsCreated().String()) {
		if err := c.GetCredentialsService().EnsureLoginCredentials(ctx, wekaCluster); err != nil {
			logger.Error(err, "ensureLoginCredentials")
			return err
		}

		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondClusterSecretsCreated().String(),
			Status: metav1.ConditionTrue, Reason: "Init", Message: "Cluster secrets are created",
		})
		_ = c.GetClient().Status().Update(ctx, wekaCluster)
		return nil
	} else {
		logger.SetPhase("CLUSTER_SECRETS_ALREADY_CREATED")
		return nil
	}
}

type PodsNotCreatedCondition struct {
	*PhaseStartingState
}

func (c *PodsNotCreatedCondition) Handle(ctx context.Context) error {
	wekaCluster := c.GetCluster()
	containers, err := c.GetClusterService().EnsureWekaContainers(ctx, wekaCluster)
	if err != nil {
		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondPodsCreated,
			Status: metav1.ConditionFalse, Reason: "Error", Message: err.Error(),
		})
		_ = c.GetClient().Status().Update(ctx, wekaCluster)
		return err
	}
	meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondPodsCreated,
		Status: metav1.ConditionTrue, Reason: "Init", Message: "All pods are created",
	})
	_ = c.GetClient().Status().Update(ctx, wekaCluster)

	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondPodsReady) {
		if ready, err := c.GetClusterService().IsContainersReady(ctx, containers); !ready {
			return err
		}

		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondPodsReady,
			Status: metav1.ConditionTrue, Reason: "Init", Message: "All weka containers are ready for clusterization",
		})
		_ = c.GetClient().Status().Update(ctx, wekaCluster)
	}

	return nil
}

type ClusterNotCreatedCondition struct {
	*PhaseStartingState
}

func (c *ClusterNotCreatedCondition) Handle(ctx context.Context) error {
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
		return err
	}

	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondClusterCreated) {

		if err := c.GetClusterService().Create(ctx, wekaCluster, containers); err != nil {
			logger.Error(err, "Failed to create cluster")

			meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
				Type:   condition.CondClusterCreated,
				Status: metav1.ConditionFalse, Reason: "Error", Message: err.Error(),
			})
			_ = c.GetClient().Status().Update(ctx, wekaCluster)
			return err
		}
		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondClusterCreated,
			Status: metav1.ConditionTrue, Reason: "Init", Message: "Cluster is formed",
		})
		_ = c.GetClient().Status().Update(ctx, wekaCluster)
	}
	return nil
}

type ContainersNotJoinedCondition struct {
	*PhaseStartingState
}

func (r *ContainersNotJoinedCondition) Handle(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleContainersNotJoined")
	defer end()

	wekaCluster := r.GetCluster()
	containers, err := r.GetClusterService().EnsureWekaContainers(ctx, wekaCluster)
	if err != nil {
		return err
	}
	for _, container := range containers {
		if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondJoinedCluster) {
			logger.Info("Container has not joined the cluster yet", "container", container.Name)
			return nil
		} else {
			if wekaCluster.Status.ClusterID == "" {
				wekaCluster.Status.ClusterID = container.Status.ClusterID
				err := r.GetClient().Status().Update(ctx, wekaCluster)
				if err != nil {
					return err
				}
			}
		}
	}

	err = r.GetClusterService().EnsureClusterContainerIds(ctx, wekaCluster, containers)
	if err != nil {
		logger.Info("not all containers are up in the cluster", "err", err)
		return nil
	}

	return nil
}

type DrivesNotAddedCondition struct {
	*PhaseStartingState
}

func (r *DrivesNotAddedCondition) Handle(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleDrivesNotAdded")
	defer end()

	wekaCluster := r.GetCluster()
	logger.SetValues("cluster", r.GetCluster().Name)
	containers, err := r.GetClusterService().EnsureWekaContainers(ctx, wekaCluster)
	if err != nil {
		logger.Error(err, "EnsureWekaContainers")
		return err
	}
	for _, container := range containers {
		if container.Spec.Mode != "drive" {
			continue
		}
		if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondDrivesAdded) {
			logger.Info("Containers did not add drives yet", "container", container.Name)
			return nil
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
			return err
		}
	}

	return nil
}

type IoNotStartedCondition struct {
	*PhaseStartingState
}

func (r *IoNotStartedCondition) Handle(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleIoNotStarted")
	defer end()

	wekaCluster := r.GetCluster()
	containers, err := r.GetClusterService().EnsureWekaContainers(ctx, wekaCluster)
	if err != nil {
		return err
	}

	meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondIoStarted,
		Status: metav1.ConditionUnknown, Reason: "Init", Message: "Starting IO",
	})
	_ = r.GetClient().Status().Update(ctx, wekaCluster)
	logger.Info("Starting IO")
	if err := r.GetClusterService().StartIo(ctx, wekaCluster, containers); err != nil {
		return err
	}
	logger.Info("IO Started, time since create:" + time.Since(wekaCluster.CreationTimestamp.Time).String())
	meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondIoStarted,
		Status: metav1.ConditionTrue, Reason: "Init", Message: "IO is started",
	})
	_ = r.GetClient().Status().Update(ctx, wekaCluster)

	return nil
}

type SecretsNotAppliedCondition struct {
	*PhaseStartingState
}

func (r *SecretsNotAppliedCondition) Handle(ctx context.Context) error {
	prerequisites := []string{
		condition.CondClusterSecretsCreated().String(),
		condition.CondPodsCreated,
		condition.CondPodsReady,
		condition.CondClusterCreated,
		condition.CondJoinedCluster,
		condition.CondDrivesAdded,
		condition.CondIoStarted,
	}
	for _, condition := range prerequisites {
		if !meta.IsStatusConditionTrue(r.GetCluster().Status.Conditions, condition) {
			return nil
		}
	}

	containers, err := r.GetClusterService().EnsureWekaContainers(ctx, r.GetCluster())
	if err != nil {
		return err
	}
	if err := r.GetCredentialsService().ApplyClusterCredentials(ctx, r.GetCluster(), containers); err != nil {
		return err
	}

	wekaCluster := r.GetCluster()
	meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondClusterSecretsApplied().String(),
		Status: metav1.ConditionTrue, Reason: "Init", Message: "Applied cluster secrets",
	})
	wekaCluster.Status.Status = "Ready"
	if err := r.GetClient().Status().Update(ctx, wekaCluster); err != nil {
		return err
	}

	return nil
}

// End Sub-States -----------------------------------------------------------

// Pre-conditions -----------------------------------------------------------

// End Pre-conditions -------------------------------------------------------

// End State Machine --------------------------------------------------------
