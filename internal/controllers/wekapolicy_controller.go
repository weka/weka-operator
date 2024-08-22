package controllers

import (
	"context"
	"fmt"
	"github.com/weka/weka-operator/internal/controllers/operations"
	weka "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"time"
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

	loop := policyLoop{
		Policy: wekaPolicy,
		Client: r.Client,
	}

	onSuccess := func(ctx context.Context) error {
		wekaPolicy.Status.LastResult = loop.Op.GetJsonResult()
		wekaPolicy.Status.LastRunTime = metav1.Now()
		if wekaPolicy.Status.Status == "Done" {
			wekaPolicy.Status.Status = "" // Reset status
		}
		return r.Status().Update(ctx, wekaPolicy)
	}

	switch wekaPolicy.Spec.Type {
	case "sign-drives":
		signDrivesOp := operations.NewSignDrivesOperation(
			r.Mgr,
			wekaPolicy.Spec.Payload.SignDrives,
			wekaPolicy,
			weka.OwnerWekaObject{
				Image:           wekaPolicy.Spec.Image,
				ImagePullSecret: wekaPolicy.Spec.ImagePullSecret,
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
			weka.OwnerWekaObject{
				Image:           wekaPolicy.Spec.Image,
				ImagePullSecret: wekaPolicy.Spec.ImagePullSecret,
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
	if wekaPolicy.Status.Status == "Done" {
		interval, _ := time.ParseDuration(wekaPolicy.Spec.Payload.Interval)
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	return result, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WekaPolicyReconciler) SetupWithManager(mgr ctrl.Manager, wrappedReconcile reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&weka.WekaPolicy{}).
		Owns(&weka.WekaContainer{}).
		Complete(wrappedReconcile)
}
