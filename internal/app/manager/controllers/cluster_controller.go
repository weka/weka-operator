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

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"
	"github.com/kr/pretty"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func NewClusterReconciler(mgr ctrl.Manager) *ClusterReconciler {
	logger := ctrl.Log.WithName("controllers").WithName("Cluster")
	return &ClusterReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Logger: logger,
	}
}

// ClusterReconciler reconciles a Cluster object
type ClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Logger logr.Logger
}

type (
	listNodes      = func(list *v1.NodeList) error
	refreshBackend = func(key types.NamespacedName, backend *wekav1alpha1.Backend) error
	createBackend  = func(backend *wekav1alpha1.Backend) error
)

//+kubebuilder:rbac:groups=weka.weka.io,resources=clusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=clusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=clusters/finalizers,verbs=update
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekacontainers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekacontainers/status,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.Logger.Info("ClusterReconciler.Reconcile() called")

	cluster, err := r.refreshCluster(ctx, req.NamespacedName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, errors.Wrap(err, "failed to reconcile cluster")
	}
	if cluster == nil {
		panic("cluster is nil")
	}

	reconciliation := iteration{
		cluster:        cluster,
		namespacedName: req.NamespacedName,
	}
	scheduler, err := reconciliation.schedulerForNodes(r.listNodes(ctx))
	if err != nil {
		r.Logger.Error(err, "schedulerForNodes failed")
		return ctrl.Result{}, pretty.Errorf("schedulerForNodes failed", err)
	}

	result, err := reconciliation.createBackends(scheduler.Backends(), r.refreshBackend(ctx), r.createBackend(ctx))
	if err != nil {
		r.Logger.Error(err, "createBackends failed")
		return ctrl.Result{}, pretty.Errorf("createBackends failed", err)
	}
	if result.Requeue {
		return result, nil
	}

	if err := r.Status().Update(ctx, cluster); err != nil {
		r.Logger.Error(err, "updateStatus failed")
		return ctrl.Result{}, nil
	}

	// Assign containers to nodes
	//if err := reconciliation.reconcileContainers(ctx, req); err != nil {
	//return ctrl.Result{}, errors.Wrap(err, "failed to reconcile containers")
	//}

	return ctrl.Result{}, nil
}

func (r *ClusterReconciler) refreshCluster(ctx context.Context, key types.NamespacedName) (*wekav1alpha1.Cluster, error) {
	cluster := &wekav1alpha1.Cluster{}
	if err := r.Get(ctx, key, cluster); err != nil {
		return nil, errors.Wrap(err, "failed to refresh cluster")
	}

	return cluster, nil
}

func (r *iteration) createBackends(backends []*wekav1alpha1.Backend, refreshBackend refreshBackend, createBackend createBackend) (ctrl.Result, error) {
	cluster := r.cluster
	namespace := cluster.Namespace
	for i := range backends {
		backend := backends[i]
		key := client.ObjectKey{Namespace: namespace, Name: backend.Name}

		if err := refreshBackend(key, backend); err != nil {
			if apierrors.IsNotFound(err) {
				if err := createBackend(backend); err != nil {
					return ctrl.Result{}, pretty.Errorf("createBackend failed", err, backend)
				}
				return ctrl.Result{Requeue: true}, nil
			} else {
				return ctrl.Result{}, pretty.Errorf("refreshBackend failed", err, backend)
			}
		}

	}
	return ctrl.Result{}, nil
}

func (r *ClusterReconciler) listNodes(ctx context.Context) listNodes {
	return func(list *v1.NodeList) error {
		return r.List(context.Background(), list)
	}
}

func (r *ClusterReconciler) refreshBackend(ctx context.Context) refreshBackend {
	return func(key types.NamespacedName, backend *wekav1alpha1.Backend) error {
		return r.Get(ctx, key, backend)
	}
}

func (r *ClusterReconciler) createBackend(ctx context.Context) createBackend {
	return func(backend *wekav1alpha1.Backend) error {
		return r.Create(ctx, backend)
	}
}

func (r *ClusterReconciler) updateStatus(ctx context.Context, cluster *wekav1alpha1.Cluster) error {
	return r.Status().Update(ctx, cluster)
}

