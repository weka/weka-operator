package controllers

import (
	"context"
	"fmt"
	"time"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// WekaManualOperationReconciler reconciles a WekaManualOperation object
type WekaManualOperationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Mgr    ctrl.Manager
}

func NewWekaManualOperationController(mgr ctrl.Manager) *WekaManualOperationReconciler {
	return &WekaManualOperationReconciler{
		Mgr:    mgr,
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}
}

type manualOpLoop struct {
	Operation *weka.WekaManualOperation
	Client    client.Client
	Op        operations.Operation
}

func (r *WekaManualOperationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "WekaManualOperationReconcile", "namespace", req.Namespace, "name", req.Name)
	defer end()

	// Fetch the WekaManualOperation instance
	wekaManualOperation := &weka.WekaManualOperation{}
	err := r.Get(ctx, req.NamespacedName, wekaManualOperation)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("WekaManualOperation resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get WekaManualOperation")
		return ctrl.Result{}, err
	}
	logger.Info("Reconciling WekaManualOperation", "action", wekaManualOperation.Spec.Action)

	loop := manualOpLoop{
		Operation: wekaManualOperation,
		Client:    r.Client,
	}

	onSuccess := func(ctx context.Context) error {
		wekaManualOperation.Status.Result = loop.Op.GetJsonResult()
		wekaManualOperation.Status.CompletedAt = metav1.Now()
		wekaManualOperation.Status.Status = "Done"
		return r.Status().Update(ctx, wekaManualOperation)
	}

	switch wekaManualOperation.Spec.Action {
	case "sign-drives":
		signDrivesOp := operations.NewSignDrivesOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.SignDrives,
			wekaManualOperation,
			weka.WekaContainerDetails{
				Image:           wekaManualOperation.Spec.Image,
				ImagePullSecret: wekaManualOperation.Spec.ImagePullSecret,
				Tolerations:     wekaManualOperation.Spec.Tolerations,
			},
			wekaManualOperation.Status.Status,
			onSuccess,
		)
		loop.Op = signDrivesOp
	case "block-drives":
		blockDrivesOp := operations.NewBlockDrivesOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.BlockDrives,
		)
		loop.Op = blockDrivesOp
	case "discover-drives":
		discoverDrivesOp := operations.NewDiscoverDrivesOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.DiscoverDrives,
			wekaManualOperation,
			weka.WekaContainerDetails{
				Image:           wekaManualOperation.Spec.Image,
				ImagePullSecret: wekaManualOperation.Spec.ImagePullSecret,
				Tolerations:     wekaManualOperation.Spec.Tolerations,
			},
			wekaManualOperation.Status.Status,
			onSuccess,
			true,
		)
		loop.Op = discoverDrivesOp
	default:
		return ctrl.Result{}, fmt.Errorf("unknown operation type: %s", wekaManualOperation.Spec.Action)
	}

	steps := []lifecycle.Step{
		{
			Name: "DeleteSelf",
			Run: func(ctx context.Context) error {
				err := r.Delete(ctx, wekaManualOperation)
				if err != nil {
					logger.Error(err, "Failed to delete WekaManualOperation")
				}
				return err
			},
			Predicates: lifecycle.Predicates{
				func() bool {
					return wekaManualOperation.DeletionTimestamp != nil || (wekaManualOperation.Status.Status == "Done" && time.Since(wekaManualOperation.Status.CompletedAt.Time) > 5*time.Minute)
				},
			},
			FinishOnSuccess:           true,
			ContinueOnPredicatesFalse: true,
		},
	}

	steps = append(steps, loop.Op.AsStep())
	switch wekaManualOperation.Spec.Action {
	case "block-drives":
		// block drive operation does not have a success callback, as it also does not have cleanup
		steps = append(steps, lifecycle.Step{
			Name: "UpdateSuccess",
			Run:  onSuccess,
		})
	}

	reconSteps := lifecycle.ReconciliationSteps{
		ConditionsObject: wekaManualOperation,
		Client:           r.Client,
		Steps:            steps,
	}

	return reconSteps.RunAsReconcilerResponse(ctx)
}

// SetupWithManager sets up the controller with the Manager.
func (r *WekaManualOperationReconciler) SetupWithManager(mgr ctrl.Manager, wrappedReconcile reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&weka.WekaManualOperation{}).
		Owns(&weka.WekaContainer{}).
		Complete(wrappedReconcile)
}
