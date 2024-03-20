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
	"fmt"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/util"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
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

//+kubebuilder:rbac:groups=weka.weka.io,resources=dummyclusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=dummyclusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=dummyclusters/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the DummyCluster object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.3/pkg/reconcile
func (r *DummyClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// TODO(user): your logic here
	dummyCluster := &wekav1alpha1.DummyCluster{}
	err := r.Get(ctx, req.NamespacedName, dummyCluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// If the custom resource is not found then, it usually means that it was deleted or not created
			// In this way, we will stop the reconciliation
			log.Info("dummyCluster resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get dummyCluster")
		return ctrl.Result{}, err
	}

	if dummyCluster.Status.Status == "" {
		dummyCluster.Status.Status = "Unknown"
		if err := r.Status().Update(ctx, dummyCluster); err != nil {
			log.Error(err, "Failed to update dummyCluster direct status field")
			return ctrl.Result{}, err
		}
	}

	if dummyCluster.Status.Conditions == nil || len(dummyCluster.Status.Conditions) == 0 {
		meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{Type: "Unknown", Status: metav1.ConditionUnknown, Reason: "Reconciling", Message: "Starting reconciliation"})
		if err = r.Status().Update(ctx, dummyCluster); err != nil {
			log.Error(err, "Failed to update dummyCluster status as conditions")
			return ctrl.Result{}, err
		}

		// data refresh (user updates? other processes can update?
		if err := r.Get(ctx, req.NamespacedName, dummyCluster); err != nil {
			log.Error(err, "Failed to re-fetch dummyCluster")
			return ctrl.Result{}, err
		}
	}

	if !controllerutil.ContainsFinalizer(dummyCluster, wekaContainerFinalizer) {
		log.Info("Adding Finalizer for wekaContaner")
		if ok := controllerutil.AddFinalizer(dummyCluster, wekaContainerFinalizer); !ok {
			log.Error(err, "Failed to add finalizer into the custom resource")
			return ctrl.Result{Requeue: true}, nil
		}

		if err = r.Update(ctx, dummyCluster); err != nil {
			log.Error(err, "Failed to update custom resource to add finalizer")
			return ctrl.Result{}, err
		}
	}

	// Check if the dummyCluster instance is marked to be deleted, which is
	// indicated by the deletion timestamp being set.
	isdummyClusterMarkedToBeDeleted := dummyCluster.GetDeletionTimestamp() != nil
	if isdummyClusterMarkedToBeDeleted {
		if controllerutil.ContainsFinalizer(dummyCluster, wekaContainerFinalizer) {
			log.Info("Performing Finalizer Operations for dummyCluster before delete CR")

			// Let's add here an status "Downgrade" to define that this resource begin its process to be terminated.
			meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{Type: "finalizing",
				Status: metav1.ConditionUnknown, Reason: "Finalizing",
				Message: fmt.Sprintf("Performing finalizer operations for the custom resource: %s ", dummyCluster.Name)})

			if err := r.Status().Update(ctx, dummyCluster); err != nil {
				log.Error(err, "Failed to update dummyCluster status")
				return ctrl.Result{}, err
			}

			// Perform all operations required before remove the finalizer and allow
			// the Kubernetes API to remove the custom resource.
			r.doFinalizerOperationsFordummyCluster(dummyCluster)

			// TODO(user): If you add operations to the doFinalizerOperationsFordummyCluster method
			// then you need to ensure that all worked fine before deleting and updating the Downgrade status
			// otherwise, you should requeue here.

			// Re-fetch the dummyCluster Custom Resource before update the status
			// so that we have the latest state of the resource on the cluster and we will avoid
			// raise the issue "the object has been modified, please apply
			// your changes to the latest version and try again" which would re-trigger the reconciliation
			if err := r.Get(ctx, req.NamespacedName, dummyCluster); err != nil {
				log.Error(err, "Failed to re-fetch dummyCluster")
				return ctrl.Result{}, err
			}

			meta.SetStatusCondition(&dummyCluster.Status.Conditions, metav1.Condition{Type: "finalizing",
				Status: metav1.ConditionTrue, Reason: "Finalizing",
				Message: fmt.Sprintf("Finalizer operations for custom resource %s name were successfully accomplished", dummyCluster.Name)})

			if err := r.Status().Update(ctx, dummyCluster); err != nil {
				log.Error(err, "Failed to update dummyCluster status")
				return ctrl.Result{}, err
			}

			log.Info("Removing Finalizer for dummyCluster after successfully perform the operations")
			if ok := controllerutil.RemoveFinalizer(dummyCluster, wekaContainerFinalizer); !ok {
				log.Error(err, "Failed to remove finalizer for dummyCluster")
				return ctrl.Result{Requeue: true}, nil
			}

			if err := r.Update(ctx, dummyCluster); err != nil {
				log.Error(err, "Failed to remove finalizer for dummyCluster")
				return ctrl.Result{}, err
			}

		}
		return ctrl.Result{}, nil
	}

	containers, err := r.ensureWekaContainers(ctx, dummyCluster)
	if err != nil {
		log.Error(err, "Failed to ensure WekaContainers")
		return ctrl.Result{}, err

	}

	err = r.CreateCluster(ctx, dummyCluster, containers)
	if err != nil {
		return ctrl.Result{}, err
	}

	// TODO: This probably a place to start running with conditions, or validate against cluster drive by guids, what was already running
	// Current state of function definitely is not anywhere close to reliable, and mostly done for happy flows quick deployments
	// Any failure within AddDrives probably will lead to a bad state
	err = r.AddDrives(ctx, dummyCluster, containers)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
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

