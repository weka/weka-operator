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
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/kr/pretty"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/util"
	"gopkg.in/yaml.v3"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const WekaFinalizer = "weka.weka.io/finalizer"

// WekaClusterReconciler reconciles a WekaCluster object
type WekaClusterReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Logger   logr.Logger
}

func NewWekaClusterController(mgr ctrl.Manager) *WekaClusterReconciler {
	return &WekaClusterReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Logger:   mgr.GetLogger().WithName("controllers").WithName("WekaCluster"),
		Recorder: mgr.GetEventRecorderFor("wekaCluster-controller"),
	}
}

func (r *WekaClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.WithName("Reconcile")
	logger.Info("Reconcile() called")
	defer logger.Info("Reconcile() finished")
	// Fetch the WekaCluster instance
	wekaCluster, err := GetCluster(ctx, req, r.Client, r.Logger)
	if err != nil {
		return ctrl.Result{}, err
	}
	if wekaCluster == nil {
		return ctrl.Result{}, nil
	}

	err = r.initState(ctx, wekaCluster)
	if err != nil {
		logger.Error(err, "Failed to initialize state")
		return ctrl.Result{}, err
	}

	if wekaCluster.GetDeletionTimestamp() != nil {
		err = r.handleDeletion(ctx, wekaCluster)
		if err != nil {
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, err
		}
		logger.Info("Deleting wekaCluster")
		return ctrl.Result{}, nil
	}

	// generate login credentials
	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondClusterSecretsCreated) {
		err = r.ensureLoginCredentials(ctx, wekaCluster)
		if err != nil {
			return ctrl.Result{}, err
		}

		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondClusterSecretsCreated,
			Status: metav1.ConditionTrue, Reason: "Init", Message: "Cluster secrets are created",
		})
		_ = r.Status().Update(ctx, wekaCluster)
	}
	// Note: All use of conditions is only as hints for skipping actions and a visibility, not strictly a state machine
	// All code should be idempotent and not rely on conditions for correctness, hence validation of succesful update of conditions is not done

	containers, err := r.ensureWekaContainers(ctx, wekaCluster)
	if err != nil {
		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondPodsCreated,
			Status: metav1.ConditionFalse, Reason: "Error", Message: err.Error(),
		})
		_ = r.Status().Update(ctx, wekaCluster)
		r.Logger.Error(err, "Failed to ensure WekaContainers")
		return ctrl.Result{RequeueAfter: time.Second * 3}, err
	}
	meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondPodsCreated,
		Status: metav1.ConditionTrue, Reason: "Init", Message: "All pods are created",
	})
	_ = r.Status().Update(ctx, wekaCluster)

	if meta.IsStatusConditionFalse(wekaCluster.Status.Conditions, condition.CondPodsReady) {
		if ready, err := r.isContainersReady(containers); !ready {
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second}, err
		}
		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondPodsReady,
			Status: metav1.ConditionTrue, Reason: "Init", Message: "All weka containers are ready for clusterization",
		})
		_ = r.Status().Update(ctx, wekaCluster)
	}

	if meta.IsStatusConditionFalse(wekaCluster.Status.Conditions, condition.CondClusterCreated) {
		err = r.CreateCluster(ctx, wekaCluster, containers)
		if err != nil {
			logger.Error(err, "Failed to create cluster")
			meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
				Type:   condition.CondClusterCreated,
				Status: metav1.ConditionFalse, Reason: "Error", Message: err.Error(),
			})
			_ = r.Status().Update(ctx, wekaCluster)
			return ctrl.Result{}, err
		}
		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondClusterCreated,
			Status: metav1.ConditionTrue, Reason: "Init", Message: "Cluster is formed",
		})
		_ = r.Status().Update(ctx, wekaCluster)
	}

	// Ensure all containers are up in the cluster
	for _, container := range containers {
		if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondJoinedCluster) {
			r.Logger.Info("Container has not joined the cluster yet", "container", container.Name)
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, nil
		} else {
			if wekaCluster.Status.ClusterID == "" {
				wekaCluster.Status.ClusterID = container.Status.ClusterID
				err := r.Status().Update(ctx, wekaCluster)
				if err != nil {
					return ctrl.Result{}, err
				}
			}
		}
	}

	err = r.EnsureClusterContainerIds(ctx, wekaCluster, containers)
	if err != nil {
		r.Logger.Info("not all containers are up in the cluster", "err", err)
		return ctrl.Result{Requeue: true, RequeueAfter: time.Second}, nil
	}

	// Ensure all containers are up in the cluster
	for _, container := range containers {
		if container.Spec.Mode != "drive" {
			continue
		}
		if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondDrivesAdded) {
			r.Logger.Info("Containers did not add drives yet", "container", container.Name)
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, nil
		}
	}
	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondDrivesAdded) {
		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondDrivesAdded,
			Status: metav1.ConditionTrue, Reason: "Init", Message: "All drives are added",
		})
		err = r.Status().Update(ctx, wekaCluster)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondIoStarted) {
		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondIoStarted,
			Status: metav1.ConditionUnknown, Reason: "Init", Message: "Starting IO",
		})
		_ = r.Status().Update(ctx, wekaCluster)
		r.Logger.Info("Starting IO")
		err = r.StartIo(ctx, wekaCluster, containers)
		if err != nil {
			return ctrl.Result{}, err
		}
		r.Logger.Info("IO Started, time since create:" + time.Since(wekaCluster.CreationTimestamp.Time).String())
		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondIoStarted,
			Status: metav1.ConditionTrue, Reason: "Init", Message: "IO is started",
		})
		_ = r.Status().Update(ctx, wekaCluster)
	}

	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondClusterSecretsApplied) {
		err = r.applyClusterCredentials(ctx, wekaCluster, containers)
		if err != nil {
			return ctrl.Result{}, err
		}

		meta.SetStatusCondition(&wekaCluster.Status.Conditions, metav1.Condition{
			Type:   condition.CondClusterSecretsApplied,
			Status: metav1.ConditionTrue, Reason: "Init", Message: "Applied cluster secrets",
		})
		wekaCluster.Status.Status = "Ready"
		err = r.Status().Update(ctx, wekaCluster)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *WekaClusterReconciler) handleDeletion(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error {
	if controllerutil.ContainsFinalizer(wekaCluster, WekaFinalizer) {
		r.Logger.Info("Performing Finalizer Operations for wekaCluster before delete CR")

		// Perform all operations required before remove the finalizer and allow
		// the Kubernetes API to remove the custom resource.
		err := r.doFinalizerOperationsForwekaCluster(ctx, wekaCluster)
		if err != nil {
			return err
		}

		r.Logger.Info("Removing Finalizer for wekaCluster after successfully perform the operations")
		if ok := controllerutil.RemoveFinalizer(wekaCluster, WekaFinalizer); !ok {
			err := errors.New("Failed to remove finalizer for wekaCluster")
			return err
		}

		if err := r.Update(ctx, wekaCluster); err != nil {
			r.Logger.Error(err, "Failed to remove finalizer for wekaCluster")
			return err
		}

	}
	return nil
}

func (r *WekaClusterReconciler) initState(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error {
	if !controllerutil.ContainsFinalizer(wekaCluster, WekaFinalizer) {

		wekaCluster.Status.InitStatus()

		err := r.Status().Update(ctx, wekaCluster)
		if err != nil {
			r.Logger.Error(err, "failed to init states")
		}

		r.Logger.Info("Adding Finalizer for weka cluster")
		if ok := controllerutil.AddFinalizer(wekaCluster, WekaFinalizer); !ok {
			r.Logger.Info("Failed to add finalizer for wekaCluster")
			return errors.New("Failed to add finalizer for wekaCluster")
		}

		if err := r.Update(ctx, wekaCluster); err != nil {
			r.Logger.Error(err, "Failed to update custom resource to add finalizer")
			return err
		}

		if err := r.Get(ctx, client.ObjectKey{Namespace: wekaCluster.Namespace, Name: wekaCluster.Name}, wekaCluster); err != nil {
			r.Logger.Error(err, "Failed to re-fetch data")
			return err
		}
		r.Logger.Info("Finalizer added for wekaCluster", "conditions", len(wekaCluster.Status.Conditions))
	}
	return nil
}

func GetCluster(ctx context.Context, req ctrl.Request, r client.Reader, logger logr.Logger) (*wekav1alpha1.WekaCluster, error) {
	wekaCluster := &wekav1alpha1.WekaCluster{}
	err := r.Get(ctx, req.NamespacedName, wekaCluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("wekaCluster resource not found. Ignoring since object must be deleted")
			return nil, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get wekaCluster")
		return nil, err
	}
	return wekaCluster, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WekaClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.WekaCluster{}).
		Owns(&wekav1alpha1.WekaContainer{}).
		Complete(r)
}

func (r *WekaClusterReconciler) doFinalizerOperationsForwekaCluster(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error {
	if cluster.Spec.Topology == "" {
		return nil
	}
	topology, err := Topologies[cluster.Spec.Topology](ctx, r, cluster.Spec.NodeSelector)
	if err != nil {
		return err
	}
	allocator := NewAllocator(r.Logger, topology)
	allocations, allocConfigMap, err := r.GetOrInitAllocMap(ctx)
	if err != nil {
		r.Logger.Error(err, "Failed to get alloc map")
		return err
	}

	changed := allocator.DeallocateCluster(OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace}, allocations)
	if changed {
		if err := r.UpdateAllocationsConfigmap(ctx, allocations, allocConfigMap); err != nil {
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

func (r *WekaClusterReconciler) ensureWekaContainers(ctx context.Context, cluster *wekav1alpha1.WekaCluster) ([]*wekav1alpha1.WekaContainer, error) {
	logger := r.Logger.WithName("ensureWekaContainers")
	allocations, allocConfigMap, err := r.GetOrInitAllocMap(ctx)
	if err != nil {
		logger.Error(err, "Failed to get alloc map")
		return nil, err
	}

	foundContainers := []*wekav1alpha1.WekaContainer{}
	template := WekaClusterTemplates[cluster.Spec.Template]
	topology, err := Topologies[cluster.Spec.Topology](ctx, r, cluster.Spec.NodeSelector)
	if err != nil {
		logger.Error(err, "Failed to get topology", "topology", cluster.Spec.Topology)
		return nil, err
	}
	allocator := NewAllocator(r.Logger, topology)
	allocations, err, changed := allocator.Allocate(OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace}, template, allocations, cluster.Spec.Size)
	if err != nil {
		logger.Error(err, "Failed to allocate resources")
		return nil, err
	}
	if changed {
		if err := r.UpdateAllocationsConfigmap(ctx, allocations, allocConfigMap); err != nil {
			logger.Error(err, "Failed to update alloc map")
			return nil, err
		}
	}

	size := cluster.Spec.Size
	if size == 0 {
		size = 1
	}

	ensureContainers := func(role string, containersNum int) error {
		logger := logger.WithName("ensureContainers").WithValues("role", role, "containersNum", containersNum)
		for i := 0; i < containersNum; i++ {
			// Check if the WekaContainer object exists
			owner := Owner{
				OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace},
				fmt.Sprintf("%s%d", role, i), role,
			} // apparently need helper function with a role.

			ownedResources, _ := GetOwnedResources(owner, allocations)
			wekaContainer, err := r.newWekaContainerForWekaCluster(cluster, ownedResources, template, topology, role, i)
			if err != nil {
				logger.Error(err, "Failed to create WekaContainer")
				return err
			}

			found := &wekav1alpha1.WekaContainer{}
			err = r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: wekaContainer.Name}, found)
			if err != nil && apierrors.IsNotFound(err) {
				// Define a new WekaContainer object
				err = r.Create(ctx, wekaContainer)
				if err != nil {
					logger.Error(err, "Failed to create WekaContainer")
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
		logger.Error(err, "Failed to ensure drive containers")
		return nil, err
	}
	if err := ensureContainers("compute", template.ComputeContainers); err != nil {
		logger.Error(err, "Failed to ensure compute containers")
		return nil, err
	}
	return foundContainers, nil
}

func (r *WekaClusterReconciler) GetOrInitAllocMap(ctx context.Context) (*Allocations, *v1.ConfigMap, error) {
	logger := r.Logger.WithName("GetOrInitAllocMap")
	// fetch alloc map from configmap
	allocations := &Allocations{
		NodeMap: AllocationsMap{},
	}
	allocMap := allocations.NodeMap
	yamlData, err := yaml.Marshal(&allocMap)
	if err != nil {
		logger.Error(err, "Failed to marshal alloc map")
		return nil, nil, err
	}

	allocMapConfigMap := &v1.ConfigMap{}
	podNamespace, err := util.GetPodNamespace()
	if err != nil {
		logger.Error(err, "Failed to get pod namespace")
		return nil, nil, err
	}
	key := client.ObjectKey{Namespace: podNamespace, Name: "weka-operator-allocmap"}
	err = r.Get(ctx, key, allocMapConfigMap)
	if err != nil && apierrors.IsNotFound(err) {
		// Define a new ConfigMap
		allocMapConfigMap = &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "weka-operator-allocmap",
				Namespace: podNamespace,
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
		err = yaml.Unmarshal([]byte(allocMapConfigMap.Data["allocmap.yaml"]), &allocations)
		if err != nil {
			return nil, nil, err
		}
	}
	return allocations, allocMapConfigMap, nil
}

func (r *WekaClusterReconciler) newWekaContainerForWekaCluster(cluster *wekav1alpha1.WekaCluster,
	ownedResources OwnedResources,
	template ClusterTemplate,
	topology Topology,
	role string, i int,
) (*wekav1alpha1.WekaContainer, error) {
	labels := map[string]string{
		"app": cluster.Name,
	}

	var hugePagesNum int
	var appendSetupCommand string
	if role == "drive" {
		hugePagesNum = template.DriveHugepages
		appendSetupCommand = cluster.Spec.DriveAppendSetupCommand
	} else {
		hugePagesNum = template.ComputeHugepages
		appendSetupCommand = cluster.Spec.ComputeAppendSetupCommand
	}

	network, err := resources.GetContainerNetwork(topology.Network)
	if err != nil {
		return nil, err
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

	secretKey := domain.GetOperatorSecretName(cluster)
	containerPrefix := domain.GetLastGuidPart(cluster)

	coreIds := ownedResources.CoreIds
	if slices.Contains([]wekav1alpha1.CpuPolicy{wekav1alpha1.CpuPolicyManual, wekav1alpha1.CpuPolicyShared}, cluster.Spec.CpuPolicy) {
		coreIds = []int{}
	} // TODO: Should not calculate CPU in topology if set to manual/shared mode, right now removing what it did set
	// This way we still track cores and can block on topology level, for good and bad.
	// TODO: What happens if cores are rotating? How we adjust weka to use new cores? Should we block on that?
	// TODO: We should not start container until we ensure
	// TODO: Basically, start-weka-container should wait for agent to start, and start-weka-container will actually start container after it will do changes if needed
	container := &wekav1alpha1.WekaContainer{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "weka.weka.io/v1alpha1",
			Kind:       "WekaContainer",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      resources.GetContainerName(cluster, role, i),
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: wekav1alpha1.WekaContainerSpec{
			NodeAffinity:       ownedResources.Node,
			Port:               ownedResources.Port,
			AgentPort:          ownedResources.AgentPort,
			Image:              cluster.Spec.Image,
			ImagePullSecret:    cluster.Spec.ImagePullSecret,
			WekaContainerName:  fmt.Sprintf("%s%ss%d", containerPrefix, role, i),
			Mode:               role,
			NumCores:           len(ownedResources.CoreIds),
			CoreIds:            coreIds,
			Network:            network,
			Hugepages:          hugePagesNum,
			HugepagesSize:      template.HugePageSize,
			HugepagesOverride:  template.HugePagesOverride,
			NumDrives:          len(ownedResources.Drives),
			PotentialDrives:    potentialDrives,
			WekaSecretRef:      v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{Key: secretKey}},
			DriversDistService: cluster.Spec.DriversDistService,
			CpuPolicy:          cluster.Spec.CpuPolicy,
			AppendSetupCommand: appendSetupCommand,
		},
	}

	if err := ctrl.SetControllerReference(cluster, container, r.Scheme); err != nil {
		return nil, err
	}

	return container, nil
}

func (r *WekaClusterReconciler) CreateCluster(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error {
	logger := r.Logger.WithName("CreateCluster")
	if len(containers) == 0 {
		logger.Info("containers list is empty")
		return pretty.Errorf("containers list is empty")
	}

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

	// update cluster name
	clusterName := cluster.GetUID()
	cmd = fmt.Sprintf("weka cluster update --cluster-name %s", clusterName)
	_, stderr, err = executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to update cluster name: %s", stderr.String())
	}

	if err := r.Status().Update(ctx, cluster); err != nil {
		return errors.Wrap(err, "Failed to update wekaCluster status")
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

func (r *WekaClusterReconciler) EnsureClusterContainerIds(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error {
	logger := r.Logger.WithName("EnsureClusterContainerIds")
	var containersMap resources.ClusterContainersMap

	fetchContainers := func() error {
		pod, err := resources.NewContainerFactory(containers[0], r.Logger).Create()
		if err != nil {
			logger.Error(err, "Could not find executor pod")
			return err
		}
		clusterizePod := &v1.Pod{}
		err = r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: pod.Name}, clusterizePod)
		if err != nil {
			logger.Error(err, "Could not find clusterize pod")
			return err
		}
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

func (r *WekaClusterReconciler) StartIo(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error {
	logger := r.Logger.WithName("StartIo")

	if len(containers) == 0 {
		err := pretty.Errorf("containers list is empty")
		logger.Error(err, "containers list is empty")
		return err
	}

	executor, err := GetExecutor(containers[0], r.Logger)
	if err != nil {
		return errors.Wrap(err, "Error creating executor")
	}

	cmd := "weka cluster start-io"
	_, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to start-io: %s", stderr.String())
	}

	return nil
}

func (r *WekaClusterReconciler) isContainersReady(containers []*wekav1alpha1.WekaContainer) (bool, error) {
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

func (r *WekaClusterReconciler) UpdateAllocationsConfigmap(ctx context.Context, allocations *Allocations, configMap *v1.ConfigMap) error {
	yamlData, err := yaml.Marshal(&allocations)
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

type loginDetails struct {
	Username   string
	Password   string
	Org        string
	SecretName string
}

func (r *WekaClusterReconciler) ensureLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error {
	secret := &v1.Secret{}

	// generate random password

	const DefaultOrg = "Root"

	operatorLogin := loginDetails{
		Username:   domain.GetOperatorClusterUsername(cluster),
		Password:   util.GeneratePassword(32),
		Org:        DefaultOrg,
		SecretName: domain.GetOperatorSecretName(cluster),
	}

	userLogin := loginDetails{
		Username:   domain.GetUserClusterUsername(cluster),
		Password:   util.GeneratePassword(32),
		Org:        DefaultOrg,
		SecretName: resources.GetUserSecretName(cluster),
	}

	ensureSecret := func(details loginDetails) error {
		err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: details.SecretName}, secret)
		if err != nil && apierrors.IsNotFound(err) {
			secret = &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      details.SecretName,
					Namespace: cluster.Namespace,
				},
				StringData: map[string]string{
					"username": details.Username,
					"password": details.Password,
					"org":      details.Org,
				},
			}

			err := ctrl.SetControllerReference(cluster, secret, r.Scheme)
			if err != nil {
				return err
			}

			err = r.Create(ctx, secret)
			if err != nil {
				return err
			}
		}

		return nil
	}

	if err := ensureSecret(operatorLogin); err != nil {
		return err
	}
	if err := ensureSecret(userLogin); err != nil {
		return err
	}
	return nil
}

func (r *WekaClusterReconciler) applyClusterCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error {
	executor, err := GetExecutor(containers[0], r.Logger)
	if err != nil {
		return errors.Wrap(err, "Error creating executor")
	}

	existingUsers := []resources.WekaUsersResponse{}
	cmd := "weka user -J || wekaauthcli user -J"
	stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		return err
	}
	err = json.Unmarshal(stdout.Bytes(), &existingUsers)
	if err != nil {
		return err
	}

	ensureUser := func(secretName string) error {
		// fetch secret from k8s
		secret := &v1.Secret{}
		err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: secretName}, secret)
		if err != nil {
			return err
		}
		username := secret.Data["username"]
		password := secret.Data["password"]
		for _, user := range existingUsers {
			if user.Username == string(username) {
				return nil
			}
		}
		// TODO: This still exposes password via Exec, solution might be to mount both secrets and create by script
		cmd := fmt.Sprintf("weka user add %s ClusterAdmin %s", username, password)
		_, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
		if err != nil {
			return errors.Wrapf(err, "Failed to add user: %s", stderr.String())
		}

		return nil
	}

	if err := ensureUser(domain.GetOperatorSecretName(cluster)); err != nil {
		return err
	}
	if err := ensureUser(resources.GetUserSecretName(cluster)); err != nil {
		return err
	}

	for _, user := range existingUsers {
		if user.Username == "admin" {
			cmd = "wekaauthcli user delete admin"
			_, stderr, err = executor.Exec(ctx, []string{"bash", "-ce", cmd})
			if err != nil {
				return errors.Wrapf(err, "Failed to delete default admin user: %s", stderr.String())
			}
			return nil
		}
	}
	return nil
}
