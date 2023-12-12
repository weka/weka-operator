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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kmmv1beta1 "github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	"github.com/pkg/errors"
	wekav1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
	"github.com/weka/weka-operator/controllers/resources"
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
	err := r.Get(ctx, req.NamespacedName, client)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			logger.Info("Client resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to fetch Client")
		return ctrl.Result{}, err
	}

	// Check if the Client instance is marked to be deleted, which is
	// indicated by the deletion timestamp being set.
	isClientMarkedToBeDeleted := client.GetDeletionTimestamp() != nil
	if isClientMarkedToBeDeleted {
		if controllerutil.ContainsFinalizer(client, clientFinalizer) {
			if err := r.finalizeClient(ctx, client); err != nil {
				return ctrl.Result{}, err
			}

			// Remove clientFinalizer. Once all finalizers have been
			// removed, the object will be deleted.
			controllerutil.RemoveFinalizer(client, clientFinalizer)
			err := r.Update(ctx, client)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer for this CR
	if !controllerutil.ContainsFinalizer(client, clientFinalizer) {
		controllerutil.AddFinalizer(client, clientFinalizer)
		err := r.Update(ctx, client)
		if err != nil {
			return ctrl.Result{}, err
		}
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

	// Drivers
	// wekafsgw
	result, err := r.reconcileWekaFsGw(ctx, req, client)
	if err != nil {
		return result, errors.Wrap(err, "failed to reconcile wekafsgw driver")
	} else if !result.IsZero() {
		return result, nil
	}

	// wekafsio
	wekafsioDriver := &kmmv1beta1.Module{}
	wekafsioNamespacedName := types.NamespacedName{
		Name:      "wekafsio",
		Namespace: "default",
	}
	err = r.Get(ctx, wekafsioNamespacedName, wekafsioDriver)
	if err != nil && apierrors.IsNotFound(err) {
		// define a new wekafsio Driver
		metadata := &metav1.ObjectMeta{
			Name:      "wekafsio",
			Namespace: "default",
		}
		options := &resources.WekaFSModuleOptions{
			ImagePullSecretName: client.Spec.ImagePullSecretName,
			WekaVersion:         client.Spec.Version,
			BackendIP:           client.Spec.Backend.IP,
		}
		spec, err := resources.WekaFSIOModule(metadata, options)
		if err != nil {
			logger.Error(err, "Invalid driver configuration for wekafsio")

			meta.SetStatusCondition(&client.Status.Conditions, metav1.Condition{
				Type:    typeAvailableClient,
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciling",
				Message: fmt.Sprintf("Invalid driver configuration for wekafsio: (%s)", err),
			})

			if err = r.Status().Update(ctx, client); err != nil {
				logger.Error(err, "unable to update Client status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		if spec.Namespace == "" {
			spec.Namespace = "default"
		}
		logger.Info("Creating a new wekafsio driver", "wekafsioDriver.Namespace", spec.Namespace, "wekafsioDriver.Name", spec.Name)
		if err = r.Create(ctx, spec); err != nil {
			logger.Error(err, "Failed to create new wekafsio driver",
				"wekafsioDriver.Namespace", spec.Namespace, "wekafsioDriver.Name", spec.Name)
			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: time.Minute * 1}, nil
	} else {
		if err != nil {
			logger.Error(err, "unable to fetch wekafsio driver")
			return ctrl.Result{}, err
		}
	}

	// Deployment
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

// reconcileWekaFsGw reconciles the wekafsgw driver
func (r *ClientReconciler) reconcileWekaFsGw(ctx context.Context, req ctrl.Request, client *wekav1alpha1.Client) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling wekafsgw driver", "NamespacedName", req.NamespacedName)

	wekafsgwDriver := &kmmv1beta1.Module{}
	wekafsgwNamespacedName := types.NamespacedName{
		Name:      "wekafsgw",
		Namespace: req.Namespace,
	}
	err := r.Get(ctx, wekafsgwNamespacedName, wekafsgwDriver)
	if err != nil && apierrors.IsNotFound(err) {
		// define a new wekafsgw Driver
		metadata := &metav1.ObjectMeta{
			Name:      wekafsgwNamespacedName.Name,
			Namespace: wekafsgwNamespacedName.Namespace,
		}
		options := &resources.WekaFSModuleOptions{
			ImagePullSecretName: client.Spec.ImagePullSecretName,
			WekaVersion:         client.Spec.Version,
			BackendIP:           client.Spec.Backend.IP,
		}
		spec, err := resources.WekaFSGWModule(metadata, options)
		if err != nil {
			logger.Error(err, "Invalid driver configuration for wekafsgw")

			meta.SetStatusCondition(&client.Status.Conditions, metav1.Condition{
				Type:    typeAvailableClient,
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciling",
				Message: fmt.Sprintf("Invalid driver configuration for wekafsgw: (%s)", err),
			})

			if err = r.Status().Update(ctx, client); err != nil {
				logger.Error(err, "unable to update Client status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		if spec.Namespace == "" {
			spec.Namespace = "default"
		}
		logger.Info("Creating a new wekafsgw driver", "wekafsgwDriver.Namespace", spec.Namespace, "wekafsgwDriver.Name", spec.Name)
		if err = r.Create(ctx, spec); err != nil {
			logger.Error(err, "Failed to create new wekafsgw driver",
				"wekafsgwDriver.Namespace", spec.Namespace, "wekafsgwDriver.Name", spec.Name)
			return ctrl.Result{}, err
		}
		ctrl.SetControllerReference(client, spec, r.Scheme)

		return ctrl.Result{RequeueAfter: time.Minute * 1}, nil
	} else {
		if err != nil {
			logger.Error(err, "unable to fetch wekafsgw driver")
			return ctrl.Result{}, err
		}
	}

	// Nothing to do
	return ctrl.Result{}, nil
}

func (r *ClientReconciler) deploymentForClient(client *wekav1alpha1.Client) (*appsv1.Deployment, error) {
	runAsNonRoot := false
	privileged := true
	runAsUser := int64(0)

	image := fmt.Sprintf("%s:%s", client.Spec.Image, client.Spec.Version)

	ls := map[string]string{
		"app.kubernetes.io": "weka-agent",
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
				MatchLabels: map[string]string{
					"app.kubernetes.io": "weka-agent",
				},
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
					ImagePullSecrets: []corev1.LocalObjectReference{
						{
							Name: client.Spec.ImagePullSecretName,
						},
					},
					Containers: []corev1.Container{
						// Agent Container
						wekaAgentContainer(client, image),
						wekaClientContainer(client, image),
					},
					Volumes: []corev1.Volume{
						{
							Name: "host-root",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/",
								},
							},
						},
						{
							Name: "host-dev",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/dev",
								},
							},
						},
						{
							Name: "host-cgroup",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/sys/fs/cgroup",
								},
							},
						},
						{
							Name: "opt-weka-data",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "hugepage-2mi-1",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{
									Medium: corev1.StorageMediumHugePages,
								},
							},
						},
						//{
						//Name: "hugepage-2mi-2",
						//VolumeSource: corev1.VolumeSource{
						//EmptyDir: &corev1.EmptyDirVolumeSource{
						//Medium: corev1.StorageMediumHugePages,
						//},
						//},
						//},
					},
				},
			},
		},
	}

	dep.ObjectMeta.ResourceVersion = ""

	// Set Client instance as the owner and controller
	ctrl.SetControllerReference(client, dep, r.Scheme)
	return dep, nil
}

