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
	"github.com/kr/pretty"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/util"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"gopkg.in/yaml.v3"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"slices"
	"strings"
	"time"
)

const WekaFinalizer = "weka.weka.io/finalizer"
const ClusterStatusInit = "Init"

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

func (r *WekaClusterReconciler) getLogSpan(ctx context.Context, names ...string) (context.Context, instrumentation.LogSpan) {
	logger := r.Logger
	joinNames := strings.Join(names, ".")
	ctx, span := instrumentation.Tracer.Start(ctx, joinNames)
	if span != nil {
		traceID := span.SpanContext().TraceID().String()
		spanID := span.SpanContext().SpanID().String()
		logger = logger.WithValues("trace_id", traceID, "span_id", spanID)
		for _, name := range names {
			logger = logger.WithName(name)
		}
	}

	ShutdownFunc := func(opts ...trace.SpanEndOption) {
		if span != nil {
			span.End(opts...)
		}
		logger.V(4).Info(fmt.Sprintf("%s finished", joinNames))
	}

	ls := instrumentation.LogSpan{
		Logger: logger,
		Span:   span,
		End:    ShutdownFunc,
	}
	logger.V(4).Info(fmt.Sprintf("%s called", joinNames))
	return ctx, ls
}

func (r *WekaClusterReconciler) SetCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster,
	condType string, status metav1.ConditionStatus, reason string, message string) error {
	ctx, logger := r.getLogSpan(ctx, fmt.Sprintf("SetCondition.%s", condType))
	defer logger.End()

	logger.WithValues(
		"cluster_name", cluster.Name,
		"cluster_uid", string(cluster.GetUID()),
		"condition_type", condType,
		"condition_status", string(status),
	).Info("Setting condition")

	condRecord := metav1.Condition{
		Type:    condType,
		Status:  status,
		Reason:  reason,
		Message: message,
	}
	for i := 0; i < 3; i++ {
		meta.SetStatusCondition(&cluster.Status.Conditions, condRecord)
		err := r.Status().Update(ctx, cluster)
		if err != nil {
			logger.Debug("Failed to update wekaCluster status", "err", err)
			if i == 2 {
				logger.Error(err, "Failed to update wekaCluster status after 3 retries")
				return errors.Wrap(err, "Failed to update wekaCluster status")
			}
			// need to re-fetch the cluster since we have a stale version of object
			err = r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, cluster)
			if err != nil {
				if apierrors.IsNotFound(err) {
					logger.Error(err, "wekaCluster resource not found although expected")
				}
				// Error reading the object - requeue the request.
				logger.Error(err, "Failed to fetch new version of object")
			}
			continue
		}
		logger.SetStatus(codes.Ok, "Condition set")
		break
	}
	return nil
}

