package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/operations"
)

// WekaManualOperationReconciler reconciles a WekaManualOperation object
type WekaManualOperationReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Mgr        ctrl.Manager
	RestClient rest.Interface
	Recorder   record.EventRecorder
}

func NewWekaManualOperationController(mgr ctrl.Manager, restClient rest.Interface) *WekaManualOperationReconciler {
	return &WekaManualOperationReconciler{
		Mgr:        mgr,
		RestClient: restClient,
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Recorder:   mgr.GetEventRecorderFor("wekaManualOperation-controller"),
	}
}

type manualOpLoop struct {
	Operation *weka.WekaManualOperation
	Client    client.Client
	Op        operations.Operation
}

func (r *WekaManualOperationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "WekaManualOperationReconcile", "namespace", req.Namespace, "name", req.Name)
	defer logger.End()

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

	onRunning := func(ctx context.Context) error {
		if wekaManualOperation.Status.Status == "" {
			wekaManualOperation.Status.Status = "Running"
			wekaManualOperation.Status.Result = loop.Op.GetJsonResult()
			wekaManualOperation.Status.CompletedAt = metav1.Now()
			return r.Status().Update(ctx, wekaManualOperation)
		}
		return nil
	}

	onSuccess := func(ctx context.Context) error {
		if wekaManualOperation.Status.Status != "Done" {
			wekaManualOperation.Status.Result = loop.Op.GetJsonResult()
			wekaManualOperation.Status.CompletedAt = metav1.Now()
			wekaManualOperation.Status.Status = "Done"
			return r.Status().Update(ctx, wekaManualOperation)
		}
		return nil
	}

	onFailure := func(ctx context.Context) error {
		wekaManualOperation.Status.Result = loop.Op.GetJsonResult()
		wekaManualOperation.Status.CompletedAt = metav1.Now()
		wekaManualOperation.Status.Status = "Failed"
		return r.Status().Update(ctx, wekaManualOperation)
	}

	// onProgress persists the current result without completing the operation, so a subsequent
	// reconcile cycle can read it back (used by the stale-virtual-drives stability gate).
	onProgress := func(ctx context.Context) error {
		if wekaManualOperation.Status.Status == "" {
			wekaManualOperation.Status.Status = "Running"
		}
		// CompletedAt is a required status field, so it must be set on any status write; stamp it
		// once here and let onSuccess/onFailure overwrite it with the real completion time, so the
		// auto-delete delay is measured from actual completion rather than each progress write.
		if wekaManualOperation.Status.CompletedAt.IsZero() {
			wekaManualOperation.Status.CompletedAt = metav1.Now()
		}
		wekaManualOperation.Status.Result = loop.Op.GetJsonResult()
		return r.Status().Update(ctx, wekaManualOperation)
	}

	ownerDetails := ownerDetailsFrom(ownerDetailsInput{
		Image:              wekaManualOperation.Spec.Image,
		ImagePullSecret:    wekaManualOperation.Spec.ImagePullSecret,
		Tolerations:        wekaManualOperation.Spec.Tolerations,
		Labels:             wekaManualOperation.GetLabels(),
		ServiceAccountName: wekaManualOperation.Spec.ServiceAccountName,
	})

	switch wekaManualOperation.Spec.Action {
	case weka.WekaManualOperationActionSignDrives:
		signDrivesOp := operations.NewSignDrivesOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.SignDrives,
			wekaManualOperation,
			ownerDetails,
			wekaManualOperation.Status.Status,
			onSuccess,
			onFailure,
			true,
		)
		loop.Op = signDrivesOp
	case weka.WekaManualOperationActionForceResignDrives:
		resignDrivesOp := operations.NewResignDrivesOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.ForceResignDrives,
			wekaManualOperation,
			ownerDetails,
			&wekaManualOperation.Status.Status,
			onSuccess,
			onFailure,
		)
		loop.Op = resignDrivesOp
	case weka.WekaManualOperationActionBlockDrives:
		blockDrivesOp := operations.NewBlockDrivesOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.BlockDrives,
			&wekaManualOperation.Status.Status,
			onSuccess,
			onFailure,
		)
		loop.Op = blockDrivesOp
	case weka.WekaManualOperationActionUnblockDrives:
		unblockDrivesOp := operations.NewUnblockDrivesOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.BlockDrives,
			&wekaManualOperation.Status.Status,
			onSuccess,
			onFailure,
		)
		loop.Op = unblockDrivesOp
	case weka.WekaManualOperationActionDiscoverDrives:
		discoverDrivesOp := operations.NewDiscoverDrivesOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.DiscoverDrives,
			wekaManualOperation,
			ownerDetails,
			wekaManualOperation.Status.Status,
			onSuccess,
			false,
		)
		loop.Op = discoverDrivesOp
	case weka.WekaManualOperationActionRemoteTracesSession:
		// Apply default duration of 1 week for manual operations if not specified
		payload := wekaManualOperation.Spec.Payload.RemoteTracesSessionConfig
		if payload != nil && payload.Duration.Duration == 0 {
			// Create a copy to avoid modifying the original spec
			payloadCopy := *payload
			payloadCopy.Duration = metav1.Duration{Duration: 7 * 24 * time.Hour} // 1 week
			payload = &payloadCopy
		}

		remoteTracesOp := operations.NewMaintainTraceSession(
			r.Mgr,
			r.RestClient,
			payload,
			wekaManualOperation,
			ownerDetails,
			onRunning,
			onSuccess,
			onFailure,
			false,
		)
		loop.Op = remoteTracesOp
	case weka.WekaManualOperationActionEnsureNICs:
		ensureNICsOp := operations.NewEnsureNICsOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.EnsureNICs,
			wekaManualOperation,
			ownerDetails,
			wekaManualOperation.Status.Status,
			onSuccess,
		)
		loop.Op = ensureNICsOp
	case weka.WekaManualOperationActionCleanStaleVirtualDrives:
		staleVidsOp := operations.NewStaleVirtualDrivesOperation(
			r.Mgr,
			wekaManualOperation.Spec.Payload.CleanStaleVirtualDrives,
			wekaManualOperation,
			r.Recorder,
			onProgress,
			onSuccess,
		)
		loop.Op = staleVidsOp
	default:
		return ctrl.Result{}, fmt.Errorf("unknown operation type: %s", wekaManualOperation.Spec.Action)
	}

	// defaults to 5m
	deletionDelay := 5 * time.Minute
	if wekaManualOperation.Spec.DeletionDelay != nil {
		deletionDelay = wekaManualOperation.Spec.DeletionDelay.Duration
	}

	steps := []lifecycle.Step{
		&lifecycle.SimpleStep{
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
					isMarkedForDeletion := wekaManualOperation.DeletionTimestamp != nil
					isCompleted := wekaManualOperation.Status.Status == "Done"
					timeSinceCompletion := time.Since(wekaManualOperation.Status.CompletedAt.Time)

					return isMarkedForDeletion || (isCompleted && timeSinceCompletion > deletionDelay)
				},
			},
			FinishOnSuccess: true,
		},
	}

	steps = append(steps, loop.Op.AsStep())

	stepsEngine := lifecycle.StepsEngine{
		Steps: steps,
	}

	return stepsEngine.RunAsReconcilerResponse(ctx)
}

// SetupWithManager sets up the controller with the Manager.
func (r *WekaManualOperationReconciler) SetupWithManager(mgr ctrl.Manager, wrappedReconcile reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&weka.WekaManualOperation{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: config.Config.MaxWorkers.WekaManualOperation}).
		Complete(wrappedReconcile)
}