func (r *DummyClusterReconciler) ensureWekaContainers(ctx context.Context, cluster *wekav1alpha1.DummyCluster) ([]*wekav1alpha1.WekaContainer, error) {
	//iterate over cluster.Spec.Size, search for WekaContainer object, and create if not found, populating node affinity from spec Hosts list mapping to index
	agentPort := cluster.Spec.AgentBasePort
	containerPort := cluster.Spec.ContainerBasePort

	roles := []string{"compute", "drive"}
	ipPortPairs := []string{}

	HostNameToIp := map[string]string{
		"wekabox17.lan": "10.222.98.0",
		"wekabox15.lan": "10.222.98.1",
		"wekabox14.lan": "10.222.98.2",
		"wekabox16.lan": "10.222.98.3",
		"wekabox18.lan": "10.222.98.4",
	}

	getIpByHostname := func(hostname string) string {
		return HostNameToIp[hostname]
	}

	foundContainers := []*wekav1alpha1.WekaContainer{}

	for i := 0; i < cluster.Spec.Size; i++ {
		// Check if the WekaContainer object exists
		core := cluster.Spec.BaseCoreId
		for _, role := range roles {
			wekaContainer, err := r.newWekaContainerForDummyCluster(cluster, i, role, containerPort, agentPort, core)
			if err != nil {
				return nil, err
			}
			core += cluster.Spec.CoreStep

			ipPortPairs = append(ipPortPairs, fmt.Sprintf("%s:%d", getIpByHostname(cluster.Spec.Hosts[i]), containerPort))
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
	spaceSeparatedHostnames := strings.Join(hostList, " ")
	commaSeparatedIPPort := strings.Join(ipPortPairs, ",")

	clusterCreateCommand := fmt.Sprintf("weka cluster create %s --host-ips %s",
		spaceSeparatedHostnames,
		commaSeparatedIPPort,
	)

	//addDrivesCommand := fmt.Sprintf("weka drive add %d --host-ip %s")
	// iterate over hosts and build string containing add driver per host
	resignDrives := ""
	addDrives := ""
	for i, _ := range cluster.Spec.Hosts {
		containerNum := i*2 + 1
		sgDiskPart := fmt.Sprintf("sgdisk -Z %s\n", cluster.Spec.Drive)
		reSignPart := fmt.Sprintf("weka local exec --container drive%d /weka/tools/weka_sign_drive %s\n", i, cluster.Spec.Drive)
		addDrives += fmt.Sprintf("weka drive add %d %s\n", containerNum, cluster.Spec.Drive)
		resignDrives += sgDiskPart + reSignPart
	}
	// create containers
	// bash helper to erase disks: seq 0 4 | xargs -n1 -P0 -INN kubectl exec clustera-drive-NN -- weka local exec -- sgdisk -Z /dev/sdd
	// bash helper to sign disks: seq 0 4 | xargs -n1 -P0 -INN kubectl exec clustera-drive-NN -- weka local exec -- /weka/tools/weka_sign_drive /dev/sdd
	// create cluster
	// bash helper to add disks: echo {1,3,5,7,9} | xargs -n1 -P5 -INN kubectl exec clustera-drive-0 -- weka cluster drive add NN /dev/sdd
	// start-io

	startIo := "weka cluster start-io"

	r.Recorder.Event(cluster, "Normal", "Debug", fmt.Sprintf("resign drives: \n%s, run in corresponding containers. \n to clusterize run in any container(assuming disks are erased and signed for weka at this point:\n%s\n%s\n%s", resignDrives, clusterCreateCommand, addDrives, startIo))
	return foundContainers, nil
}

func (r *DummyClusterReconciler) newWekaContainerForDummyCluster(cluster *wekav1alpha1.DummyCluster, i int, role string, port, agentPort, core int) (*wekav1alpha1.WekaContainer, error) {
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
	for _, container := range containers {
		if container.GetDeletionTimestamp() != nil {
			return errors.New("Container " + container.Name + " is being deleted, rejecting cluster create")
		}
		if container.Status.ManagementIP == "" {
			return errors.New("ManagementIP is not set for container " + container.Name)
		}
		hostIps = append(hostIps, fmt.Sprintf("%s:%d", container.Status.ManagementIP, container.Spec.Port))
		hostnamesList = append(hostnamesList, container.Status.ManagementIP)
	}
	hostIpsStr := strings.Join(hostIps, ",")
	cmd := fmt.Sprintf("weka cluster create %s --host-ips %s", strings.Join(hostnamesList, " "), hostIpsStr)

	if cluster.Status.Status != "Unknown" {
		return nil // TODO: no yet some "creating" status, so "Unknown"
	}
	r.Logger.Info("Creating cluster", "cmd", cmd)

	//TODO: Utility func on cluster to get exec command on arbitrary container
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
	stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to create cluster: %s", stderr.String())
	}
	r.Logger.Info("Cluster created", "stdout", stdout.String(), "stderr", stderr.String())

	cluster.Status.Status = "Configuring" //TODO: Conditions apparently are mechanism to control state machine and we should adopt instead of this
	if err := r.Status().Update(ctx, cluster); err != nil {
		return errors.Wrap(err, "Failed to update dummyCluster status")
	}

	return nil
}

func (r *DummyClusterReconciler) AddDrives(ctx context.Context, cluster *wekav1alpha1.DummyCluster, containers []*wekav1alpha1.WekaContainer) error {
	if cluster.Status.Status != "Configuring" {
		return nil
	}
	//return errors.New("not implemented")
	return nil
}