func (r *WekaClusterReconciler) Reconcile(initContext context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger := r.getLogSpan(initContext, "WekaClusterReconcile")
	logger = logger.WithValues("namespace", req.Name, "name", req.Name)

	// Fetch the WekaCluster instance
	ctx, wekaCluster, err := r.GetClusterAndContext(initContext, req)

	if err != nil {
		logger.Error(err, "Failed to get wekaCluster")
		logger.End()
		return ctrl.Result{}, err
	}
	if wekaCluster == nil {
		logger.Error(errors.New("WekaCluster not found"), "Existing WekaCluster not found")
		logger.End()
		return ctrl.Result{}, nil
	}
	logger.End()
	ctx, logger = r.getLogSpan(ctx, "WekaClusterReconcile")
	logger = logger.WithValues("namespace", wekaCluster.Namespace, "name", wekaCluster.Name)
	defer logger.End()

	logger.WithValues("cluster_status", wekaCluster.Status.Status).Info("Reconciling WekaCluster")

	logger.SetPhase("CLUSTER_RECONCILE_STARTED")

	err = r.initState(ctx, wekaCluster)
	if err != nil {
		logger.Error(err, "Failed to initialize state")
		return ctrl.Result{}, err
	}
	logger.SetPhase("CLUSTER_RECONCILE_INITIALIZED")

	if wekaCluster.GetDeletionTimestamp() != nil {
		err = r.handleDeletion(ctx, wekaCluster)
		if err != nil {
			logger.Error(err, "Failed to handle deletion")
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, err
		}
		logger.SetPhase("CLUSTER_IS_BEING_DELETED")
		return ctrl.Result{}, nil
	}

	// generate login credentials
	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondClusterSecretsCreated) {
		err = r.ensureLoginCredentials(ctx, wekaCluster)
		if err != nil {
			return ctrl.Result{}, err
		}

		_ = r.SetCondition(ctx, wekaCluster, condition.CondClusterSecretsCreated, metav1.ConditionTrue, "Init", "Cluster secrets are created")
	} else {
		logger.SetPhase("CLUSTER_SECRETS_ALREADY_CREATED")
	}

	// Note: All use of conditions is only as hints for skipping actions and a visibility, not strictly a state machine
	// All code should be idempotent and not rely on conditions for correctness, hence validation of succesful update of conditions is not done
	logger.SetPhase("ENSURING_CLUSTER_CONTAINERS")
	containers, err := r.ensureWekaContainers(ctx, wekaCluster)
	if err != nil {
		_ = r.SetCondition(ctx, wekaCluster, condition.CondPodsCreated, metav1.ConditionFalse, "Error", err.Error())
		logger.Error(err, "Failed to ensure WekaContainers")
		return ctrl.Result{RequeueAfter: time.Second * 3}, err
	}

	_ = r.SetCondition(ctx, wekaCluster, condition.CondPodsCreated, metav1.ConditionTrue, "Init", "All pods are created")

	logger.SetPhase("PODS_ALREADY_EXIST")

	if meta.IsStatusConditionFalse(wekaCluster.Status.Conditions, condition.CondPodsReady) {
		logger.Debug("Checking if all containers are ready")
		if ready, err := r.isContainersReady(ctx, containers); !ready {
			logger.SetPhase("CONTAINERS_NOT_READY")
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second}, err
		}
		logger.SetPhase("CONTAINERS_ARE_READY")
		_ = r.SetCondition(ctx, wekaCluster, condition.CondPodsReady, metav1.ConditionTrue, "Init", "All weka containers are ready for clusterization")
	} else {
		logger.SetPhase("CONTAINERS_ARE_ALREADY_READY")
	}

	if meta.IsStatusConditionFalse(wekaCluster.Status.Conditions, condition.CondClusterCreated) {
		logger.SetPhase("CLUSTERIZING")
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
		_ = r.SetCondition(ctx, wekaCluster, condition.CondClusterCreated, metav1.ConditionTrue, "Init", "Cluster is formed")
		logger.SetPhase("CLUSTER_FORMED")
	} else {
		logger.SetPhase("CLUSTER_ALREADY_FORMED")
	}

	// Ensure all containers are up in the cluster
	logger.Debug("Ensuring all containers are up in the cluster")
	joinedContainers := 0
	for _, container := range containers {
		if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondJoinedCluster) {
			logger.Info("Container has not joined the cluster yet", "container", container.Name)
			logger.SetPhase("CONTAINERS_NOT_JOINED_CLUSTER")
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, nil
		} else {
			if wekaCluster.Status.ClusterID == "" {
				wekaCluster.Status.ClusterID = container.Status.ClusterID
				err := r.Status().Update(ctx, wekaCluster)
				if err != nil {
					return ctrl.Result{}, err
				}
				logger.WithValues("container_name", container.Name).InfoWithStatus(codes.Ok, "Container joined cluster successfully")
			}
			joinedContainers++
		}
	}
	if joinedContainers == len(containers) {
		logger.SetPhase("ALL_CONTAINERS_ALREADY_JOINED")
	} else {
		logger.SetPhase("CONTAINERS_JOINED_CLUSTER")
	}

	err = r.EnsureClusterContainerIds(ctx, wekaCluster, containers)
	if err != nil {
		logger.Info("not all containers are up in the cluster", "err", err)
		return ctrl.Result{Requeue: true, RequeueAfter: time.Second}, nil
	}

	// Ensure all containers are up in the cluster
	logger.Debug("Ensuring all drives are up in the cluster")
	for _, container := range containers {
		if container.Spec.Mode != "drive" {
			continue
		}
		if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondDrivesAdded) {
			logger.Info("Containers did not add drives yet", "container", container.Name)
			logger.InfoWithStatus(codes.Unset, "Containers did not add drives yet")
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, nil
		}
	}
	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondDrivesAdded) {
		err := r.SetCondition(ctx, wekaCluster, condition.CondDrivesAdded, metav1.ConditionTrue, "Init", "All drives are added")
		if err != nil {
			return ctrl.Result{}, err
		}
		logger.SetPhase("ALL_DRIVES_ADDED")
	}

	logger.Info("Ensuring IO is started")
	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondIoStarted) {
		_ = r.SetCondition(ctx, wekaCluster, condition.CondIoStarted, metav1.ConditionUnknown, "Init", "Starting IO")
		logger.Info("Starting IO")
		err = r.StartIo(ctx, wekaCluster, containers)
		if err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("IO Started, time since create:" + time.Since(wekaCluster.CreationTimestamp.Time).String())
		_ = r.SetCondition(ctx, wekaCluster, condition.CondIoStarted, metav1.ConditionTrue, "Init", "IO is started")
		logger.SetPhase("IO_IS_STARTED")
	}

	logger.SetPhase("CONFIGURING_CLUSTER_CREDENTIALS")
	if !meta.IsStatusConditionTrue(wekaCluster.Status.Conditions, condition.CondClusterSecretsApplied) {
		err = r.applyClusterCredentials(ctx, wekaCluster, containers)
		if err != nil {
			return ctrl.Result{}, err
		}
		_ = r.SetCondition(ctx, wekaCluster, condition.CondClusterSecretsApplied, metav1.ConditionTrue, "Init", "Applied cluster secrets")
		wekaCluster.Status.Status = "Ready"
		err = r.Status().Update(ctx, wekaCluster)
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	logger.SetPhase("CLUSTER_READY")
	return ctrl.Result{}, nil
}

