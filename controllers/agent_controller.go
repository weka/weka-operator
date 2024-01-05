// This controller manages Deployment reconciliations.
package controllers

import (
	"bytes"
	"context"
	"fmt"

	"github.com/pkg/errors"

	wekav1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
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

func (r *AgentReconciler) Reconcile(ctx context.Context, client *wekav1alpha1.Client) (ctrl.Result, error) {
	r.RecordEvent(v1.EventTypeNormal, "Reconciling", "Reconciling agent")
	key := runtimeClient.ObjectKeyFromObject(r.Desired)
	existing := &appsv1.DaemonSet{}
	if err := r.Get(ctx, key, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("failed to get deployment %s: %w", key, err)
		}

		// The resource did not already exisst, so create it.
		if err := r.Create(ctx, r.Desired); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create deployment %s: %w", key, err)
		}

		if err := r.UpdateStatus(ctx, metav1.Condition{
			Type:    "Available",
			Status:  metav1.ConditionFalse,
			Reason:  "Reconciling",
			Message: "Requested new agent",
		}); err != nil {
			r.Logger.Error(err, "Failed to update status")
		}

		return ctrl.Result{Requeue: true}, nil
	}

	if !r.isAgentAvailable(existing) {
		// The resource exists, but is not yet in a ready state
		return ctrl.Result{Requeue: true}, nil
	}

	// The resource exists and is ready.  This adds metadata from the spec
	patch := runtimeClient.MergeFrom(existing.DeepCopy())
	existing.Spec = r.Desired.Spec
	for k, v := range r.Desired.Annotations {
		existing.Annotations[k] = v
	}
	for k, v := range r.Desired.Labels {
		existing.Labels[k] = v
	}

	r.RecordEvent(v1.EventTypeNormal, "Reconciled", "Reconciled agent")
	//if err := r.UpdateStatus(ctx, metav1.Condition{
	//Type:    "Available",
	//Status:  metav1.ConditionTrue,
	//Reason:  "Reconciled",
	//Message: "Agent is ready",
	//}); err != nil {
	//return ctrl.Result{}, errors.Wrap(err, "failed to update status")
	//}
	return ctrl.Result{}, r.Patch(ctx, existing, patch)
}

// Exec
func (r *AgentReconciler) Exec(ctx context.Context, cmd []string) (stdout, stderr bytes.Buffer, err error) {
	agentPods, err := r.GetAgentPods(ctx)
	if err != nil {
		return stdout, stderr, errors.Wrap(&PodNotFound{err}, "failed to get agent pods")
	}
	pod := agentPods.Items[0]

	exec, err := util.NewExecInPod(&pod)
	if err != nil {
		return stdout, stderr, errors.Wrapf(err, "Failed to run command %s", cmd)
	}
	return exec.Exec(ctx, cmd)
}

// appear that daemonsets support conditions.  Instead, just return true.
func (r *AgentReconciler) isAgentAvailable(deployment *appsv1.DaemonSet) bool {
	return deployment.Status.NumberReady == deployment.Status.DesiredNumberScheduled
}

// GetAgentPods returns the pods belonging to the daemonset
func (r *AgentReconciler) GetAgentPods(ctx context.Context) (*v1.PodList, error) {
	agent, err := r.GetAgentResource(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "error getting pods")
	}

	config, err := util.KubernetesConfiguration()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get kubernetes configuration")
	}

	clientset, err := util.KubernetesClientSet(config)
	namespace := agent.Namespace
	agentPods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io=%s", agent.Name),
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to get pod")
	}

	return agentPods, nil
}

// GetAgentResource returns the agent DaemonSet resource
func (r *AgentReconciler) GetAgentResource(ctx context.Context) (*appsv1.DaemonSet, error) {
	agent := &appsv1.DaemonSet{}
	key := client.ObjectKeyFromObject(r.Desired)
	err := r.Get(ctx, key, agent)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get agent resources")
	}

	return agent, nil
}
