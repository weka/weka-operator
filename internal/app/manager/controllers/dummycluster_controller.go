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
	"encoding/json"
	"fmt"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/util"
	"gopkg.in/yaml.v3"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"slices"
	"strings"
	"time"

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
	CondJoinedCluster  = "JoinedCluster"
)

// +kubebuilder:rbac:groups=weka.weka.io,resources=dummyclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=weka.weka.io,resources=dummyclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=weka.weka.io,resources=dummyclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get,list
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

	containers, err := r.ensureWekaContainers(ctx, dummyCluster)
	if err != nil {
		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{Type: CondPodsCreated,
			Status: metav1.ConditionFalse, Reason: "Error", Message: err.Error()})
		_ = r.Status().Update(ctx, dummyCluster)
		r.Logger.Error(err, "Failed to ensure WekaContainers")
		return ctrl.Result{}, err
	}
	meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{Type: CondPodsCreated,
		Status: metav1.ConditionTrue, Reason: "Success", Message: "All pods are created"})
	_ = r.Status().Update(ctx, dummyCluster)

	if meta.IsStatusConditionFalse(dummyCluster.Status.Conditions, CondPodsRead) {
		if ready, err := r.isContainersReady(containers); !ready {
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second}, err
		}
		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{Type: CondPodsRead,
			Status: metav1.ConditionTrue, Reason: "Success", Message: "All weka containers are ready for clusterization"})
		_ = r.Status().Update(ctx, dummyCluster)
	}

	if meta.IsStatusConditionFalse(dummyCluster.Status.Conditions, CondClusterCreated) {
		err = r.CreateCluster(ctx, dummyCluster, containers)
		if err != nil {
			return ctrl.Result{}, err
		}
		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{Type: CondClusterCreated,
			Status: metav1.ConditionTrue, Reason: "Success", Message: "Cluster is formed"})
		_ = r.Status().Update(ctx, dummyCluster)
	}

	// Ensure all containers are up in the cluster
	for _, container := range containers {
		if !meta.IsStatusConditionTrue(container.Status.Conditions, CondJoinedCluster) {
			r.Logger.Info("Container has not joined the cluster yet", "container", container.Name)
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, nil
		} else {
			if dummyCluster.Status.ClusterID == "" {
				dummyCluster.Status.ClusterID = container.Status.ClusterID
				err := r.Status().Update(ctx, dummyCluster)
				if err != nil {
					return ctrl.Result{}, err
				}
			}
		}
	}

	err = r.EnsureClusterContainerIds(ctx, dummyCluster, containers)
	if err != nil {
		r.Logger.Info("not all containers are up in the cluster", "err", err)
		return ctrl.Result{Requeue: true, RequeueAfter: time.Second}, nil
	}

	// Ensure all containers are up in the cluster
	for _, container := range containers {
		if container.Spec.Mode != "drive" {
			continue
		}
		if !meta.IsStatusConditionTrue(container.Status.Conditions, CondDrivesAdded) {
			r.Logger.Info("Containers did not add drives yet", "container", container.Name)
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, nil
		}
	}

	if !meta.IsStatusConditionTrue(dummyCluster.Status.Conditions, CondIoStarted) {
		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{Type: CondIoStarted,
			Status: metav1.ConditionUnknown, Reason: "Starting", Message: "Starting IO"})
		_ = r.Status().Update(ctx, dummyCluster)
		err = r.StartIo(ctx, dummyCluster, containers)
		if err != nil {
			return ctrl.Result{}, err
		}
		r.Logger.Info("IO Started, time since create:" + time.Since(dummyCluster.CreationTimestamp.Time).String())
		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{Type: CondIoStarted,
			Status: metav1.ConditionTrue, Reason: "Success", Message: "IO is started"})
		_ = r.Status().Update(ctx, dummyCluster)
	}

	return ctrl.Result{}, nil
}

func (r *DummyClusterReconciler) handleDeletion(ctx context.Context, dummyCluster *wekav1alpha1.DummyCluster) error {
	if controllerutil.ContainsFinalizer(dummyCluster, wekaContainerFinalizer) {
		r.Logger.Info("Performing Finalizer Operations for dummyCluster before delete CR")

		// Perform all operations required before remove the finalizer and allow
		// the Kubernetes API to remove the custom resource.
		err := r.doFinalizerOperationsFordummyCluster(ctx, dummyCluster)
		if err != nil {
			return err
		}

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
	if !controllerutil.ContainsFinalizer(dummyCluster, wekaContainerFinalizer) {

		dummyCluster.Status.Conditions = []metav1.Condition{}

		// Set Predefined conditions to explicit False for visibility
		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{Type: CondPodsCreated,
			Status: metav1.ConditionFalse, Reason: "Init",
			Message: "The pods for the custom resource are not created yet"})

		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{Type: CondPodsRead,
			Status: metav1.ConditionFalse, Reason: "Init",
			Message: "The pods for the custom resource are not ready yet"})

		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{Type: CondClusterCreated,
			Status: metav1.ConditionFalse, Reason: "Init",
			Message: "Cluster is not created yet"})

		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{Type: CondDrivesAdded,
			Status: metav1.ConditionFalse, Reason: "Init",
			Message: "Drives are not added yet"})

		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{Type: CondIoStarted,
			Status: metav1.ConditionFalse, Reason: "Init",
			Message: "Weka Cluster IO is not started"})

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

