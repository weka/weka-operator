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
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"

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

	Builder        *resources.Builder
	ConditionReady *condition.Ready

	Logger logr.Logger

	// -- State dependent components
	// These may be nil depending on where we are int he reconciliation process
	CurrentInstance *wekav1alpha1.Client
	ApiKey          *ApiKey
}

type Reconciler interface {
	Reconcile(ctx context.Context, client *wekav1alpha1.Client) (ctrl.Result, error)
}

type reconcilePhase struct {
	Name      string
	Reconcile func(name types.NamespacedName, client *wekav1alpha1.Client) (Reconciler, error)
}

type patcher func(status *wekav1alpha1.ClientStatus) error

type ApiKey struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
}

func NewClientReconciler(mgr ctrl.Manager) *ClientReconciler {
	return &ClientReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("weka-operator"),

		ApiKey:  &ApiKey{},
		Builder: resources.NewBuilder(mgr.GetScheme()),

		Logger: mgr.GetLogger().WithName("controllers").WithName("Client"),
	}
}

// reconcilePhases is the order in which to reconcile sub-resources
func (r *ClientReconciler) reconcilePhases() []reconcilePhase {
	return []reconcilePhase{
		{
			Name:      "weka-agent",
			Reconcile: r.reconcileAgent,
		},
		{
			Name:      "process_list",
			Reconcile: r.reconcileProcessList,
		},
		{
			Name:      "container_list",
			Reconcile: r.reconcileContainerList,
		},
	}
}

// reconcileAgent reconciles the deployment containing the client and agent
func (r *ClientReconciler) reconcileAgent(name types.NamespacedName, client *wekav1alpha1.Client) (Reconciler, error) {
	r.Recorder.Event(client, v1.EventTypeNormal, "Reconciling", "Reconciling deployment")
	key := runtimeClient.ObjectKeyFromObject(client)

	desired, err := resources.AgentResource(client, key)
	if err != nil {
		return nil, fmt.Errorf("invalid deployment configuration: %w", err)
	}

	if err := controllerutil.SetControllerReference(client, desired, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference: %w", err)
	}
	return NewAgentReconciler(r, desired, name), nil
}

// reconcileApiKey Extracts the API key from the client
func (r *ClientReconciler) reconcileApiKey(name types.NamespacedName, client *wekav1alpha1.Client) (Reconciler, error) {
	executor, err := r.executor(name, client)
	if err != nil {
		return nil, errors.Wrap(err, "unable to get agent reconciler")
	}
	return NewApiKeyReconciler(r, executor), nil
}

// reconcileProcessList Adds `weka ps` to the status
func (r *ClientReconciler) reconcileProcessList(name types.NamespacedName, client *wekav1alpha1.Client) (Reconciler, error) {
	executor, err := r.executor(name, client)
	if err != nil {
		return nil, errors.Wrap(err, "unable to get agent reconciler")
	}
	return NewProcessListReconciler(r, executor), nil
}

// reconcileContainerList Adds `weka cluster container` to the status
func (r *ClientReconciler) reconcileContainerList(name types.NamespacedName, client *wekav1alpha1.Client) (Reconciler, error) {
	executor, err := r.executor(name, client)
	if err != nil {
		return nil, errors.Wrap(err, "unable to get agent reconciler")
	}
	return NewContainerListReconciler(r, executor), nil
}

func (r *ClientReconciler) executor(name types.NamespacedName, client *wekav1alpha1.Client) (Executor, error) {
	key := runtimeClient.ObjectKeyFromObject(client)

	desired, err := resources.AgentResource(client, key)
	if err != nil {
		return nil, fmt.Errorf("invalid deployment configuration: %w", err)
	}

	if err := controllerutil.SetControllerReference(client, desired, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference: %w", err)
	}
	return NewAgentReconciler(r, desired, name), nil
}

