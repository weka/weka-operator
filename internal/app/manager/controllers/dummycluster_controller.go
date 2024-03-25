/*
Copyright 2024.

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
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/util"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const wekaContainerFinalizer = "weka.weka.io/finalizer"

// DummyClusterReconciler reconciles a DummyCluster object
type DummyClusterReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Logger   logr.Logger
}

func NewDummyClusterController(mgr ctrl.Manager) *DummyClusterReconciler {
	return &DummyClusterReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Logger:   mgr.GetLogger().WithName("controllers").WithName("DummyCluster"),
		Recorder: mgr.GetEventRecorderFor("dummycluster-controller"),
	}
}

const (
	CondPodsCreated    = "PodsCreated"
	CondPodsRead       = "PodsReady"
	CondClusterCreated = "ClusterCreated"
	CondDrivesAdded    = "DrivesAdded"
	CondIoStarted      = "IoStarted"
)

// +kubebuilder:rbac:groups=weka.weka.io,resources=dummyclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=weka.weka.io,resources=dummyclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=weka.weka.io,resources=dummyclusters/finalizers,verbs=update
func (r *DummyClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.WithName("Reconcile")
	logger.Info("Reconcile() called")
	defer logger.Info("Reconcile() finished")
	// Fetch the DummyCluster instance
	dummyCluster, err := GetCluster(ctx, req, r.Client, r.Logger)
	if err != nil {
		return ctrl.Result{}, err
	}
	if dummyCluster == nil {
		return ctrl.Result{}, nil
	}

	err = r.initState(ctx, dummyCluster)
	if err != nil {
		logger.Error(err, "Failed to initialize state")
		return ctrl.Result{}, err
	}

	if dummyCluster.GetDeletionTimestamp() != nil {
		err = r.handleDeletion(ctx, dummyCluster)
		if err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("Deleting dummyCluster")
		return ctrl.Result{}, nil
	}

	// Note: All use of conditions is only as hints for skipping actions and a visibility, not strictly a state machine
	// All code should be idempotent and not rely on conditions for correctness, hence validation of succesful update of conditions is not done

	adminSecret := &v1.Secret{}
	secretResourceId := client.ObjectKey{Namespace: dummyCluster.Namespace, Name: "operator-admin-password"}
	// Look for operator-password-secret
	result, err := r.ensureSecret(ctx, secretResourceId, adminSecret)
	if err != nil {
		logger.Error(err, "Failed to ensure secret")
		return ctrl.Result{}, err
	}
	if result.Requeue {
		return result, nil
	}

	containers, err := r.ensureWekaContainers(ctx, dummyCluster, adminSecret)
	if err != nil {
		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{
			Type:   CondPodsCreated,
			Status: metav1.ConditionFalse, Reason: "Error", Message: err.Error(),
		})
		_ = r.Status().Update(ctx, dummyCluster)
		r.Logger.Error(err, "Failed to ensure WekaContainers")
		return ctrl.Result{}, err
	}
	meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{
		Type:   CondPodsCreated,
		Status: metav1.ConditionTrue, Reason: "Success", Message: "All pods are created",
	})
	_ = r.Status().Update(ctx, dummyCluster)

	if meta.IsStatusConditionFalse(dummyCluster.Status.Conditions, CondPodsRead) {
		if ready, err := r.isContainersReady(containers); !ready {
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second}, err
		}
		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{
			Type:   CondPodsRead,
			Status: metav1.ConditionTrue, Reason: "Success", Message: "All weka containers are ready for clusterization",
		})
		_ = r.Status().Update(ctx, dummyCluster)
	}

	if meta.IsStatusConditionFalse(dummyCluster.Status.Conditions, CondClusterCreated) {
		err = r.CreateCluster(ctx, dummyCluster, containers)
		if err != nil {
			return ctrl.Result{}, err
		}
		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{
			Type:   CondClusterCreated,
			Status: metav1.ConditionTrue, Reason: "Success", Message: "Cluster is formed",
		})
		_ = r.Status().Update(ctx, dummyCluster)
	}

	// Set cluster password
	result, err = r.setClusterPassword(ctx, dummyCluster, containers)
	if err != nil {
		r.Logger.Error(err, "Error configuring cluster password")
		return ctrl.Result{}, err
	}
	if result.Requeue {
		return result, nil
	}
	r.Logger.Info("Cluster password configured")
	// Finished setting cluster password

	err = r.EnsureClusterContainerIds(ctx, dummyCluster, containers)
	if err != nil {
		r.Logger.Info("not all containers are up in the cluster", "err", err)
		return ctrl.Result{Requeue: true, RequeueAfter: time.Second}, nil
	}

	if !meta.IsStatusConditionTrue(dummyCluster.Status.Conditions, CondDrivesAdded) {
		// TODO: Move responsibility to weka container to parallelize and watch for their statuses
		if err = r.Get(ctx, client.ObjectKey{Namespace: dummyCluster.Namespace, Name: dummyCluster.Name}, dummyCluster); err != nil {
			return ctrl.Result{}, err
		}
		// TODO: this might stack if original thread crashed, release "lease" from the one that failed, and wait for ttl if it was a crash
		// Moving responsibility to weka container is preferable
		if !meta.IsStatusConditionFalse(dummyCluster.Status.Conditions, CondDrivesAdded) {
			return ctrl.Result{Requeue: true, RequeueAfter: 5 * time.Second}, nil
		}
		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{
			Type:    CondDrivesAdded,
			Status:  metav1.ConditionUnknown,
			Reason:  "Adding",
			Message: fmt.Sprintf("Drives are being added to the cluster, op_id %s", uuid.NewUUID()),
			// Update ensures we are up to date, meaning being leader here,
		})
		err = r.Status().Update(ctx, dummyCluster)
		if err != nil {
			return ctrl.Result{Requeue: true}, nil
		}

		err = r.AddDrives(ctx, dummyCluster, containers)
		if err != nil {
			return ctrl.Result{}, err
		}
		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{
			Type:   CondDrivesAdded,
			Status: metav1.ConditionTrue, Reason: "Success", Message: "Drives are added to the cluster",
		})
		_ = r.Status().Update(ctx, dummyCluster)
	}

	if !meta.IsStatusConditionTrue(dummyCluster.Status.Conditions, CondIoStarted) {
		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{
			Type:   CondIoStarted,
			Status: metav1.ConditionUnknown, Reason: "Starting", Message: "Starting IO",
		})
		_ = r.Status().Update(ctx, dummyCluster)
		err = r.StartIo(ctx, dummyCluster, containers)
		if err != nil {
			return ctrl.Result{}, err
		}
		r.Logger.Info("IO Started")
		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{
			Type:   CondIoStarted,
			Status: metav1.ConditionTrue, Reason: "Success", Message: "IO is started",
		})
		_ = r.Status().Update(ctx, dummyCluster)
	}

	return ctrl.Result{}, nil
}

func (r *DummyClusterReconciler) handleDeletion(ctx context.Context, dummyCluster *wekav1alpha1.DummyCluster) error {
	if controllerutil.ContainsFinalizer(dummyCluster, wekaContainerFinalizer) {
		r.Logger.Info("Performing Finalizer Operations for dummyCluster before delete CR")

		// Perform all operations required before remove the finalizer and allow
		// the Kubernetes API to remove the custom resource.
		r.doFinalizerOperationsFordummyCluster(dummyCluster)

		r.Logger.Info("Removing Finalizer for dummyCluster after successfully perform the operations")
		if ok := controllerutil.RemoveFinalizer(dummyCluster, wekaContainerFinalizer); !ok {
			err := errors.New("Failed to remove finalizer for dummyCluster")
			return err
		}

		if err := r.Update(ctx, dummyCluster); err != nil {
			r.Logger.Error(err, "Failed to remove finalizer for dummyCluster")
			return err
		}

	}
	return nil
}

func (r *DummyClusterReconciler) initState(ctx context.Context, dummyCluster *wekav1alpha1.DummyCluster) error {
	logger := r.Logger.WithName("initState")
	logger.Info("initState() called")
	defer logger.Info("initState() finished")
	if !controllerutil.ContainsFinalizer(dummyCluster, wekaContainerFinalizer) {

		dummyCluster.Status.Conditions = []metav1.Condition{}

		// Set Predefined conditions to explicit False for visibility
		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{
			Type:   CondPodsCreated,
			Status: metav1.ConditionFalse, Reason: "Init",
			Message: "The pods for the custom resource are not created yet",
		})

		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{
			Type:   CondPodsRead,
			Status: metav1.ConditionFalse, Reason: "Init",
			Message: "The pods for the custom resource are not ready yet",
		})

		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{
			Type:   CondClusterCreated,
			Status: metav1.ConditionFalse, Reason: "Init",
			Message: "Cluster is not created yet",
		})

		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{
			Type:   CondDrivesAdded,
			Status: metav1.ConditionFalse, Reason: "Init",
			Message: "Drives are not added yet",
		})

		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{
			Type:   CondIoStarted,
			Status: metav1.ConditionFalse, Reason: "Init",
			Message: "Weka Cluster IO is not started",
		})

		err := r.Status().Update(ctx, dummyCluster)
		if err != nil {
			r.Logger.Error(err, "failed to init states")
		}

		r.Logger.Info("Adding Finalizer for weka cluster")
		if ok := controllerutil.AddFinalizer(dummyCluster, wekaContainerFinalizer); !ok {
			r.Logger.Info("Failed to add finalizer for dummyCluster")
			return errors.New("Failed to add finalizer for dummyCluster")
		}

		if err := r.Update(ctx, dummyCluster); err != nil {
			r.Logger.Error(err, "Failed to update custom resource to add finalizer")
			return err
		}

		if err := r.Get(ctx, client.ObjectKey{Namespace: dummyCluster.Namespace, Name: dummyCluster.Name}, dummyCluster); err != nil {
			r.Logger.Error(err, "Failed to re-fetch data")
			return err
		}
		r.Logger.Info("Finalizer added for dummyCluster", "conditions", len(dummyCluster.Status.Conditions))
	}
	return nil
}

func GetCluster(ctx context.Context, req ctrl.Request, r client.Reader, logger logr.Logger) (*wekav1alpha1.DummyCluster, error) {
	logger = logger.WithName("GetCluster")
	logger.Info("GetCluster() called")
	defer logger.Info("GetCluster() finished")

	dummyCluster := &wekav1alpha1.DummyCluster{}
	err := r.Get(ctx, req.NamespacedName, dummyCluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("dummyCluster resource not found. Ignoring since object must be deleted")
			return nil, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get dummyCluster")
		return nil, err
	}
	return dummyCluster, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DummyClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.DummyCluster{}).
		Owns(&wekav1alpha1.WekaContainer{}).
		Complete(r)
}

func (r *DummyClusterReconciler) doFinalizerOperationsFordummyCluster(cluster *wekav1alpha1.DummyCluster) {
	r.Recorder.Event(cluster, "Warning", "Deleting",
		fmt.Sprintf("Custom Resource %s is being deleted from the namespace %s",
			cluster.Name,
			cluster.Namespace))
}

func (r *DummyClusterReconciler) ensureWekaContainers(ctx context.Context, cluster *wekav1alpha1.DummyCluster, secret *v1.Secret) ([]*wekav1alpha1.WekaContainer, error) {
	logger := r.Logger.WithName("ensureWekaContainers")
	logger.Info("ensureWekaContainers() called")
	defer logger.Info("ensureWekaContainers() finished")

	// iterate over cluster.Spec.Size, search for WekaContainer object, and create if not found, populating node affinity from spec Hosts list mapping to index
	agentPort := cluster.Spec.AgentBasePort
	containerPort := cluster.Spec.ContainerBasePort

	roles := []string{"compute", "drive"}

	foundContainers := []*wekav1alpha1.WekaContainer{}

	for i := 0; i < cluster.Spec.Size; i++ {
		// Check if the WekaContainer object exists
		core := cluster.Spec.BaseCoreId
		for _, role := range roles {
			wekaContainer, err := r.newWekaContainerForDummyCluster(cluster, i, role, containerPort, agentPort, core, secret)
			if err != nil {
				return nil, err
			}
			core += cluster.Spec.CoreStep

			agentPort++
			containerPort += 100

			found := &wekav1alpha1.WekaContainer{}
			err = r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: fmt.Sprintf("%s-%s-%d", cluster.Name, role, i)}, found)
			if err != nil && apierrors.IsNotFound(err) {
				// Define a new WekaContainer object
				err = r.Create(ctx, wekaContainer)
				if err != nil {
					return nil, err
				}
			}
			foundContainers = append(foundContainers, found)
		}
	}

	hostList := []string{}
	for _, host := range cluster.Spec.Hosts {
		for i := 0; i < 2; i++ {
			hostList = append(hostList, host)
		}
	}
	return foundContainers, nil
}

func (r *DummyClusterReconciler) newWekaContainerForDummyCluster(cluster *wekav1alpha1.DummyCluster, i int, role string, port, agentPort, core int, secret *v1.Secret) (*wekav1alpha1.WekaContainer, error) {
	labels := map[string]string{
		"app": cluster.Name,
	}

	container := &wekav1alpha1.WekaContainer{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "weka.weka.io/v1alpha1",
			Kind:       "WekaContainer",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s-%d", cluster.Name, role, i),
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: wekav1alpha1.WekaContainerSpec{
			NodeAffinity:      cluster.Spec.Hosts[i],
			Port:              port,
			AgentPort:         agentPort,
			Image:             cluster.Spec.Image,
			ImagePullSecret:   cluster.Spec.ImagePullSecret,
			WekaContainerName: fmt.Sprintf("%s%ss%d", cluster.Spec.WekaContainerNamePrefix, role, i),
			Mode:              role,
			NumCores:          1,
			CoreIds:           []int{core},
			Network: wekav1alpha1.Network{
				EthDevice: cluster.Spec.NetworkSelector.EthDevice,
			},
			Hugepages: cluster.Spec.Hugepages,
			WekaUsername: v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{Name: secret.Name},
					Key:                  "username",
				},
			},
			WekaPassword: v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{Name: secret.Name},
					Key:                  "password",
				},
			},
		},
	}

	if err := ctrl.SetControllerReference(cluster, container, r.Scheme); err != nil {
		return nil, err
	}

	return container, nil
}

func (r *DummyClusterReconciler) CreateCluster(ctx context.Context, cluster *wekav1alpha1.DummyCluster, containers []*wekav1alpha1.WekaContainer) error {
	logger := r.Logger.WithName("CreateCluster")
	logger.Info("CreateCluster() called")
	defer logger.Info("CreateCluster() finished")

	var hostIps []string
	var hostnamesList []string
	for _, container := range containers {
		hostIps = append(hostIps, fmt.Sprintf("%s:%d", container.Status.ManagementIP, container.Spec.Port))
		hostnamesList = append(hostnamesList, container.Status.ManagementIP)
	}
	hostIpsStr := strings.Join(hostIps, ",")
	cmd := fmt.Sprintf("weka status || weka cluster create %s --host-ips %s", strings.Join(hostnamesList, " "), hostIpsStr)

	r.Logger.Info("Creating cluster", "cmd", cmd)

	executor, err := GetExecutor(containers[0], r.Logger)
	if err != nil {
		return errors.Wrap(err, "Could not create executor")
	}
	stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to create cluster: %s", stderr.String())
	}
	r.Logger.Info("Cluster created", "stdout", stdout.String(), "stderr", stderr.String())

	if err := r.Status().Update(ctx, cluster); err != nil {
		return errors.Wrap(err, "Failed to update dummyCluster status")
	}

	return nil
}

func (r *DummyClusterReconciler) AddDrives(ctx context.Context, cluster *wekav1alpha1.DummyCluster, containers []*wekav1alpha1.WekaContainer) error {
	// TODO: Parallelize
	for _, container := range containers {
		// get executor for container
		if container.Spec.Mode != "drive" {
			continue
		}
		// TODO: Check if already done, again, Condition
		executor, err := GetExecutor(container, r.Logger)
		if err != nil {
			return errors.Wrap(err, "Error creating executor")
		}
		// TODO: Needs more safety!!!!
		cmd := fmt.Sprintf("weka local exec sgdisk -Z %s", cluster.Spec.Drive)
		stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
		if err != nil {
			return errors.Wrapf(err, "Failed to erase disk: %s", stderr.String())
		}

		cmd = fmt.Sprintf("weka local exec /weka/tools/weka_sign_drive %s", cluster.Spec.Drive)
		stdout, stderr, err = executor.Exec(ctx, []string{"bash", "-ce", cmd})
		if err != nil {
			return errors.Wrapf(err, "Failed to sign disk: %s", stderr.String())
		}

		cmd = fmt.Sprintf("weka cluster drive add %d %s", *container.Status.ClusterContainerID, cluster.Spec.Drive)
		stdout, stderr, err = executor.Exec(ctx, []string{"bash", "-ce", cmd})
		if err != nil {
			return errors.Wrapf(err, "Failed to sign disk: %s", stderr.String())
		}
		r.Logger.Info("drive added", "stderr", stderr.String(), "stdout", stdout.String())
	}
	return nil
}

func GetExecutor(container *wekav1alpha1.WekaContainer, logger logr.Logger) (*util.Exec, error) {
	pod, err := resources.NewContainerFactory(container, logger).Create()
	if err != nil {
		return nil, errors.Wrap(err, "Could not find executor pod")
	}
	executor, err := util.NewExecInPod(pod)
	if err != nil {
		return nil, errors.Wrap(err, "Could not create executor")
	}
	return executor, nil
}

func (r *DummyClusterReconciler) EnsureClusterContainerIds(ctx context.Context, cluster *wekav1alpha1.DummyCluster, containers []*wekav1alpha1.WekaContainer) error {
	var containersMap resources.ClusterContainersMap

	fetchContainers := func() error {
		pod, err := resources.NewContainerFactory(containers[0], r.Logger).Create()
		if err != nil {
			return errors.Wrap(err, "Could not find executor pod")
		}
		clusterizePod := &v1.Pod{}
		err = r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: pod.Name}, clusterizePod)
		executor, err := util.NewExecInPod(clusterizePod)
		if err != nil {
			return errors.Wrap(err, "Could not create executor")
		}
		cmd := "weka cluster container -J"
		stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
		if err != nil {
			return errors.Wrapf(err, "Failed to fetch containers list from cluster")
		}
		response := resources.ClusterContainersResponse{}
		err = json.Unmarshal(stdout.Bytes(), &response)
		if err != nil {
			return errors.Wrapf(err, "Failed to create cluster: %s", stderr.String())
		}
		containersMap, err = resources.MapByContainerName(response)
		if err != nil {
			return errors.Wrapf(err, "Failed to map containers")
		}
		return nil
	}

	for _, container := range containers {
		if container.Status.ClusterContainerID == nil {
			if containersMap == nil {
				err := fetchContainers()
				if err != nil {
					return err
				}
			}

			if clusterContainer, ok := containersMap[container.Spec.WekaContainerName]; !ok {
				return errors.New("Container " + container.Spec.WekaContainerName + " not found in cluster")
			} else {
				containerId, err := clusterContainer.ContainerId()
				if err != nil {
					return errors.Wrap(err, "Failed to parse container id")
				}
				container.Status.ClusterContainerID = &containerId
				if err := r.Status().Update(ctx, container); err != nil {
					return errors.Wrap(err, "Failed to update container status")
				}
			}
		}
	}
	return nil
}

func (r *DummyClusterReconciler) StartIo(ctx context.Context, cluster *wekav1alpha1.DummyCluster, containers []*wekav1alpha1.WekaContainer) error {
	executor, err := GetExecutor(containers[0], r.Logger)
	if err != nil {
		return errors.Wrap(err, "Error creating executor")
	}

	cmd := "weka cluster start-io"
	_, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to start-io: %s", stderr.String())
	}

	r.Logger.Info("Starting IO")
	return nil
}

func (r *DummyClusterReconciler) isContainersReady(containers []*wekav1alpha1.WekaContainer) (bool, error) {
	logger := r.Logger.WithName("isContainersReady")
	logger.Info("isContainersReady() called")
	defer logger.Info("isContainersReady() finished")

	for _, container := range containers {
		if container.GetDeletionTimestamp() != nil {
			return false, errors.New("Container " + container.Name + " is being deleted, rejecting cluster create")
		}
		if container.Status.ManagementIP == "" {
			logger.Info("Container not ready, ManagementIP not set", "name", container.Name)
			return false, nil
		}

		if container.Status.Status != "Running" {
			logger.Info("Container not ready, Status not Running", "name", container.Name)
			return false, nil
		}
	}
	logger.Info("All containers are ready")
	return true, nil
}

// Run `weka user passwd` to set the password on the cluster
func (r *DummyClusterReconciler) setClusterPassword(ctx context.Context, cluster *wekav1alpha1.DummyCluster, containers []*wekav1alpha1.WekaContainer) (ctrl.Result, error) {
	logger := r.Logger.WithName("setClusterPassword")
	logger.Info("setClusterPassword() called")
	defer logger.Info("setClusterPassword() finished")

	secretResourceId := client.ObjectKey{Namespace: cluster.Namespace, Name: "operator-admin-password"}
	// Get the password from the secret
	secret := &v1.Secret{}
	if err := r.Get(ctx, secretResourceId, secret); err != nil {
		logger.Error(err, "Failed to get secret")
		return ctrl.Result{}, err
	}
	username := string(secret.Data["username"])
	password := string(secret.Data["password"])

	// Test credentials
	// See if credentials need updating by trying to login with them
	pod := &v1.Pod{}
	execPodId := client.ObjectKey{Namespace: cluster.Namespace, Name: containers[0].Name}
	if err := r.Get(ctx, execPodId, pod); err != nil {
		logger.Error(err, "Failed to get exec pod")
		return ctrl.Result{}, err
	}
	executor, err := util.NewExecInPod(pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return ctrl.Result{}, err
	}
	cmd := fmt.Sprintf("weka user login %s %s", username, password)
	_, _, err = executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err == nil {
		// Credentials are valid, nothing to do
		logger.Info("Credentials are valid")
		return ctrl.Result{}, nil
	}

	// Credentials in secret are not yet valid
	// Create new user with the same credentials
	// Since the secret credentials are not yet valid, use the default creds to
	// login
	cmd = fmt.Sprintf(
		"WEKA_USERNAME=admin WEKA_PASSWORD=admin weka user add %s %s %s",
		username,
		"clusteradmin",
		password)
	_, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		logger.Info("Command failed", "cmd", cmd)
		logger.Error(err, "Failed to set password", "stderr", stderr.String())
		return ctrl.Result{}, err
	}

	logger.Info("Cluster password updated")
	return ctrl.Result{}, nil
}

func (r *DummyClusterReconciler) ensureSecret(ctx context.Context, key client.ObjectKey, secret *v1.Secret) (ctrl.Result, error) {
	logger := r.Logger.WithName("ensureSecret")
	err := r.Get(ctx, key, secret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Generate a random password
			password, err := generateRandomPassword()
			if err != nil {
				logger.Error(err, "Failed to generate random password")
				return ctrl.Result{}, err
			}
			secret = &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      key.Name,
					Namespace: key.Namespace,
				},
				StringData: map[string]string{
					"username": "weka-operator-admin",
					"password": password,
				},
			}
			if err := r.Create(ctx, secret); err != nil {
				logger.Error(err, "Failed to create secret")
				return ctrl.Result{}, err
			}
			logger.Info("Secret created", "name", secret.Name, "namespace", secret.Namespace)
			return ctrl.Result{Requeue: true}, nil
		} else {
			logger.Error(err, "Error getting secret")
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func generateRandomPassword() (string, error) {
	// password must contain at least 8 characters, with an uppercase letter, a lowercase letter and a number or a special character
	const length = 16
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*()_+"

	bytes := make([]byte, length)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}