func (r *DummyClusterReconciler) doFinalizerOperationsFordummyCluster(ctx context.Context, cluster *wekav1alpha1.DummyCluster) error {
	if cluster.Spec.Topology == "" {
		return nil
	}
	topology, err := Topologies[cluster.Spec.Topology](ctx, r)
	if err != nil {
		return err
	}
	allocator := NewAllocator(r.Logger, topology)
	allocMap, allocConfigMap, err := r.GetOrInitAllocMap(ctx)
	if err != nil {
		r.Logger.Error(err, "Failed to get alloc map")
		return err
	}

	changed := allocator.DeallocateCluster(OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace}, allocMap)
	if changed {
		if err := r.UpdateAllocationMap(ctx, allocMap, allocConfigMap); err != nil {
			r.Logger.Error(err, "Failed to update alloc map")
			return err
		}
	}
	r.Recorder.Event(cluster, "Warning", "Deleting",
		fmt.Sprintf("Custom Resource %s is being deleted from the namespace %s",
			cluster.Name,
			cluster.Namespace))
	return nil
}

func (r *DummyClusterReconciler) ensureWekaContainers(ctx context.Context, cluster *wekav1alpha1.DummyCluster) ([]*wekav1alpha1.WekaContainer, error) {
	allocMap, allocConfigMap, err := r.GetOrInitAllocMap(ctx)
	if err != nil {
		return nil, err
	}

	foundContainers := []*wekav1alpha1.WekaContainer{}
	template := WekaClusterTemplates[cluster.Spec.Template]
	topology, err := Topologies[cluster.Spec.Topology](ctx, r)
	allocator := NewAllocator(r.Logger, topology)
	allocMap, err, changed := allocator.Allocate(OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace}, template, allocMap, cluster.Spec.Size)
	if err != nil {
		return nil, err
	}
	if changed {
		if err := r.UpdateAllocationMap(ctx, allocMap, allocConfigMap); err != nil {
			return nil, err
		}
	}

	size := cluster.Spec.Size
	if size == 0 {
		size = 1
	}

	ensureContainers := func(role string, containersNum int) error {
		for i := 0; i < containersNum; i++ {
			// Check if the WekaContainer object exists
			owner := Owner{OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace},
				fmt.Sprintf("%s%d", role, i), role} // apparently need helper function with a role.

			ownedResources, _ := GetOwnedResources(owner, allocMap)
			wekaContainer, err := r.newWekaContainerForDummyCluster(cluster, ownedResources, template, topology, role, i)
			if err != nil {
				return err
			}

			found := &wekav1alpha1.WekaContainer{}
			err = r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: wekaContainer.Name}, found)
			if err != nil && apierrors.IsNotFound(err) {
				// Define a new WekaContainer object
				err = r.Create(ctx, wekaContainer)
				if err != nil {
					return err
				}
				foundContainers = append(foundContainers, wekaContainer)
			} else {
				foundContainers = append(foundContainers, found)
			}
		}
		return nil
	}
	if err := ensureContainers("drive", template.DriveContainers); err != nil {
		return nil, err
	}
	if err := ensureContainers("compute", template.ComputeContainers); err != nil {
		return nil, err
	}
	return foundContainers, nil
}

func (r *DummyClusterReconciler) GetOrInitAllocMap(ctx context.Context) (AllocationsMap, *v1.ConfigMap, error) {
	// fetch alloc map from configmap
	allocMap := AllocationsMap{}
	yamlData, err := yaml.Marshal(&allocMap)

	allocMapConfigMap := &v1.ConfigMap{}
	err = r.Get(ctx, client.ObjectKey{Namespace: util.GetPodNamespace(), Name: "weka-operator-allocmap"}, allocMapConfigMap)
	if err != nil && apierrors.IsNotFound(err) {
		// Define a new ConfigMap
		allocMapConfigMap = &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "weka-operator-allocmap",
				Namespace: util.GetPodNamespace(),
			},
			Data: map[string]string{
				"allocmap.yaml": string(yamlData),
			},
		}
		err = r.Create(ctx, allocMapConfigMap)
		if err != nil {
			return nil, nil, err
		}
	} else {
		if err != nil {
			return nil, nil, err
		}
		err = yaml.Unmarshal([]byte(allocMapConfigMap.Data["allocmap.yaml"]), &allocMap)
		if err != nil {
			return nil, nil, err
		}
	}
	return allocMap, allocMapConfigMap, nil
}