func (r *WekaClusterReconciler) handleDeletion(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error {
	ctx, logger := r.getLogSpan(ctx, "handleDeletion")
	logger = logger.WithValues("namespace", wekaCluster.Namespace, "name", wekaCluster.Name)
	defer logger.End()

	if controllerutil.ContainsFinalizer(wekaCluster, WekaFinalizer) {
		logger.Info("Performing Finalizer Operations for wekaCluster before delete CR")

		// Perform all operations required before remove the finalizer and allow
		// the Kubernetes API to remove the custom resource.
		err := r.doFinalizerOperationsForwekaCluster(ctx, wekaCluster)
		if err != nil {
			return err
		}

		logger.Info("Removing Finalizer for wekaCluster after successfully perform the operations")
		if ok := controllerutil.RemoveFinalizer(wekaCluster, WekaFinalizer); !ok {
			err := errors.New("Failed to remove finalizer for wekaCluster")
			return err
		}

		if err := r.Update(ctx, wekaCluster); err != nil {
			logger.Error(err, "Failed to remove finalizer for wekaCluster")
			return err
		}

	}
	return nil
}

func (r *WekaClusterReconciler) initState(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error {
	ctx, logger := r.getLogSpan(ctx, "initState")
	logger = logger.WithValues("namespace", wekaCluster.Namespace, "name", wekaCluster.Name)
	defer logger.End()
	if !controllerutil.ContainsFinalizer(wekaCluster, WekaFinalizer) {

		wekaCluster.Status.InitStatus()

		err := r.Status().Update(ctx, wekaCluster)
		if err != nil {
			logger.Error(err, "failed to init states")
		}

		logger.Info("Adding Finalizer for weka cluster")
		if ok := controllerutil.AddFinalizer(wekaCluster, WekaFinalizer); !ok {
			logger.Info("Failed to add finalizer for wekaCluster")
			return errors.New("Failed to add finalizer for wekaCluster")
		}

		if err := r.Update(ctx, wekaCluster); err != nil {
			logger.Error(err, "Failed to update custom resource to add finalizer")
			return err
		}

		if err := r.Get(ctx, client.ObjectKey{Namespace: wekaCluster.Namespace, Name: wekaCluster.Name}, wekaCluster); err != nil {
			logger.Error(err, "Failed to re-fetch data")
			return err
		}
		logger.Info("Finalizer added for wekaCluster", "conditions", len(wekaCluster.Status.Conditions))
	}
	return nil
}

func (r *WekaClusterReconciler) GetClusterAndContext(initContext context.Context, req ctrl.Request) (context.Context, *wekav1alpha1.WekaCluster, error) {
	initContext, logger := r.getLogSpan(initContext, "GetClusterAndContext")
	defer logger.End()

	wekaCluster := &wekav1alpha1.WekaCluster{}
	err := r.Get(initContext, req.NamespacedName, wekaCluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("wekaCluster resource not found. Ignoring since object must be deleted")
			return initContext, nil, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get wekaCluster")
		return initContext, nil, err
	}

	if wekaCluster.Status.Status == ClusterStatusInit && wekaCluster.Status.TraceId == "" {
		span := trace.SpanFromContext(initContext)
		wekaCluster.Status.TraceId = span.SpanContext().TraceID().String()
		wekaCluster.Status.SpanID = span.SpanContext().SpanID().String()
		err := r.Status().Update(initContext, wekaCluster)
		if err != nil {
			logger.Error(err, "Failed to update traceId")
			return initContext, nil, err
		}
	}

	retCtx := instrumentation.NewContextWithTraceID(initContext, nil, wekaCluster.Status.TraceId, wekaCluster.Status.SpanID)

	return retCtx, wekaCluster, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WekaClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.WekaCluster{}).
		Owns(&wekav1alpha1.WekaContainer{}).
		Complete(r)
}

func (r *WekaClusterReconciler) doFinalizerOperationsForwekaCluster(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error {
	ctx, logger := r.getLogSpan(ctx, "doFinalizerOperationsForwekaCluster")
	logger = logger.WithValues("namespace", cluster.Namespace, "name", cluster.Name)
	defer logger.End()
	if cluster.Spec.Topology == "" {
		return nil
	}
	topology, err := Topologies[cluster.Spec.Topology](ctx, r, cluster.Spec.NodeSelector)
	if err != nil {
		return err
	}
	allocator := NewAllocator(logger, topology)
	allocations, allocConfigMap, err := r.GetOrInitAllocMap(ctx)
	if err != nil {
		logger.Error(err, "Failed to get alloc map")
		return err
	}

	changed := allocator.DeallocateCluster(OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace}, allocations)
	if changed {
		if err := r.UpdateAllocationsConfigmap(ctx, allocations, allocConfigMap); err != nil {
			logger.Error(err, "Failed to update alloc map")
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
	ctx, logger := r.getLogSpan(ctx, "ensureWekaContainers")
	logger = logger.WithValues("namespace", cluster.Namespace, "name", cluster.Name)
	defer logger.End()
	allocations, allocConfigMap, err := r.GetOrInitAllocMap(ctx)
	if err != nil {
		logger.Error(err, "could not init allocmap")
		return nil, err
	}

	foundContainers := []*wekav1alpha1.WekaContainer{}
	template := WekaClusterTemplates[cluster.Spec.Template]
	topology, err := Topologies[cluster.Spec.Topology](ctx, r, cluster.Spec.NodeSelector)
	allocator := NewAllocator(logger, topology)
	if err != nil {
		logger.Error(err, "Failed to get topology", "topology", cluster.Spec.Topology)
		return nil, err
	}
	allocations, err, changed := allocator.Allocate(
		ctx,
		OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace},
		template,
		allocations,
		cluster.Spec.Size)
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
	logger.InfoWithStatus(codes.Unset, "Ensuring containers")

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
			l := logger.WithValues("container_name", wekaContainer.Name)

			found := &wekav1alpha1.WekaContainer{}
			err = r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: wekaContainer.Name}, found)
			if err != nil && apierrors.IsNotFound(err) {
				// Define a new WekaContainer object
				l.Info("Creating container")
				err = r.Create(ctx, wekaContainer)
				if err != nil {
					logger.Error(err, "Failed to create WekaContainer")
					return err
				}
				foundContainers = append(foundContainers, wekaContainer)
				l.Info("Container created")
			} else {
				foundContainers = append(foundContainers, found)
				l.Info("Container already exists")
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
	logger.InfoWithStatus(codes.Ok, "All cluster containers are created")
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
	ctx, logger := r.getLogSpan(ctx, "CreateCluster")
	logger = logger.WithValues("namespace", cluster.Namespace, "name", cluster.Name)
	defer logger.End()

	if len(containers) == 0 {
		logger.Info("containers list is empty")
		return pretty.Errorf("containers list is empty")
	}

	var hostIps []string
	var hostnamesList []string
	logger.Info("Creating cluster", trace.WithAttributes(
		attribute.Int("total_containers", len(containers)),
		attribute.String("cluster_uid", string(cluster.GetUID())),
	))

	for _, container := range containers {
		hostIps = append(hostIps, fmt.Sprintf("%s:%d", container.Status.ManagementIP, container.Spec.Port))
		hostnamesList = append(hostnamesList, container.Status.ManagementIP)
	}
	hostIpsStr := strings.Join(hostIps, ",")
	cmd := fmt.Sprintf("weka status || weka cluster create %s --host-ips %s", strings.Join(hostnamesList, " "), hostIpsStr)
	logger.Info("Creating cluster", "cmd", cmd)

	executor, err := GetExecutor(containers[0], logger)
	if err != nil {
		logger.Error(err, "Could not create executor")
		return errors.Wrap(err, "Could not create executor")
	}
	stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		logger.Error(err, "Failed to create cluster")
		return errors.Wrapf(err, "Failed to create cluster: %s", stderr.String())
	}
	logger.Info("Cluster created", trace.WithAttributes(attribute.String("stdout", stdout.String()), attribute.String("stderr", stderr.String())))
	logger.Info("Cluster created", "stdout", stdout.String(), "stderr", stderr.String())

	// update cluster name
	clusterName := cluster.GetUID()
	cmd = fmt.Sprintf("weka cluster update --cluster-name %s", clusterName)
	logger.Debug("Updating cluster name")
	_, stderr, err = executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to update cluster name: %s", stderr.String())
	}

	if err := r.Status().Update(ctx, cluster); err != nil {
		return errors.Wrap(err, "Failed to update wekaCluster status")
	}
	logger.SetPhase("Cluster created")
	return nil
}

