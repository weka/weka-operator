package controllers

import (
	"context"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	wekav1alpha1 "github.com/weka/weka-operator/internal/app/manager/api/v1alpha1"
)

func NewBackendReconciler(mgr ctrl.Manager) *BackendReconciler {
	return &BackendReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Logger: ctrl.Log.WithName("controllers").WithName("Backend"),
	}
}

type BackendReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Logger logr.Logger
}

//+kubebuilder:rbac:groups=weka.weka.io,resources=backends,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=backends/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=backends/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

func (r *BackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.Logger.Info("BackendReconciler.Reconcile() called")

	backend := &wekav1alpha1.Backend{}
	if err := r.Get(ctx, req.NamespacedName, backend); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// The underlying node should have a drive.weka.io/drive allocation
	// Capture this allocation and create a drive CRD for each
	node := &v1.Node{}
	if err := r.Get(ctx, client.ObjectKey{Name: backend.Spec.NodeName}, node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	drives := node.Status.Allocatable["drive.weka.io/drive"]
	r.Logger.Info("Drives: ", "count", drives.Value())

	return ctrl.Result{}, nil
}

func (r *BackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.Backend{}).
		Complete(r)
}