func (r *DummyClusterReconciler) newWekaContainerForDummyCluster(cluster *wekav1alpha1.DummyCluster,
	ownedResources OwnedResources,
	template ClusterTemplate,
	topology Topology,
	role string, i int) (*wekav1alpha1.WekaContainer, error) {
	labels := map[string]string{
		"app": cluster.Name,
	}

	var hugePagesNum int
	if role == "drive" {
		hugePagesNum = template.DriveHugepages
	} else {
		hugePagesNum = template.ComputeHugepages
	}

	network := wekav1alpha1.Network{}
	// These are on purpose different types
	// Network selector might be "Aws" or "auto" and that will prepare EthDevice for container-level, which will be simpler
	if topology.Network.EthDevice != "" {
		network.EthDevice = topology.Network.EthDevice
	}
	if topology.Network.UdpMode {
		network.UdpMode = true
	}

	potentialDrives := ownedResources.Drives[:]
	availableDrives := topology.GetAllNodesDrives(ownedResources.Node)
	for i := 0; i < len(availableDrives); i++ {
		if slices.Contains(potentialDrives, availableDrives[i]) {
			continue
		}
		potentialDrives = append(potentialDrives, availableDrives[i])
	}
	// Selected by ownership drives are first in the list and will be attempted first, granting happy flow

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
			NodeAffinity:      ownedResources.Node,
			Port:              ownedResources.Port,
			AgentPort:         ownedResources.AgentPort,
			Image:             cluster.Spec.Image,
			ImagePullSecret:   cluster.Spec.ImagePullSecret,
			WekaContainerName: fmt.Sprintf("%s%ss%d", cluster.Spec.WekaContainerNamePrefix, role, i),
			Mode:              role,
			NumCores:          len(ownedResources.CoreIds),
			CoreIds:           ownedResources.CoreIds,
			Network:           network,
			Hugepages:         hugePagesNum,
			HugepagesSize:     template.HugePageSize,
			HugepagesOverride: template.HugePagesOverride,
			NumDrives:         len(ownedResources.Drives),
			PotentialDrives:   potentialDrives,
		},
	}

	if err := ctrl.SetControllerReference(cluster, container, r.Scheme); err != nil {
		return nil, err
	}

	return container, nil
}

func (r *DummyClusterReconciler) CreateCluster(ctx context.Context, cluster *wekav1alpha1.DummyCluster, containers []*wekav1alpha1.WekaContainer) error {

	var hostIps []string
	var hostnamesList []string
	r.Logger.Info("Creating cluster", "totalContainers", len(containers))
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
	// TODO: Parallelize by moving into weka container responsibility
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
		for _, drive := range container.Spec.PotentialDrives[:container.Spec.NumDrives] {
			// TODO: Needs more safety!!!!
			cmd := fmt.Sprintf("weka local exec sgdisk -Z %s", drive)
			stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
			if err != nil {
				return errors.Wrapf(err, "Failed to erase disk: %s", stderr.String())
			}

			cmd = fmt.Sprintf("weka local exec /weka/tools/weka_sign_drive %s", drive)
			stdout, stderr, err = executor.Exec(ctx, []string{"bash", "-ce", cmd})
			if err != nil {
				return errors.Wrapf(err, "Failed to sign disk: %s", stderr.String())
			}

			cmd = fmt.Sprintf("weka cluster drive add %d %s", *container.Status.ClusterContainerID, drive)
			stdout, stderr, err = executor.Exec(ctx, []string{"bash", "-ce", cmd})
			if err != nil {
				return errors.Wrapf(err, "Failed to sign disk: %s", stderr.String())
			}
			r.Logger.Info("drive added", "stderr", stderr.String(), "stdout", stdout.String())

		}
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
	for _, container := range containers {
		if container.GetDeletionTimestamp() != nil {
			return false, errors.New("Container " + container.Name + " is being deleted, rejecting cluster create")
		}
		if container.Status.ManagementIP == "" {
			return false, nil
		}

		if container.Status.Status != "Running" {
			return false, nil
		}
	}
	return true, nil
}

func (r *DummyClusterReconciler) UpdateAllocationMap(ctx context.Context, allocMap AllocationsMap, configMap *v1.ConfigMap) error {
	yamlData, err := yaml.Marshal(&allocMap)
	if err != nil {
		return err
	}
	configMap.Data["allocmap.yaml"] = string(yamlData)
	err = r.Update(ctx, configMap)
	if err != nil {
		return err
	}
	return nil
}
