package controllers

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/kr/pretty"
	"github.com/pkg/errors"
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
	Drives wekav1alpha1.DriveList
}

type NoMatchingDrivesError struct {
	Backend *wekav1alpha1.Backend
}

func (e *NoMatchingDrivesError) Error() string {
	return pretty.Sprintf("no matching drives for backend %s", e.Backend.Name)
}

//+kubebuilder:rbac:groups=weka.weka.io,resources=backends,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=backends/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=backends/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update

func (r *BackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.WithName("Reconcile")
	logger.Info("BackendReconciler.Reconcile() called", "name", req.NamespacedName)

	backend := &wekav1alpha1.Backend{}
	if err := r.Get(ctx, req.NamespacedName, backend); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Refresh drives list
	if err := r.refreshDrives(ctx, backend); err != nil {
		var noMatchingDrivesError *NoMatchingDrivesError
		if errors.As(err, &noMatchingDrivesError) {
			logger.Info("no matching drives", "backend", backend.Name)
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, pretty.Errorf("refreshDrives failed", err, backend)
	}

	return ctrl.Result{}, nil
}

func (r *BackendReconciler) refreshDrives(ctx context.Context, backend *wekav1alpha1.Backend) error {
	logger := r.Logger.WithName("refreshDrives")
	// Gets all drives in the namespace
	drives := &wekav1alpha1.DriveList{}
	if err := r.List(ctx, drives, client.InNamespace(backend.Namespace)); err != nil {
		return pretty.Errorf("List(drives) failed", err)
	}

	// Filter drives for this backend
	backendDrives := wekav1alpha1.DriveList{}
	for _, drive := range drives.Items {
		if drive.Spec.NodeName == backend.Spec.NodeName {
			backendDrives.Items = append(backendDrives.Items, drive)
			driveName := wekav1alpha1.DriveName(drive.Name)
			backend.Status.DriveAssignments[driveName] = nil
		}
	}
	if backendDrives.Items == nil {
		logger.Info("no matching drives", "backend", backend.Name)
		return &NoMatchingDrivesError{Backend: backend}
	}

	logger.Info("found drives", "count", len(backendDrives.Items))
	r.Drives = backendDrives

	return r.Status().Update(ctx, backend)
}

func (r *BackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.Backend{}).
		Complete(r)
}
