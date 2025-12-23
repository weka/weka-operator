package wekaclient

import (
	"context"

	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"go.opentelemetry.io/otel/codes"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services/exec"
)

type ClientController struct {
	Manager       ctrl.Manager
	ThrottlingMap throttling.Throttler
	ExecService   exec.ExecService
}

func NewClientController(mgr ctrl.Manager, restClient rest.Interface) *ClientController {
	return &ClientController{
		ThrottlingMap: throttling.NewSyncMapThrottler(),
		Manager:       mgr,
		ExecService:   exec.NewExecService(restClient, mgr.GetConfig()),
	}
}

func (c *ClientController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ClientReconcile", "namespace", req.Namespace, "name", req.Name)
	defer end()

	ctx, cancel := context.WithTimeout(ctx, config.Config.Timeouts.ReconcileTimeout)
	defer cancel()

	wekaClient, err := c.GetClient(ctx, req)
	if err != nil {
		logger.Error(err, "Failed to get wekaClient")
		return ctrl.Result{}, err
	}
	if wekaClient == nil {
		logger.Info("wekaClient resource not found. Ignoring since object must be deleted")
		return ctrl.Result{}, nil
	}

	logger.SetValues("client_uuid", string(wekaClient.GetUID()))
	steps := ClientReconcileSteps(c, wekaClient)
	return steps.RunAsReconcilerResponse(ctx)
}

func (c *ClientController) GetClient(ctx context.Context, req ctrl.Request) (*wekav1alpha1.WekaClient, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetClient", "namespace", req.Namespace, "name", req.Name)
	defer end()

	wekaClient := &wekav1alpha1.WekaClient{}
	if err := c.Manager.GetClient().Get(ctx, req.NamespacedName, wekaClient); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("wekaClient resource not found. Ignoring since object must be deleted")
			return nil, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get wekaClient")
		return nil, err
	}
	logger.SetStatus(codes.Ok, "Fetched wekaClient")
	return wekaClient, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClientController) SetupWithManager(mgr ctrl.Manager, wrappedReconiler reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.WekaClient{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: config.Config.MaxWorkers.WekaClient}).
		Complete(wrappedReconiler)
}
