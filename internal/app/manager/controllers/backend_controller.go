package controllers

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/kr/pretty"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
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
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update

func (r *BackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.Logger.Info("BackendReconciler.Reconcile() called", "name", req.NamespacedName)

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
	if int(drives.Value()) != backend.Status.DriveCount {
		backend.Status.DriveCount = int(drives.Value())
		if err := r.Status().Update(ctx, backend); err != nil {
			return ctrl.Result{}, pretty.Errorf("Status().Update(backend) failed", err, backend)
		}

		return ctrl.Result{Requeue: true}, nil
	}

	if err := r.labelNode(ctx, node, backend); err != nil {
		return ctrl.Result{}, pretty.Errorf("labelNode failed", err, node)
	}

	return ctrl.Result{}, nil
}

func (r *BackendReconciler) labelNode(ctx context.Context, node *v1.Node, backend *wekav1alpha1.Backend) error {
	labels := node.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels["weka.io/backend"] = backend.Name
	node.SetLabels(labels)

	if err := r.Update(ctx, node); err != nil {
		return pretty.Errorf("Update(node) failed", err, node)
	}

	return nil
}

func (r *BackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.Backend{}).
		Complete(r)
}