//+kubebuilder:rbac:groups=weka.weka.io,resources=clients,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=clients/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=clients/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create

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
	client := &wekav1alpha1.Client{}
	if err := r.Get(ctx, req.NamespacedName, client); err != nil {
		return ctrl.Result{}, runtimeClient.IgnoreNotFound(err)
	}
	r.CurrentInstance = client

	if client.Status.Conditions == nil || len(client.Status.Conditions) == 0 {
		meta.SetStatusCondition(&client.Status.Conditions, metav1.Condition{
			Type:    typeAvailableClient,
			Status:  metav1.ConditionUnknown,
			Reason:  "Initializing",
			Message: "Beginning Reconcialiation",
		})
		if err := r.Status().Update(ctx, client); err != nil {
			r.Logger.Error(err, "Failed to update status")
			return ctrl.Result{}, err
		}
	}

	phases := r.reconcilePhases()
	for _, phase := range phases {
		reconciler, err := phase.Reconcile(req.NamespacedName, client)
		result, err := reconciler.Reconcile(ctx, client)
		if err != nil {
			if apierrors.IsNotFound(err) {
				r.Logger.Info("Resource not found", "phase", phase.Name)
				continue
			}

			errBundle := &multiError.Error{}
			errBundle = multiError.Append(errBundle, err)

			msg := fmt.Sprintf("Failed to reconcile phase %s: %s", phase.Name, err)
			patchErr := r.patchStatus(ctx, client, func(status *wekav1alpha1.ClientStatus) error {
				patcher := r.ConditionReady.PatcherFailed(msg)
				patcher(status)
				return nil
			})
			if apierrors.IsNotFound(patchErr) {
				errBundle = multiError.Append(errBundle, patchErr)
			}

			if err := errBundle.ErrorOrNil(); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to reconcile phase %s: %w", phase.Name, err)
			}
		}

		// non-default Result means a requeue which means that the current phase is not done
		// if result IsZero, then the phase is done or nothing needed to be done
		// and we can move on to the next phase
		if !result.IsZero() {
			r.Logger.Info("Requeueing", "phase", phase.Name)
			return result, nil
		}
	}

	r.RecordEvent(v1.EventTypeNormal, "Reconciled", "Finished Reconciliation")
	meta.SetStatusCondition(&client.Status.Conditions, metav1.Condition{
		Type:    typeAvailableClient,
		Status:  metav1.ConditionTrue,
		Reason:  "Reconciled",
		Message: "Finished Reconciliation",
	})
	return ctrl.Result{}, r.Status().Update(ctx, client)
}

func (r *ClientReconciler) patchStatus(ctx context.Context, client *wekav1alpha1.Client, patcher patcher) error {
	patch := runtimeClient.MergeFrom(client.DeepCopy())
	if err := patcher(&client.Status); err != nil {
		return err
	}
	return r.Status().Patch(ctx, client, patch)
}

func (r *ClientReconciler) RecordEvent(eventtype string, reason string, message string) error {
	if r.CurrentInstance == nil {
		return fmt.Errorf("current client is nil")
	}
	r.Recorder.Event(r.CurrentInstance, v1.EventTypeNormal, reason, message)
	return nil
}

func (r *ClientReconciler) RecordCondition(ctx context.Context, condition metav1.Condition) error {
	if r.CurrentInstance == nil {
		return fmt.Errorf("current client is nil")
	}
	if err := r.Get(ctx, runtimeClient.ObjectKeyFromObject(r.CurrentInstance), r.CurrentInstance); err != nil {
		return errors.Wrap(err, "RecordCondition: failed to get client")
	}
	meta.SetStatusCondition(&r.CurrentInstance.Status.Conditions, condition)
	return r.Status().Update(ctx, r.CurrentInstance)
}

// UpdateStatus sets Status fields on the Client
func (r *ClientReconciler) UpdateStatus(ctx context.Context, updater func(*wekav1alpha1.ClientStatus)) error {
	if r.CurrentInstance == nil {
		return fmt.Errorf("current client is nil")
	}
	if err := r.Get(ctx, runtimeClient.ObjectKeyFromObject(r.CurrentInstance), r.CurrentInstance); err != nil {
		return errors.Wrap(err, "UpdateStatus: failed to get client")
	}
	updater(&r.CurrentInstance.Status)
	return r.Status().Update(ctx, r.CurrentInstance)
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
	r.Logger.Info("Successfully finalized Client")
	return nil
}
