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
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	wekav1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
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
	logger.Info("Reconciling Client")

	// TODO(user): your logic here
	client := &wekav1alpha1.Client{}
	err := r.Get(ctx, req.NamespacedName, client)
	if err != nil {
		logger.Error(err, "unable to fetch Client")
		return ctrl.Result{}, err
	}

	if client.Status.Conditions == nil || len(client.Status.Conditions) == 0 {
		meta.SetStatusCondition(&client.Status.Conditions, metav1.Condition{
			Type:    typeAvailableClient,
			Status:  metav1.ConditionUnknown,
			Reason:  "Reconciling",
			Message: "Reconciling Client",
		})
		if err = r.Status().Update(ctx, client); err != nil {
			logger.Error(err, "unable to update Client status")
			return ctrl.Result{}, err
		}
	}

	// Deployment - Runs weka-client as a simple busybox container
	deployment := &appsv1.Deployment{}
	err = r.Get(ctx, req.NamespacedName, deployment)
	if err != nil && apierrors.IsNotFound(err) {
		// define a new deployment
		dep, err := r.deploymentForClient(client)
		if err != nil {
			logger.Error(err, "unable to create Deployment for Client", "Client.Namespace", client.Namespace, "Client.Name", client.Name)

			meta.SetStatusCondition(&client.Status.Conditions, metav1.Condition{
				Type:    typeAvailableClient,
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciling",
				Message: fmt.Sprintf("Failed to create Deployment for the custom resource (%s): (%s)", client.Name, err),
			})

			if err = r.Status().Update(ctx, client); err != nil {
				logger.Error(err, "unable to update Client status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		logger.Info("Creating a new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
		if err = r.Create(ctx, dep); err != nil {
			logger.Error(err, "Failed to create new Deployment",
				"Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
			return ctrl.Result{}, err
		}

		// Deployment created successfully - return and requeue
		return ctrl.Result{RequeueAfter: time.Minute * 1}, nil
	} else if err != nil {
		logger.Error(err, "unable to fetch Deployment")
		return ctrl.Result{}, err
	}

	// DaemonSet - Runs a privileged container that installs a file on each node
	daemon := &appsv1.DaemonSet{}
	err = r.Get(ctx, req.NamespacedName, daemon)
	if err != nil && apierrors.IsNotFound(err) {
		fileManagerDaemon, err := r.fileManagerDaemonSet(client)
		if err != nil {
			logger.Error(err, "unable to create DaemonSet for Client", "Client.Namespace", client.Namespace, "Client.Name", client.Name)

			meta.SetStatusCondition(&client.Status.Conditions, metav1.Condition{
				Type:    "Available",
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciling",
				Message: fmt.Sprintf("Failed to create DaemonSet for the custom resource (%s): (%s)", client.Name, err),
			})

			if err = r.Status().Update(ctx, client); err != nil {
				logger.Error(err, "unable to update Client status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		logger.Info("Creating a new DaemonSet", "DaemonSet.Namespace", fileManagerDaemon.Namespace, "DaemonSet.Name", fileManagerDaemon.Name)
		if err = r.Create(ctx, fileManagerDaemon); err != nil {
			logger.Error(err, "Failed to create new DaemonSet",
				"DaemonSet.Namespace", fileManagerDaemon.Namespace, "DaemonSet.Name", fileManagerDaemon.Name)
			return ctrl.Result{}, err
		}

		// DaemonSet created successfully - return and requeue
		return ctrl.Result{RequeueAfter: time.Minute * 1}, nil

	} else if err != nil {
		logger.Error(err, "unable to fetch DaemonSet")
		return ctrl.Result{}, err
	}

	logger.Info("Finished Reconciling Client")
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClientReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.Client{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

func (r *ClientReconciler) deploymentForClient(client *wekav1alpha1.Client) (*appsv1.Deployment, error) {
	runAsNonRoot := true
	privileged := false
	runAsUser := int64(1001)

	ls := map[string]string{
		"app.kubernetes.io": "weka-client",
		"iteration":         "2",
		"runAsNonRoot":      strconv.FormatBool(runAsNonRoot),
		"privileged":        strconv.FormatBool(privileged),
		"runAsUser":         strconv.FormatInt(runAsUser, 10),
	}
	replicas := int32(1)

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      client.Name,
			Namespace: client.Namespace,
			Labels:    ls,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: &[]bool{true}[0],
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{{
						Image:           "busybox:1.35",
						Name:            "weka-client",
						ImagePullPolicy: corev1.PullIfNotPresent,
						SecurityContext: &corev1.SecurityContext{
							// RunAsNonRoot: &[]bool{false}[0],
							// Privileged:   &[]bool{true}[0],
							RunAsNonRoot: &[]bool{runAsNonRoot}[0],
							Privileged:   &[]bool{privileged}[0],
							RunAsUser:    &[]int64{runAsUser}[0],
						},
						Command: []string{"sleep", "3600"},
						TTY:     true,
						Stdin:   true,
					}},
				},
			},
		},
	}

	dep.ObjectMeta.ResourceVersion = ""

	// Set Client instance as the owner and controller
	ctrl.SetControllerReference(client, dep, r.Scheme)
	return dep, nil
}

// Creates a DaemonSet that installs a file on each node
// This container is privileged and runs as root
func (r *ClientReconciler) fileManagerDaemonSet(client *wekav1alpha1.Client) (*appsv1.DaemonSet, error) {

	name := client.Name

	daemon := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: client.Namespace,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io": name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io": name,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Image:           "busybox:1.35",
						Name:            name,
						ImagePullPolicy: corev1.PullIfNotPresent,
						SecurityContext: &corev1.SecurityContext{
							Privileged:   &[]bool{true}[0],
							RunAsNonRoot: &[]bool{false}[0],
							RunAsUser:    &[]int64{0}[0],
						},
						Command: []string{"sleep", "3600"},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "root",
								MountPath: "/mnt/root",
							},
						},
					}},
					Volumes: []corev1.Volume{
						{
							Name: "root",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/",
								},
							},
						},
					},
				},
			},
		},
	}

	ctrl.SetControllerReference(client, daemon, r.Scheme)
	return daemon, nil
}
