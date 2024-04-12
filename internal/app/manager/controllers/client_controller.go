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
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/util"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"strings"
	"time"

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
	multiError "github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	runtimeClient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	typeAvailableClient   = "Available"
	typeUnavailableClient = "Unavailable"
)

// ClientReconciler reconciles a Client object
type ClientReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	ConditionReady *condition.Ready

	Logger logr.Logger

	// -- State dependent components
	// These may be nil depending on where we are int he reconciliation process
	CurrentInstance *wekav1alpha1.WekaClient
}

func (r *ClientReconciler) getLogSpan(ctx context.Context, names ...string) (context.Context, instrumentation.LogSpan) {
	logger := r.Logger
	joinNames := strings.Join(names, ".")
	ctx, span := instrumentation.Tracer.Start(ctx, joinNames)
	if span != nil {
		traceID := span.SpanContext().TraceID().String()
		spanID := span.SpanContext().SpanID().String()
		logger = logger.WithValues("trace_id", traceID, "span_id", spanID)
		for _, name := range names {
			logger = logger.WithName(name)
		}
	}

	ShutdownFunc := func(opts ...trace.SpanEndOption) {
		if span != nil {
			span.End(opts...)
		}
		logger.Info(fmt.Sprintf("%s finished", joinNames))
	}

	ls := instrumentation.LogSpan{
		Logger: logger,
		Span:   span,
		End:    ShutdownFunc,
	}
	logger.Info(fmt.Sprintf("%s called", joinNames))
	return ctx, ls
}

type PhaseReconciler interface {
	Reconcile(ctx context.Context, client *wekav1alpha1.WekaClient) (ctrl.Result, error)
}