func GetExecutor(container *wekav1alpha1.WekaContainer, logger instrumentation.LogSpan) (*util.Exec, error) {
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
	ctx, logger := r.getLogSpan(ctx, "EnsureClusterContainerIds")
	defer logger.End()
	var containersMap resources.ClusterContainersMap

	fetchContainers := func() error {
		pod, err := resources.NewContainerFactory(containers[0], logger).Create()
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
					logger.Error(err, "Failed to fetch containers list from cluster")
					return err
				}
			}

			if clusterContainer, ok := containersMap[container.Spec.WekaContainerName]; !ok {
				err := errors.New("Container " + container.Spec.WekaContainerName + " not found in cluster")
				logger.Error(err, "Container not found in cluster", "container_name", container.Name)
				return err
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
	logger.InfoWithStatus(codes.Ok, "Cluster container ids are set")
	return nil
}

func (r *WekaClusterReconciler) StartIo(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error {
	ctx, logger := r.getLogSpan(ctx, "StartIo")
	defer logger.End()

	if len(containers) == 0 {
		err := pretty.Errorf("containers list is empty")
		logger.Error(err, "containers list is empty")
		return err
	}

	executor, err := GetExecutor(containers[0], logger)
	if err != nil {
		return errors.Wrap(err, "Error creating executor")
	}

	logger.SetPhase("STARTING_IO")
	cmd := "weka cluster start-io"
	_, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		logger.WithValues("stderr", stderr.String()).Error(err, "Failed to start-io")
		return errors.Wrapf(err, "Failed to start-io: %s", stderr.String())
	}
	logger.InfoWithStatus(codes.Ok, "IO started")
	logger.SetPhase("IO_STARTED")
	return nil
}

