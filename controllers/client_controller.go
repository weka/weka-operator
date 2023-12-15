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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/pkg/errors"
	wekav1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
	"github.com/weka/weka-operator/controllers/condition"
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

	ApiKey               *ApiKey
	Builder              *resources.Builder
	ConditionReady       *condition.Ready
	ModuleReconciler     *ModuleReconciler
	DeploymentReconciler *DeploymentReconciler
}

type reconcilePhase struct {
	Name      string
	Reconcile func(ctx context.Context, client *wekav1alpha1.Client) (ctrl.Result, error)
}

type patcher func(status *wekav1alpha1.ClientStatus) error

type ApiKey struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
}

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
		{
			Name:      "api_key",
			Reconcile: r.reconcileApiKey,
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

// reconcileApiKey Extracts the API key from the client
func (r *ClientReconciler) reconcileApiKey(ctx context.Context, client *wekav1alpha1.Client) (ctrl.Result, error) {
	// Client generates a key at startup and puts it in a well known location
	// In order to read this file, we need to use Exec to run cat on the container and then read STDOUT
	logger := log.FromContext(ctx)
	logger.Info("Reconciling API Key", "Client", client.Name, "Namespace", client.Namespace)

	config, err := kubernetesConfiguration()
	if err != nil {
		logger.Error(err, "Failed to get kubernetes configuration")
		return ctrl.Result{}, errors.Wrap(err, "failed to get kubernetes configuration")
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		logger.Error(err, "Failed to get clientset")
		return ctrl.Result{}, errors.Wrap(err, "failed to get clientset")
	}

	// Lookup the pod via the deployment
	deployment := &appsv1.Deployment{}
	err = r.Get(ctx, runtimeClient.ObjectKeyFromObject(client), deployment)
	if err != nil {
		logger.Error(err, "Failed to get deployment")
		return ctrl.Result{}, errors.Wrap(err, "failed to get deployment")
	}
	deploymentPods, err := clientset.CoreV1().Pods(client.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io=%s", deployment.Name),
	})
	if err != nil {
		logger.Error(err, "Failed to get pod")
		return ctrl.Result{}, errors.Wrap(err, "failed to get pod")
	}
	pod := deploymentPods.Items[0]

	podExec := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod.Name).
		Namespace(pod.Namespace).
		SubResource("exec").
		VersionedParams(&v1.PodExecOptions{
			Container: "weka-client",
			Command:   []string{"cat", "/root/.weka/auth-token.json"},
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(config, "POST", podExec.URL())
	if err != nil {
		logger.Error(err, "Failed to create executor")
		return ctrl.Result{}, errors.Wrap(err, "failed to create executor")
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
		Tty:    false,
	})
	if err != nil {
		logger.Info("Failed to stream", "stdout", stdout.String(), "stderr", stderr.String())
		return ctrl.Result{}, errors.Wrap(err, "failed to stream")
	}

	// Parse the JSON
	//   - keys: access_token, refresh_token, token_type
	json.Unmarshal(stdout.Bytes(), r.ApiKey)

	return ctrl.Result{}, nil
}

func kubernetesConfiguration() (*rest.Config, error) {
	kubeConfigPath := os.Getenv("KUBECONFIG")
	if kubeConfigPath == "" {
		return rest.InClusterConfig()
	} else {
		return clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	}
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

	if client.Status.Conditions == nil || len(client.Status.Conditions) == 0 {
		meta.SetStatusCondition(&client.Status.Conditions, metav1.Condition{
			Type:    typeAvailableClient,
			Status:  metav1.ConditionUnknown,
			Reason:  "Initializing",
			Message: "Beginning Reconcialiation",
		})
		if err := r.Status().Update(ctx, client); err != nil {
			logger.Error(err, "Failed to update status")
			return ctrl.Result{}, err
		}

		if err := r.patchStatus(ctx, client, r.patcher(ctx, client)); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
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
