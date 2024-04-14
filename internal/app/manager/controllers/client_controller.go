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

func (r *ClientReconciler) getLogSpan(ctx context.Context, names ...string) (context.Context, instrumentation.SpanLogger) {
	logger := r.Logger
	joinNames := strings.Join(names, ".")
	ctx, span := instrumentation.Tracer.Start(ctx, joinNames)
	if span != nil {
		traceID := span.SpanContext().TraceID().String()
		spanID := span.SpanContext().SpanID().String()
		logger = logger.WithValues("trace_id", traceID, "span_id", spanID)
		for _, name := range names {
			logger = logger.WithValues("name", name)
		}
	}

	ShutdownFunc := func(opts ...trace.SpanEndOption) {
		if span != nil {
			span.End(opts...)
		}
		logger.V(4).Info(fmt.Sprintf("%s finished", joinNames))
	}

	ls := instrumentation.SpanLogger{
		Logger: logger,
		Span:   span,
		End:    ShutdownFunc,
	}
	logger.V(4).Info(fmt.Sprintf("%s called", joinNames))
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

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile

func (r *ClientReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "Reconcile", "namespace", req.Namespace, "name", req.Name)
	defer end()

	wekaClient, err := GetClient(ctx, req, r.Client)
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

	result, containers, err := r.ensureClientsWekaContainers(ctx, wekaClient, applicableNodes)
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

func (r *ClientReconciler) RecordEvent(eventtype string, reason string, message string) error {
	if r.CurrentInstance == nil {
		return fmt.Errorf("current client is nil")
	}
	r.Recorder.Event(r.CurrentInstance, v1.EventTypeNormal, reason, message)
	return nil
}

func (r *ClientReconciler) RecordCondition(ctx context.Context, condition metav1.Condition) error {
	ctx, logger := r.getLogSpan(ctx, "RecordCondition")
	logger = logger.WithValues("namespace", r.CurrentInstance.Namespace, "name", r.CurrentInstance.Name)
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
	ctx, logger := r.getLogSpan(ctx, "UpdateStatus")
	logger = logger.WithValues("namespace", r.CurrentInstance.Namespace, "name", r.CurrentInstance.Name)
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

func (r *ClientReconciler) ensureClientsWekaContainers(ctx context.Context, wekaClient *wekav1alpha1.WekaClient, nodes []string) (ctrl.Result, []*wekav1alpha1.WekaContainer, error) {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureClientsWekaContainers", "namespace", wekaClient.Namespace, "name", wekaClient.Name)
	defer end()

	foundContainers := []*wekav1alpha1.WekaContainer{}
	size := len(nodes)
	if size == 0 {
		return ctrl.Result{Requeue: true}, nil, nil
	}

	for _, node := range nodes {
		ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureClientsWekaContainers", "node", node)
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
		end()
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

func GetClient(ctx context.Context, req ctrl.Request, r client.Reader) (*wekav1alpha1.WekaClient, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetClient", "namespace", req.Namespace, "name", req.Name)
	defer end()

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