func wekaAgentContainer(client *wekav1alpha1.Client, image string) corev1.Container {
	container := corev1.Container{
		Image:           image,
		Name:            "weka-agent",
		ImagePullPolicy: corev1.PullAlways,
		Command: []string{
			"/usr/bin/weka",
			"--agent",
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot: &[]bool{false}[0],
			Privileged:   &[]bool{true}[0],
			RunAsUser:    &[]int64{0}[0],
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "host-root",
				MountPath: "/dev/root",
			},
			{
				Name:      "host-dev",
				MountPath: "/dev",
			},
			{
				Name:      "host-cgroup",
				MountPath: "/sys/fs/cgroup",
			},
			{
				Name:      "opt-weka-data",
				MountPath: "/opt/weka/data",
			},
			{
				Name:      "hugepage-2mi-1",
				MountPath: "/dev/hugepages",
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				"hugepages-2Mi": resource.MustParse("1024Mi"),
				"memory":        resource.MustParse("8Gi"),
			},
			Requests: corev1.ResourceList{
				"memory":        resource.MustParse("8Gi"),
				"hugepages-2Mi": resource.MustParse("1024Mi"),
			},
		},
		Env: []corev1.EnvVar{
			{
				Name:  "WEKA_VERSION",
				Value: client.Spec.Version,
			},
			{
				Name:  "DRIVERS_VERSION",
				Value: client.Spec.Version,
			},
			{
				Name:  "IONODE_COUNT",
				Value: strconv.Itoa(int(client.Spec.IONodeCount)),
			},
			{
				Name:  "BACKEND_PRIVATE_IP",
				Value: client.Spec.Backend.IP,
			},
			{
				Name:  "WEKA_CLI_DEBUG",
				Value: client.Spec.Debug,
			},
		},
	}

	return container
}

func wekaClientContainer(client *wekav1alpha1.Client, image string) corev1.Container {
	return corev1.Container{
		Image:           image,
		Name:            "weka-client",
		ImagePullPolicy: corev1.PullAlways,
		Command: []string{
			//"sleep", "infinity",
			"/opt/start-weka-client.sh",
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot: &[]bool{false}[0],
			Privileged:   &[]bool{true}[0],
			RunAsUser:    &[]int64{0}[0],
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "host-root",
				MountPath: "/mnt/root",
			},
			{
				Name:      "host-dev",
				MountPath: "/dev",
			},
			{
				Name:      "host-cgroup",
				MountPath: "/sys/fs/cgroup",
			},
			{
				Name:      "opt-weka-data",
				MountPath: "/opt/weka/data",
			},
			//{
			//Name:      "hugepage-2mi-2",
			//MountPath: "/dev/hugepages",
			//},
		},
		//Resources: corev1.ResourceRequirements{
		//Limits: corev1.ResourceList{
		//"hugepages-2Mi": resource.MustParse("512Mi"),
		//"memory":        resource.MustParse("1Gi"),
		//},
		//Requests: corev1.ResourceList{
		//"memory": resource.MustParse("1Gi"),
		//},
		//},
		Env: []corev1.EnvVar{
			{
				Name:  "WEKA_VERSION",
				Value: client.Spec.Version,
			},
			{
				Name:  "DRIVERS_VERSION",
				Value: client.Spec.Version,
			},
			{
				Name:  "IONODE_COUNT",
				Value: strconv.Itoa(int(client.Spec.IONodeCount)),
			},
			{
				Name:  "BACKEND_PRIVATE_IP",
				Value: client.Spec.Backend.IP,
			},
			{
				Name:  "WEKA_CLI_DEBUG",
				Value: "", // client.Spec.Debug,
			},
		},
		Ports: []corev1.ContainerPort{
			{ContainerPort: 14000},
			{ContainerPort: 14100},
		},
	}
}

func (r *ClientReconciler) finalizeClient(ctx context.Context, client *wekav1alpha1.Client) error {
	logger := log.FromContext(ctx)
	logger.Info("Successfully finalized Client")
	return nil
}