func (r *WekaClusterReconciler) isContainersReady(ctx context.Context, containers []*wekav1alpha1.WekaContainer) (bool, error) {
	ctx, logger := r.getLogSpan(ctx, "isContainersReady")
	defer logger.End()

	for _, container := range containers {
		l := logger.WithValues("namespace", container.Namespace, "name", container.Name)
		if container.GetDeletionTimestamp() != nil {
			l.Debug("Container is being deleted, rejecting cluster create")
			return false, errors.New("Container " + container.Name + " is being deleted, rejecting cluster create")
		}
		if container.Status.ManagementIP == "" {
			l.Debug("Container is not ready yet or has no valid management IOP")
			return false, nil
		}

		if container.Status.Status != "Running" {
			l.Debug("Container is not running yet")
			return false, nil
		}
	}
	logger.InfoWithStatus(codes.Ok, "Containers are ready")
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
	ctx, logger := r.getLogSpan(ctx, "ensureLoginCredentials")
	defer logger.End()

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

	logger.Debug("Random passwords generated")

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
	ctx, logger := r.getLogSpan(ctx, "applyClusterCredentials")
	defer logger.End()
	logger.SetPhase("APPLYING_CLUSTER_CREDENTIALS")
	executor, err := GetExecutor(containers[0], logger)
	if err != nil {
		logger.Error(err, "Error creating executor")
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

	logger.WithValues("user_name", domain.GetOperatorClusterUsername(cluster)).Info("Ensuring operator user")
	if err := ensureUser(domain.GetOperatorSecretName(cluster)); err != nil {
		logger.Error(err, "Failed to apply operator user credentials")
		return err
	}

	logger.WithValues("user_name", domain.GetUserClusterUsername(cluster)).Info("Ensuring admin user")
	if err := ensureUser(resources.GetUserSecretName(cluster)); err != nil {
		logger.Error(err, "Failed to apply admin user credentials")
		return err
	}

	for _, user := range existingUsers {
		if user.Username == "admin" {
			cmd = "wekaauthcli user delete admin"
			logger.Info("Deleting default admin user")
			_, stderr, err = executor.Exec(ctx, []string{"bash", "-ce", cmd})
			if err != nil {
				logger.Error(err, "Failed to delete admin user")
				return errors.Wrapf(err, "Failed to delete default admin user: %s", stderr.String())
			}
			return nil
		}
	}
	logger.SetPhase("CLUSTER_CREDENTIALS_APPLIED")
	return nil
}
