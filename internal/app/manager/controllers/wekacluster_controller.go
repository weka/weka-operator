package controllers

import (
	"context"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/cluster"
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/factory"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	werrors "github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	"github.com/pkg/errors"
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

	Steps *lifecycle.ReconciliationSteps[*wekav1alpha1.WekaCluster]
}

func NewWekaClusterController(mgr ctrl.Manager) *WekaClusterReconciler {
	client := mgr.GetClient()
	config := mgr.GetConfig()
	scheme := mgr.GetScheme()

	recorder := mgr.GetEventRecorderFor("wekaCluster-controller")

	crdManager := services.NewCrdManager(mgr)
	execService := services.NewExecService(config)
	secretsService := services.NewSecretsService(client, scheme, execService)

	state := &cluster.ClusterState{
		ReconciliationState: lifecycle.ReconciliationState[*wekav1alpha1.WekaCluster]{
			Subject:    &wekav1alpha1.WekaCluster{},
			Conditions: &[]metav1.Condition{},
		},

		Client:         client,
		CrdManager:     crdManager,
		ExecService:    execService,
		Manager:        mgr,
		Recorder:       recorder,
		SecretsService: secretsService,
	}

	return &WekaClusterReconciler{
		Client:   client,
		Scheme:   scheme,
		Manager:  mgr,
		Recorder: mgr.GetEventRecorderFor("wekaCluster-controller"),

		CrdManager:           services.NewCrdManager(mgr),
		SecretsService:       services.NewSecretsService(client, scheme, execService),
		ExecService:          execService,
		WekaContainerFactory: factory.NewWekaContainerFactory(scheme),

		Steps: state.StandardReconciliationSteps(ClusterStatusInit, WekaFinalizer),
	}
}

func (r *WekaClusterReconciler) Reconcile(initContext context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(initContext, "WekaClusterReconcile", "namespace", req.Namespace, "cluster_name", req.Name)
	defer end()

	// generate login credentials

	if r.Steps == nil {
		panic("Steps not initialized")
	}
	r.Steps.State.Request = req
	if err := r.Steps.Reconcile(ctx); err != nil {
		var retryableError *werrors.RetryableError
		if errors.As(err, &retryableError) {
			logger.Debug("Requeue reconciliation", "err", err)
			return ctrl.Result{Requeue: true, RequeueAfter: retryableError.RetryAfter}, nil
		}
		logger.Error(err, "Failed to reconcile cluster")
		return ctrl.Result{}, err
	}

	// Note: All use of conditions is only as hints for skipping actions and a visibility, not strictly a state machine
	// All code should be idempotent and not rely on conditions for correctness, hence validation of succesful update of conditions is not done

	logger.SetPhase("CLUSTER_READY")
	var err error
	wekaCluster := r.Steps.State.Subject

	err = r.HandleUpgrade(ctx, wekaCluster)
	if err != nil {
		// TODO: separate unknown from expected reconcilation errors for info/error logging,
		// right now err is swallowed as meaningless for known cases
		logger.Info("upgrade in process", "lastErr", err)
		return ctrl.Result{RequeueAfter: time.Second * 3}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WekaClusterReconciler) SetupWithManager(mgr ctrl.Manager, wrappedReconcile reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.WekaCluster{}).
		Owns(&wekav1alpha1.WekaContainer{}).
		Complete(wrappedReconcile)
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
