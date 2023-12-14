/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	wekav1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
	"github.com/weka/weka-operator/controllers/resources"

	multiError "github.com/hashicorp/go-multierror"
	runtimeClient "sigs.k8s.io/controller-runtime/pkg/client"
)

const clientFinalizer = "client.weka.io/finalizer"

const (
	typeAvailableClient   = "Available"
	typeUnavailableClient = "Unavailable"
)

// ClientReconciler reconciles a Client object
type ClientReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	Builder              *resources.Builder
	ModuleReconciler     *ModuleReconciler
	DeploymentReconciler *DeploymentReconciler
}

type reconcilePhase struct {
	Name      string
	Reconcile func(ctx context.Context, client *wekav1alpha1.Client) (ctrl.Result, error)
}

type patcher func(status *wekav1alpha1.ClientStatus) error

// reconcilePhases is the order in which to reconcile sub-resources
func (r *ClientReconciler) reconcilePhases() []reconcilePhase {
	return []reconcilePhase{
		{
			Name:      "wekafsgw",
			Reconcile: r.reconcileWekaFsGw,
		},
		{
			Name:      "wekafsio",
			Reconcile: r.reconcileWekaFsIO,
		},
		{
			Name:      "deployment",
			Reconcile: r.reconcileDeployment,
		},
	}
}

// reconcileWekaFsGw reconciles the wekafsgw driver
func (r *ClientReconciler) reconcileWekaFsGw(ctx context.Context, client *wekav1alpha1.Client) (ctrl.Result, error) {
	key := runtimeClient.ObjectKeyFromObject(client)

	options := &resources.WekaFSModuleOptions{
		ModuleName:          "wekafsgw",
		ModuleLoadingOrder:  []string{},
		ImagePullSecretName: client.Spec.ImagePullSecretName,
		WekaVersion:         client.Spec.Version,
		BackendIP:           client.Spec.Backend.IP,
	}
	desired, err := r.Builder.WekaFSModule(client, key, options)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("invalid driver configuration for wekafsgw: %w", err)
	}
	return ctrl.Result{}, r.ModuleReconciler.Reconcile(ctx, desired)
}

// reconcileWekaFsIO reconciles the wekafsio driver
func (r *ClientReconciler) reconcileWekaFsIO(ctx context.Context, client *wekav1alpha1.Client) (ctrl.Result, error) {
	key := runtimeClient.ObjectKeyFromObject(client)

	options := &resources.WekaFSModuleOptions{
		ModuleName:          "wekafsio",
		ModuleLoadingOrder:  []string{"wekafsio", "wekafsgw"},
		ImagePullSecretName: client.Spec.ImagePullSecretName,
		WekaVersion:         client.Spec.Version,
		BackendIP:           client.Spec.Backend.IP,
	}
	desired, err := r.Builder.WekaFSModule(client, key, options)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("invalid driver configuration for wekafsio: %w", err)
	}
	return ctrl.Result{}, r.ModuleReconciler.Reconcile(ctx, desired)
}

// reconcileDeployment reconciles the deployment containing the client and agent
func (r *ClientReconciler) reconcileDeployment(ctx context.Context, client *wekav1alpha1.Client) (ctrl.Result, error) {
	key := runtimeClient.ObjectKeyFromObject(client)

	desired, err := r.Builder.DeploymentForClient(client, key)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("invalid deployment configuration: %w", err)
	}
	return ctrl.Result{}, r.DeploymentReconciler.Reconcile(ctx, desired)
}

//+kubebuilder:rbac:groups=weka.weka.io,resources=clients,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=clients/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=clients/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Client object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *ClientReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Client", "NamespacedName", req.NamespacedName, "Request", req)

	client := &wekav1alpha1.Client{}
	if err := r.Get(ctx, req.NamespacedName, client); err != nil {
		return ctrl.Result{}, runtimeClient.IgnoreNotFound(err)
	}
	if err := r.patchStatus(ctx, client, r.patcher(ctx, client)); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	phases := r.reconcilePhases()
	for _, phase := range phases {
		result, err := phase.Reconcile(ctx, client)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("Resource not found", "phase", phase.Name)
				continue
			}

			errBundle := &multiError.Error{}
			errBundle = multiError.Append(errBundle, err)

			//msg := fmt.Sprintf("Failed to reconcile phase %s: %s", phase.Name, err)
			//patchErr := r.patchStatus(ctx, client, func(status *wekav1alpha1.ClientStatus) error {
			//patcher := r.ConditionReady.PatcherFailed(msg)
			//patcher(status)
			//return nil
			//})
			//if apiErrors.IsNotFound(patchErr) {
			//errBundle = multiError.Append(errBundle, patchErr)
			//}

			if err := errBundle.ErrorOrNil(); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to reconcile phase %s: %w", phase.Name, err)
			}
		}
		if !result.IsZero() {
			return result, nil
		}
	}

	return ctrl.Result{}, nil
}

func (r *ClientReconciler) patchStatus(ctx context.Context, client *wekav1alpha1.Client, patcher patcher) error {
	patch := runtimeClient.MergeFrom(client.DeepCopy())
	if err := patcher(&client.Status); err != nil {
		return err
	}
	return r.Status().Patch(ctx, client, patch)
}

func (r *ClientReconciler) patcher(ctx context.Context, client *wekav1alpha1.Client) patcher {
	return func(status *wekav1alpha1.ClientStatus) error {
		return nil
	}
}

// TODO: Factor the below  out into reconciler methods
// SetupWithManager sets up the controller with the Manager.
func (r *ClientReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.Client{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

func (r *ClientReconciler) finalizeClient(ctx context.Context, client *wekav1alpha1.Client) error {
	logger := log.FromContext(ctx)
	logger.Info("Successfully finalized Client")
	return nil
}