type reconcilePhase struct {
	Name      string
	Reconcile func(name types.NamespacedName, client *wekav1alpha1.WekaClient) (PhaseReconciler, error)
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
		Logger:   mgr.GetLogger().WithName("controllers").WithName("WekaClient"),
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
func (r *ClientReconciler) reconcileAgent(name types.NamespacedName, client *wekav1alpha1.WekaClient) (PhaseReconciler, error) {
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
func (r *ClientReconciler) reconcileApiKey(name types.NamespacedName, client *wekav1alpha1.WekaClient) (PhaseReconciler, error) {
	executor, err := r.executor(name, client)
	if err != nil {
		return nil, errors.Wrap(err, "unable to get agent reconciler")
	}
	return NewApiKeyReconciler(r, executor), nil
}

// reconcileProcessList Adds `weka ps` to the status
func (r *ClientReconciler) reconcileProcessList(name types.NamespacedName, client *wekav1alpha1.WekaClient) (PhaseReconciler, error) {
	executor, err := r.executor(name, client)
	if err != nil {
		return nil, errors.Wrap(err, "unable to get agent reconciler")
	}
	return NewProcessListReconciler(r, executor), nil
}

// reconcileContainerList Adds `weka cluster container` to the status
func (r *ClientReconciler) reconcileContainerList(name types.NamespacedName, client *wekav1alpha1.WekaClient) (PhaseReconciler, error) {
	executor, err := r.executor(name, client)
	if err != nil {
		return nil, errors.Wrap(err, "unable to get agent reconciler")
	}
	return NewContainerListReconciler(r, executor), nil
}

func (r *ClientReconciler) executor(name types.NamespacedName, client *wekav1alpha1.WekaClient) (Executor, error) {
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

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the WekaClient object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile

func (r *ClientReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger := r.getLogSpan(ctx, "Reconcile", fmt.Sprintf("%s/%s", req.Namespace, req.Name))
	defer logger.End()
	wekaClient, err := GetClient(ctx, req, r.Client, r.Logger)
	if err != nil {
		logger.Error(err, "Failed to get wekaClient")
		return ctrl.Result{}, err
	}
	if wekaClient == nil {
		logger.Info("wekaClient resource not found. Ignoring since object must be deleted")
		return ctrl.Result{}, nil
	}

	err = r.initState(ctx, wekaClient)
	if err != nil {
		logger.Error(err, "Failed to initialize state")
		return ctrl.Result{}, err
	}

	if wekaClient.GetDeletionTimestamp() != nil {
		err = r.handleDeletion(ctx, wekaClient)
		if err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("Deleting wekaClient")
		return ctrl.Result{}, nil
	}

	applicableNodes, err := getNodesByLabels(ctx, r.Client, wekaClient.Spec.NodeSelector)
	if err != nil {
		logger.Error(err, "Failed to get applicable nodes by labels")
		return ctrl.Result{}, err
	}

	result, containers, err := r.ensureWekaContainers(ctx, wekaClient, applicableNodes)
	if err != nil {
		logger.Error(err, "Failed to ensure weka containers")
		return result, err
	}
	if result.Requeue {
		return result, nil
	}

	_ = containers
	return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, nil
}

func (r *ClientReconciler) OriginalReconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger := r.getLogSpan(ctx, "Reconcile", fmt.Sprintf("%s/%s", req.Namespace, req.Name))
	defer logger.End()
	r.Logger.Info("Reconciling WekaClient")
	client := &wekav1alpha1.WekaClient{}
	if err := r.Get(ctx, req.NamespacedName, client); err != nil {
		return ctrl.Result{}, runtimeClient.IgnoreNotFound(err)
	}
	r.CurrentInstance = client

	if client.Status.Conditions == nil || len(client.Status.Conditions) == 0 {
		meta.SetStatusCondition(&client.Status.Conditions, metav1.Condition{
			Type:    typeAvailableClient,
			Status:  metav1.ConditionUnknown,
			Reason:  "Initializing",
			Message: "Beginning Reconciliation",
		})
		if err := r.Status().Update(ctx, client); err != nil {
			r.Logger.Error(err, "Failed to update status")
			return ctrl.Result{}, err
		}
	}

	phases := r.reconcilePhases()
	for _, phase := range phases {
		logger.SetPhase(phase.Name)
		reconciler, err := phase.Reconcile(req.NamespacedName, client)
		if err != nil {
			logger.WithValues("phase", phase.Name).Error(err, "Failed to get reconciler for phase")
			return ctrl.Result{}, fmt.Errorf("failed to get reconciler for phase %s: %w", phase.Name, err)
		}
		result, err := reconciler.Reconcile(ctx, client)
		if err != nil {
			if apierrors.IsNotFound(err) {
				r.Logger.Info("Resource not found", "phase", phase.Name)
				continue
			}
			errBundle := &multiError.Error{}
			errBundle = multiError.Append(errBundle, err)
			logger.WithValues("phase", phase.Name).Error(err, "Failed to reconcile phase")
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
	logger.InfoWithStatus(codes.Ok, "Finished Reconciliation")
	_ = r.RecordEvent(v1.EventTypeNormal, "Reconciled", "Finished Reconciliation")
	meta.SetStatusCondition(&client.Status.Conditions, metav1.Condition{
		Type:    typeAvailableClient,
		Status:  metav1.ConditionTrue,
		Reason:  "Reconciled",
		Message: "Finished Reconciliation",
	})
	logger.SetPhase("RECONCILED")
	return ctrl.Result{}, r.Status().Update(ctx, client)
}

func (r *ClientReconciler) patchStatus(ctx context.Context, client *wekav1alpha1.WekaClient, patcher patcher) error {
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
	ctx, logger := r.getLogSpan(ctx, "RecordCondition", fmt.Sprintf("%s/%s", r.CurrentInstance.Namespace, r.CurrentInstance.Name))
	defer logger.End()
	logger.WithValues("condition", condition.Type, "message", condition.Message,
		"status", condition.Status, "reason", condition.Reason).AddEvent("Recording condition")

	if r.CurrentInstance == nil {
		logger.InfoWithStatus(codes.Error, "Current client is nil")
		return fmt.Errorf("current client is nil")
	}
	if err := r.Get(ctx, runtimeClient.ObjectKeyFromObject(r.CurrentInstance), r.CurrentInstance); err != nil {
		logger.Error(err, "Failed to get client")
		return errors.Wrap(err, "RecordCondition: failed to get client")
	}
	meta.SetStatusCondition(&r.CurrentInstance.Status.Conditions, condition)
	err := r.Status().Update(ctx, r.CurrentInstance)
	if err != nil {
		logger.Error(err, "Failed to update status")
		return errors.Wrap(err, "RecordCondition: failed to update status")
	}
	return err
}

// UpdateStatus sets Status fields on the Client
func (r *ClientReconciler) UpdateStatus(ctx context.Context, updater func(*wekav1alpha1.ClientStatus)) error {
	ctx, logger := r.getLogSpan(ctx, "UpdateStatus", fmt.Sprintf("%s/%s", r.CurrentInstance.Namespace, r.CurrentInstance.Name))
	defer logger.End()

	if r.CurrentInstance == nil {
		logger.InfoWithStatus(codes.Error, "Current client is nil")
		return fmt.Errorf("current client is nil")
	}
	if err := r.Get(ctx, runtimeClient.ObjectKeyFromObject(r.CurrentInstance), r.CurrentInstance); err != nil {
		logger.Error(err, "Failed to get client")
		return errors.Wrap(err, "UpdateStatus: failed to get client")
	}
	updater(&r.CurrentInstance.Status)
	err := r.Status().Update(ctx, r.CurrentInstance)
	if err != nil {
		logger.Error(err, "Failed to update status")
	}
	return err
}

// TODO: Factor the below  out into reconciler methods
// SetupWithManager sets up the controller with the Manager.
func (r *ClientReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.WekaClient{}).
		Owns(&wekav1alpha1.WekaContainer{}).
		Complete(r)
}

func (r *ClientReconciler) finalizeClient(ctx context.Context, client *wekav1alpha1.WekaClient) error {
	r.Logger.Info("Successfully finalized WekaClient")
	return nil
}

func (r *ClientReconciler) initState(ctx context.Context, wekaClient *wekav1alpha1.WekaClient) error {
	if wekaClient.GetFinalizers() == nil {
		wekaClient.SetFinalizers([]string{WekaFinalizer})
		err := r.Update(ctx, wekaClient)
		if err != nil {
			return errors.Wrap(err, "failed to update wekaClient")
		}
	}
	return nil
}

func (r *ClientReconciler) handleDeletion(ctx context.Context, wekaClient *wekav1alpha1.WekaClient) error {
	if controllerutil.ContainsFinalizer(wekaClient, WekaFinalizer) {
		if err := r.finalizeClient(ctx, wekaClient); err != nil {
			return errors.Wrap(err, "failed to finalize wekaClient")
		}
		controllerutil.RemoveFinalizer(wekaClient, WekaFinalizer)
		if err := r.Update(ctx, wekaClient); err != nil {
			return errors.Wrap(err, "failed to update wekaClient")
		}
	}
	return nil
}

func (r *ClientReconciler) ensureWekaContainers(ctx context.Context, wekaClient *wekav1alpha1.WekaClient, nodes []string) (ctrl.Result, []*wekav1alpha1.WekaContainer, error) {
	foundContainers := []*wekav1alpha1.WekaContainer{}
	size := len(nodes)
	if size == 0 {
		return ctrl.Result{Requeue: true}, nil, nil
	}

	for _, node := range nodes {
		wekaContainer, err := r.buildClientWekaContainer(wekaClient, node)
		if err != nil {
			return ctrl.Result{}, nil, err
		}
		err = ctrl.SetControllerReference(wekaClient, wekaContainer, r.Scheme)
		if err != nil {
			return ctrl.Result{Requeue: true}, nil, err
		}

		found := &wekav1alpha1.WekaContainer{}
		err = r.Get(ctx, client.ObjectKey{Namespace: wekaContainer.Namespace, Name: wekaContainer.Name}, found)
		if err != nil && apierrors.IsNotFound(err) {
			// Define a new WekaContainer object
			err = r.Create(ctx, wekaContainer)
			if err != nil {
				return ctrl.Result{Requeue: true}, nil, err
			}

			foundContainers = append(foundContainers, wekaContainer)
		} else {
			foundContainers = append(foundContainers, found)
		}
	}
	return ctrl.Result{}, foundContainers, nil
}

func (r *ClientReconciler) buildClientWekaContainer(wekaClient *wekav1alpha1.WekaClient, node string) (*wekav1alpha1.WekaContainer, error) {
	//clientRandomPart, err := password.Generate(10, 3, 0, true, true)
	//if err != nil {
	//	return nil, err
	//}
	network, err := resources.GetContainerNetwork(wekaClient.Spec.NetworkSelector)
	if err != nil {
		return nil, err
	}

	container := &wekav1alpha1.WekaContainer{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "weka.weka.io/v1alpha1",
			Kind:       "WekaContainer",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", wekaClient.ObjectMeta.Name, node),
			Namespace: wekaClient.Namespace,
			Labels:    map[string]string{"app": "weka-client", "clientName": wekaClient.ObjectMeta.Name},
		},
		Spec: wekav1alpha1.WekaContainerSpec{
			NodeAffinity:       node,
			Port:               wekaClient.Spec.Port,
			AgentPort:          wekaClient.Spec.AgentPort,
			Image:              wekaClient.Spec.Image,
			ImagePullSecret:    wekaClient.Spec.ImagePullSecret,
			WekaContainerName:  fmt.Sprintf("%sclient", util.GetLastGuidPart(wekaClient.GetUID())),
			Mode:               "client",
			NumCores:           1,
			CoreIds:            []int{3},
			Network:            network,
			Hugepages:          1600,
			HugepagesSize:      "2Mi",
			WekaSecretRef:      v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{Key: wekaClient.Spec.WekaSecretRef}},
			DriversDistService: wekaClient.Spec.DriversDistService,
			JoinIps:            wekaClient.Spec.JoinIps,
		},
	}
	return container, nil
}

func GetClient(ctx context.Context, req ctrl.Request, r client.Reader, logger logr.Logger) (*wekav1alpha1.WekaClient, error) {
	wekaClient := &wekav1alpha1.WekaClient{}
	err := r.Get(ctx, req.NamespacedName, wekaClient)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("wekaClient resource not found. Ignoring since object must be deleted")
			return nil, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get wekaClient")
		return nil, err
	}
	return wekaClient, nil
}
