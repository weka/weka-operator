package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
)

// WekaManualOperationReconciler reconciles a WekaManualOperation object
type WekaManualOperationReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Mgr        ctrl.Manager
	RestClient rest.Interface
}

func NewWekaManualOperationController(mgr ctrl.Manager, restClient rest.Interface) *WekaManualOperationReconciler {
	return &WekaManualOperationReconciler{
		Mgr:        mgr,
		RestClient: restClient,
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
	}
}

type manualOpLoop struct {
	Operation *weka.WekaManualOperation
	Client    client.Client
	Op        operations.Operation
}

func (r *WekaManualOperationReconciler) RunGC(ctx context.Context) {}

func (r *WekaManualOperationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "WekaManualOperationReconcile", "namespace", req.Namespace, "name", req.Name)
	defer end()

	ctx, cancel := context.WithTimeout(ctx, config.Config.Timeouts.ReconcileTimeout)
	defer cancel()

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

	onFailure := func(ctx context.Context) error {
		wekaManualOperation.Status.Result = loop.Op.GetJsonResult()
		wekaManualOperation.Status.CompletedAt = metav1.Now()
		wekaManualOperation.Status.Status = "Failed"
		return r.Status().Update(ctx, wekaManualOperation)
	}

	var image, imagePullSecret string
	if wekaManualOperation.Spec.Image != nil {
		image = *wekaManualOperation.Spec.Image
	}
	if wekaManualOperation.Spec.ImagePullSecret != nil {
		imagePullSecret = *wekaManualOperation.Spec.ImagePullSecret
	}

	switch wekaManualOperation.Spec.Action {
	case "sign-drives":
		signDrivesOp := operations.NewSignDrivesOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.SignDrives,
			wekaManualOperation,
			weka.WekaContainerDetails{
				Image:           image,
				ImagePullSecret: imagePullSecret,
				Tolerations:     wekaManualOperation.Spec.Tolerations,
				Labels:          wekaManualOperation.ObjectMeta.GetLabels(),
			},
			wekaManualOperation.Status.Status,
			onSuccess,
			onFailure,
			true,
		)
		loop.Op = signDrivesOp
	case "force-resign-drives":
		resignDrivesOp := operations.NewResignDrivesOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.ForceResignDrives,
			wekaManualOperation,
			weka.WekaContainerDetails{
				Image:           image,
				ImagePullSecret: imagePullSecret,
				Tolerations:     wekaManualOperation.Spec.Tolerations,
				Labels:          wekaManualOperation.ObjectMeta.GetLabels(),
			},
			&wekaManualOperation.Status.Status,
			onSuccess,
			onFailure,
		)
		loop.Op = resignDrivesOp
	case "block-drives":
		blockDrivesOp := operations.NewBlockDrivesOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.BlockDrives,
			&wekaManualOperation.Status.Status,
			onSuccess,
			onFailure,
		)
		loop.Op = blockDrivesOp
	case "unblock-drives":
		unblockDrivesOp := operations.NewUnblockDrivesOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.BlockDrives,
			&wekaManualOperation.Status.Status,
			onSuccess,
			onFailure,
		)
		loop.Op = unblockDrivesOp
	case "replace-drives":
		replaceDriveOp := operations.NewReplaceDrivesOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.ReplaceDrives,
			&wekaManualOperation.Status.Status,
			onSuccess,
			onFailure,
		)
		loop.Op = replaceDriveOp
	case "discover-drives":
		discoverDrivesOp := operations.NewDiscoverDrivesOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.DiscoverDrives,
			wekaManualOperation,
			weka.WekaContainerDetails{
				Image:           image,
				ImagePullSecret: imagePullSecret,
				Tolerations:     wekaManualOperation.Spec.Tolerations,
				Labels:          wekaManualOperation.ObjectMeta.GetLabels(),
			},
			wekaManualOperation.Status.Status,
			onSuccess,
			false,
		)
		loop.Op = discoverDrivesOp
	case "remote-traces-session":
		remoteTracesOp := operations.NewMaintainTraceSession(
			r.Mgr,
			r.RestClient,
			wekaManualOperation.Spec.Payload.RemoteTracesSessionConfig,
			wekaManualOperation,
			weka.WekaContainerDetails{
				Image:           image,
				ImagePullSecret: imagePullSecret,
				Tolerations:     wekaManualOperation.Spec.Tolerations,
				Labels:          wekaManualOperation.ObjectMeta.GetLabels(),
			},
		)
		loop.Op = remoteTracesOp
	case "ensure-nics":
		ensureNICsOp := operations.NewEnsureNICsOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.EnsureNICs,
			wekaManualOperation,
			weka.WekaContainerDetails{
				Image:           image,
				ImagePullSecret: imagePullSecret,
				Tolerations:     wekaManualOperation.Spec.Tolerations,
				Labels:          wekaManualOperation.ObjectMeta.GetLabels(),
			},
			wekaManualOperation.Status.Status,
			onSuccess,
		)
		loop.Op = ensureNICsOp
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

	reconSteps := lifecycle.ReconciliationSteps{
		StatusObject: wekaManualOperation,
		Client:       r.Client,
		Steps:        steps,
	}

	return reconSteps.RunAsReconcilerResponse(ctx)
}

// SetupWithManager sets up the controller with the Manager.
func (r *WekaManualOperationReconciler) SetupWithManager(mgr ctrl.Manager, wrappedReconcile reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&weka.WekaManualOperation{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: config.Config.MaxWorkers.WekaManualOperation}).
		Complete(wrappedReconcile)
}
