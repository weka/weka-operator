// This controller manages Deployment reconciliations.
package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type DeploymentReconciler struct {
	client.Client
	Deployment *appsv1.Deployment
	Key        client.ObjectKey
	Logger     logr.Logger
}

func NewDeploymentReconciler(c client.Client) *DeploymentReconciler {
	return &DeploymentReconciler{
		Client:     c,
		Deployment: &appsv1.Deployment{},
	}
}

type deploymentPhase struct {
	Name      string
	Reconcile func(ctx context.Context) (ctrl.Result, error)
}

func (r *DeploymentReconciler) phases() []deploymentPhase {
	return []deploymentPhase{
		//{
		//Name:      "AddFinalizer",
		//Reconcile: r.reconcileAddFinalizer,
		//},
		{
			Name:      "WaitForDeploymentAvailable",
			Reconcile: r.reconcileWaitForDeploymentAvailable,
		},
	}
}

//func (r *DeploymentReconciler) reconcileAddFinalizer(ctx context.Context) (ctrl.Result, error) {
//// Add Finalizer
//finalizerName := "weka-operator.weka.io/deployment-finalizer"
//if r.Deployment.ObjectMeta.DeletionTimestamp.IsZero() {
//// Add the finalizer as long as the object is not being deleted
//if !controllerutil.ContainsFinalizer(r.Deployment, finalizerName) {
//controllerutil.AddFinalizer(r.Deployment, finalizerName)
//if err := r.Update(ctx, r.Deployment); err != nil {
//return ctrl.Result{}, fmt.Errorf("failed to add finalizer to deployment %s: %w", r.Key, err)
//}
//}
//} else {
//// The object is being deleted
//// Remove the client container from the cluster
//if controllerutil.ContainsFinalizer(r.Deployment, finalizerName) {
//if err := r.deleteWekaContainer(ctx, r.Deployment); err != nil {
//return ctrl.Result{}, fmt.Errorf("failed to delete weka container from cluster %s: %w", r.Key, err)
//}

//// Remove the finalizer
//controllerutil.RemoveFinalizer(r.Deployment, finalizerName)
//if err := r.Update(ctx, r.Deployment); err != nil {
//return ctrl.Result{}, fmt.Errorf("failed to remove finalizer from deployment %s: %w", r.Key, err)
//}
//}
//}

//return ctrl.Result{}, nil
//}

func (r *DeploymentReconciler) reconcileWaitForDeploymentAvailable(ctx context.Context) (ctrl.Result, error) {
	// Wait for the deployment to become available
	if !r.isDeploymentAvailable(r.Deployment) {
		r.Logger.Info("deployment is not available yet, requeuing")
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, nil
}

func (r *DeploymentReconciler) Reconcile(ctx context.Context, desired *appsv1.Deployment) (ctrl.Result, error) {
	r.Logger = ctrl.LoggerFrom(ctx)
	r.Key = client.ObjectKeyFromObject(desired)
	if err := r.Get(ctx, r.Key, r.Deployment); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("failed to get deployment %s: %w", r.Key, err)
		}
		if err := r.Create(ctx, desired); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create deployment %s: %w", r.Key, err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	for _, phase := range r.phases() {
		r.Logger.Info("reconciling phase", "phase", phase.Name)
		result, err := phase.Reconcile(ctx)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to reconcile phase %s: %w", phase.Name, err)
		}

		if result.Requeue {
			return result, nil
		}
	}

	patch := client.MergeFrom(r.Deployment.DeepCopy())
	r.Deployment.Spec = desired.Spec
	for k, v := range desired.Annotations {
		r.Deployment.Annotations[k] = v
	}
	for k, v := range desired.Labels {
		r.Deployment.Labels[k] = v
	}

	return ctrl.Result{}, r.Patch(ctx, r.Deployment, patch)
}

//func (r *DeploymentReconciler) deleteWekaContainer(ctx context.Context, deployment *appsv1.Deployment) error {
//// cluster container --filter hostname=mbp-k8s-oci-0,container=client --format jso
//stdout, stderr, err := clientExec(ctx, client, []string{"/usr/bin/weka", "cluster", "container", "--filter", "container=client", "--format", "json"})
//if err != nil {
//r.Logger.Error(err, "failed to list containers", "stdout", stdout, "stderr", stderr)
//return errors.Wrap(err, "Failed to list containers")
//}

// containerList := []WekaContainer{}
// json.Unmarshal(stdout.Bytes(), &containerList)

//// host_id is a string of the form "HostId<id>"
//hostRegexp := regexp.MustCompile(`HostId<(\d+)>`)
//for _, container := range containerList {
//matches := hostRegexp.FindStringSubmatch(container.HostID)
//if len(matches) < 1 {
//r.Logger.Error(err, "failed to parse host id", "host_id", container.HostID)
//return errors.New("Failed to parse host id")
//}

//hostID := matches[1]
//stdout, stderr, err = clientExec(ctx, client, []string{"/usr/bin/weka", "cluster", "container", "deactivate"})
//if err != nil {
//r.Logger.Error(err, "failed to deactivate container", "stdout", stdout, "stderr", stderr)
//return errors.Wrap(err, "Failed to deactivate container")
//}

// stdout, stderr, err = clientExec(ctx, client, []string{"/usr/bin/weka", "cluster", "container", "remove", "--host-id", hostID})

//return nil
//}
//}

// isDeploymentAvailable returns true if the deployment is available.
// The helper inspects the status conditions to determine if it has reached an available state
func (r *DeploymentReconciler) isDeploymentAvailable(deployment *appsv1.Deployment) bool {
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentAvailable && condition.Status == "True" {
			return true
		}
	}

	return false
}