func newIteration(req ctrl.Request) *iteration {
	return &iteration{
		cluster:        &wekav1alpha1.Cluster{},
		backends:       wekav1alpha1.BackendList{},
		namespacedName: req.NamespacedName,
	}
}

type iteration struct {
	cluster        *wekav1alpha1.Cluster
	backends       wekav1alpha1.BackendList
	namespacedName client.ObjectKey
}

func (r *iteration) schedulerForNodes(listNodes listNodes) (*domain.Scheduling, error) {
	// Reconcile available nodes
	// List all available nodes
	nodes := &v1.NodeList{}
	if err := listNodes(nodes); err != nil {
		return nil, errors.Wrap(err, "failed to list nodes")
	}

	backends := []*wekav1alpha1.Backend{}
	for _, node := range nodes.Items {
		if isBackendNode(node) {
			backend := r.newBackendForNode(&node)
			backends = append(backends, backend)
		}
	}
	scheduler := domain.ForCluster(r.cluster, backends)

	return scheduler, nil
}

func isBackendNode(node v1.Node) bool {
	roleLabel, ok := node.Labels["weka.io/role"]
	return ok && roleLabel == "backend"
}

func (i *iteration) newBackendForNode(node *v1.Node) *wekav1alpha1.Backend {
	cluster := i.cluster
	return &wekav1alpha1.Backend{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      node.Name,
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				"weka.io/cluster": cluster.Name,
				"weka.io/node":    node.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: wekav1alpha1.GroupVersion.String(),
					Kind:       "Cluster",
					Name:       cluster.Name,
					UID:        cluster.UID,
				},
			},
		},
		Spec: wekav1alpha1.BackendSpec{
			NodeName: node.Name,
		},
	}
}

//func (r *iteration) reconcileContainers(ctx context.Context, req ctrl.Request) error {
//if r.cluster == nil {
//return errors.New("argument error: cluster is nil")
//}
//if r.cluster.Spec.SizeClass == "" {
//return errors.New("size class not set")
//}

//cluster := r.cluster
//// Number of containers to create determined by the size class
//sizeClassName := cluster.Spec.SizeClass
//sizeClass, ok := SizeClasses[sizeClassName]
//if !ok {
//return errors.Errorf("invalid size class: %s", sizeClassName)
//}

//var errors error
//for i := 0; i < sizeClass.ContainerCount; i++ {
//container := &wekav1alpha1.WekaContainer{
//ObjectMeta: ctrl.ObjectMeta{
//Name:      cluster.Name + "-container-" + strconv.Itoa(i),
//Namespace: cluster.Namespace,
//Labels: map[string]string{
//"weka.io/cluster": cluster.Name,
//},
//OwnerReferences: []metav1.OwnerReference{
//{
//APIVersion: wekav1alpha1.GroupVersion.String(),
//Kind:       "Cluster",
//Name:       cluster.Name,
//UID:        cluster.UID,
//},
//},
//},
//Spec: wekav1alpha1.ContainerSpec{
//Name:    cluster.Name + "-container-" + strconv.Itoa(i),
//Cluster: v1.ObjectReference{Name: cluster.Name, Namespace: cluster.Namespace},
//},
//}

//key := client.ObjectKey{Namespace: container.Namespace, Name: container.Name}
//if err := r.Get(ctx, key, container); err != nil {
//if apierrors.IsNotFound(err) {
//if err := r.Create(ctx, container); err != nil {
//errors = multierror.Append(errors, err)
//continue
//}
//} else {
//errors = multierror.Append(errors, err)
//continue
//}
//}

//if err := r.assignContainerToNode(ctx, container); err != nil {
//errors = multierror.Append(errors, err)
//continue
//}
//}
//return errors
//}

//func (r *iteration) assignContainerToNode(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
//for _, backend := range r.backends.Items {
//// Check if the container is already assigned to a node
//if container.Status.AssignedNode.Name != "" {
//return nil
//}

//// Check if there are available drives on the backend
//for drive, container := range backend.Status.Assignments {
//if container == nil {
//backend.Status.Assignments[drive] = container
//container.Status.AssignedNode = backend.Status.Node
//if err := r.Status().Update(ctx, container); err != nil {
//return errors.Wrap(err, "failed to assign container to node")
//}
//return nil
//}
//}
//}

//return errors.New("no nodes available for container")
//}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.Cluster{}).
		Complete(r)
}
