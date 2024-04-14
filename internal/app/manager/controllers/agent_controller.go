// This controller manages Deployment reconciliations.
package controllers

import (
	"bytes"
	"context"
	"fmt"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"reflect"
	"strings"

	"github.com/pkg/errors"

	"github.com/weka/weka-operator/util"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimeClient "sigs.k8s.io/controller-runtime/pkg/client"
)

type AgentReconciler struct {
	*ClientReconciler
	Desired          *appsv1.DaemonSet
	RootResourceName types.NamespacedName
}

type PodNotFound struct {
	Err error
}

func (e *PodNotFound) Error() string {
	return "pod not found"
}

func NewAgentReconciler(c *ClientReconciler, desired *appsv1.DaemonSet, root types.NamespacedName) *AgentReconciler {
	return &AgentReconciler{c, desired, root}
}

func (r *AgentReconciler) getLogSpan(ctx context.Context, names ...string) (context.Context, instrumentation.SpanLogger) {
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

func (r *AgentReconciler) Reconcile(ctx context.Context, client *wekav1alpha1.WekaClient) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ReconcileClients", "namespace", r.RootResourceName, "name", r.RootResourceName.Name)
	defer end()

	logger.Info("Reconciling agent")

	key := runtimeClient.ObjectKeyFromObject(r.Desired)
	existing := &appsv1.DaemonSet{}
	if err := r.Get(ctx, key, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to get daemonset")
			return ctrl.Result{}, fmt.Errorf("failed to get daemonset %s: %w", key, err)
		}

		// The resource did not already exist, so create it.
		logger.Info("Creating agent daemonset")
		if err := r.Create(ctx, r.Desired); err != nil {
			logger.Error(err, "Failed to create daemonset")
			return ctrl.Result{}, fmt.Errorf("failed to create daemonset %s: %w", key, err)
		}

		if err := r.RecordCondition(ctx, metav1.Condition{
			Type:    "Available",
			Status:  metav1.ConditionFalse,
			Reason:  "Reconciling",
			Message: "Requested new agent",
		}); err != nil {
			logger.Error(err, "Failed to update status")
		}
		logger.WithValues("deployment", r.Desired.Name).InfoWithStatus(codes.Ok, "Created deployment")

		return ctrl.Result{Requeue: true}, nil
	}

	if !r.isAgentAvailable(existing) {
		// The resource exists, but is not yet in a ready state\
		logger.InfoWithStatus(codes.Error, "Agent not available")
		return ctrl.Result{Requeue: true}, nil
	}

	if !reflect.DeepEqual(existing.Spec, r.Desired.Spec) {
		desired := existing.DeepCopy()
		desired.Spec = r.Desired.Spec
		logger.Info("Updating agent", "name", desired.Name, "version", desired.Spec.Template.Spec.Containers[0].Image)
		if err := r.Update(ctx, desired); err != nil {
			logger.Error(err, "Failed to update daemonset")
			return ctrl.Result{}, errors.Wrap(err, "failed to update daemonset")
		}
		logger.InfoWithStatus(codes.Ok, "Successfully updated daemonset")
		return ctrl.Result{Requeue: true}, nil
	}

	_ = r.RecordEvent(v1.EventTypeNormal, "Reconciled", "Reconciled agent")
	logger.InfoWithStatus(codes.Ok, "Reconciled agent")
	return ctrl.Result{}, nil
}

// Exec executes a command in the agent pods
func (r *AgentReconciler) Exec(ctx context.Context, cmd []string) (stdout, stderr bytes.Buffer, err error) {
	ctx, logger := r.getLogSpan(ctx, "Exec")
	logger = logger.WithValues("namespace", r.RootResourceName, "name", r.RootResourceName.Name)
	defer logger.End()
	logger.Debug("Fetching agent pods")
	agentPods, err := r.GetAgentPods(ctx)
	if err != nil {
		logger.Error(err, "Failed to get agent pods")
		return stdout, stderr, errors.Wrap(&PodNotFound{err}, "failed to get agent pods")
	}
	pod := agentPods.Items[0]
	logger.WithValues("cmd", strings.Join(cmd, " ")).Debug("Running command", "command")
	exec, err := util.NewExecInPod(&pod)
	if err != nil {
		logger.Error(err, "Failed to create executor")
		return stdout, stderr, errors.Wrap(err, "failed to create executor")
	}
	return exec.Exec(ctx, cmd)
}

// isAgentAvailable appear that daemonsets support conditions.  Instead, just return true.
func (r *AgentReconciler) isAgentAvailable(deployment *appsv1.DaemonSet) bool {
	return deployment.Status.NumberReady == deployment.Status.DesiredNumberScheduled
}

// GetAgentPods returns the pods belonging to the daemonset
func (r *AgentReconciler) GetAgentPods(ctx context.Context) (*v1.PodList, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetAgentPods", "namespace", r.RootResourceName, "name", r.RootResourceName.Name)
	defer end()
	logger.Debug("Fetching agent pods")
	agent, err := r.GetAgentResource(ctx)
	if err != nil {
		logger.Error(err, "Failed to get agent resource")
		return nil, errors.Wrap(err, "error getting pods")
	}

	config, err := util.KubernetesConfiguration()
	if err != nil {
		logger.Error(err, "Failed to get kubernetes configuration")
		return nil, errors.Wrap(err, "failed to get kubernetes configuration")
	}

	clientset, err := util.KubernetesClientSet(config)
	namespace := agent.Namespace
	agentPods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io=%s", agent.Name),
	})
	if err != nil {
		logger.Error(err, "Failed to get pod(s)")
		return nil, errors.Wrap(err, "failed to get pod")
	}

	return agentPods, nil
}

// GetAgentResource returns the agent DaemonSet resource
func (r *AgentReconciler) GetAgentResource(ctx context.Context) (*appsv1.DaemonSet, error) {
	ctx, logger := r.getLogSpan(ctx, "GetAgentResource")
	logger = logger.WithValues("namespace", r.RootResourceName, "name", r.RootResourceName.Name)
	defer logger.End()
	agent := &appsv1.DaemonSet{}
	key := client.ObjectKeyFromObject(r.Desired)
	err := r.Get(ctx, key, agent)
	if err != nil {
		logger.Error(err, "Failed to get agent resource")
		return nil, errors.Wrap(err, "failed to get agent resources")
	}

	return agent, nil
}
