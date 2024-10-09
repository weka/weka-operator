package controllers

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// WekaPolicyReconciler reconciles a WekaPolicy object
type WekaPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Mgr    ctrl.Manager
}

func NewWekaPolicyController(mgr ctrl.Manager) *WekaPolicyReconciler {
	return &WekaPolicyReconciler{
		Mgr:    mgr,
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}
}

type policyLoop struct {
	Policy *weka.WekaPolicy
	Client client.Client
	Op     operations.Operation
}

func (r *WekaPolicyReconciler) RunGC(ctx context.Context) {}

//+kubebuilder:rbac:groups=weka.weka.io,resources=wekapolicies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekapolicies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekapolicies/finalizers,verbs=update

func (r *WekaPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "WekaPolicyReconcile", "namespace", req.Namespace, "name", req.Name)
	defer end()

	// Fetch the WekaPolicy instance
	wekaPolicy := &weka.WekaPolicy{}
	err := r.Get(ctx, req.NamespacedName, wekaPolicy)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("WekaPolicy resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get WekaPolicy")
		return ctrl.Result{}, err
	}
	logger.Info("Reconciling WekaPolicy", "type", wekaPolicy.Spec.Type)

	loop := &policyLoop{
		Policy: wekaPolicy,
		Client: r.Client,
	}

	if loop.DurationTillNext() > 0 {
		logger.Info("Policy not ready to run", "requeueAfter", loop.DurationTillNext())
		return ctrl.Result{RequeueAfter: loop.DurationTillNext()}, nil
	}

	if slices.Contains([]string{"Done", ""}, wekaPolicy.Status.Status) {
		wekaPolicy.Status.Status = "Running"
		if wekaPolicy.Status.LastRunTime.IsZero() {
			// put old time to avoid "invalidValue" error
			wekaPolicy.Status.LastRunTime = metav1.NewTime(time.Now().Add(-time.Hour))
		}
		err = r.Status().Update(ctx, wekaPolicy)
		if err != nil {
			logger.Error(err, "Failed to update WekaPolicy status")
			return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
		}
	}

	onSuccess := func(ctx context.Context) error {
		wekaPolicy.Status.LastResult = loop.Op.GetJsonResult()
		wekaPolicy.Status.LastRunTime = metav1.Now()
		wekaPolicy.Status.Status = "Done"
		return r.Status().Update(ctx, wekaPolicy)
	}

	switch wekaPolicy.Spec.Type {
	case "sign-drives":
		signDrivesOp := operations.NewSignDrivesOperation(
			r.Mgr,
			wekaPolicy.Spec.Payload.SignDrives,
			wekaPolicy,
			weka.WekaContainerDetails{
				Image:           wekaPolicy.Spec.Image,
				ImagePullSecret: wekaPolicy.Spec.ImagePullSecret,
				Tolerations:     wekaPolicy.Spec.Tolerations,
				Labels:          wekaPolicy.ObjectMeta.GetLabels(),
			},
			wekaPolicy.Status.Status,
			onSuccess,
		)
		loop.Op = signDrivesOp
	case "discover-drives":
		discoverDrivesOp := operations.NewDiscoverDrivesOperation(
			r.Mgr,
			wekaPolicy.Spec.Payload.DiscoverDrives,
			wekaPolicy,
			weka.WekaContainerDetails{
				Image:           wekaPolicy.Spec.Image,
				ImagePullSecret: wekaPolicy.Spec.ImagePullSecret,
				Tolerations:     wekaPolicy.Spec.Tolerations,
				Labels:          wekaPolicy.ObjectMeta.GetLabels(),
			},
			wekaPolicy.Status.Status,
			onSuccess,
			true,
		)
		loop.Op = discoverDrivesOp
	default:
		return ctrl.Result{}, fmt.Errorf("unknown policy type: %s", wekaPolicy.Spec.Type)
	}

	steps := loop.Op.GetSteps()

	reconSteps := lifecycle.ReconciliationSteps{
		ConditionsObject: wekaPolicy,
		Client:           r.Client,
		Steps:            steps,
	}

	result, err := reconSteps.RunAsReconcilerResponse(ctx)
	if err != nil {
		logger.Error(err, "Error processing policy")
		return result, err
	}

	// If the policy is done, requeue after the specified interval
	if loop.DurationTillNext() > 0 {
		logger.Info("Policy done, requeueing", "requeueAfter", loop.DurationTillNext())
		return ctrl.Result{RequeueAfter: loop.DurationTillNext()}, nil
	}

	return result, nil
}

func (r *policyLoop) DurationTillNext() time.Duration {
	if r.Policy.Status.Status != "Done" {
		return 0
	}
	if r.Policy.Status.LastRunTime.IsZero() {
		return 0
	}

	interval, _ := time.ParseDuration(r.Policy.Spec.Payload.Interval)
	sleepFor := time.Until(r.Policy.Status.LastRunTime.Add(interval))
	return sleepFor
}

// SetupWithManager sets up the controller with the Manager.
func (r *WekaPolicyReconciler) SetupWithManager(mgr ctrl.Manager, wrappedReconcile reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&weka.WekaPolicy{}).
		Owns(&weka.WekaContainer{}).
		Complete(wrappedReconcile)
}
