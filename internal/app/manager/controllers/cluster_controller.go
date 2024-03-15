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
	"errors"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"
	"github.com/kr/pretty"
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
	createBackend   = func(backend *wekav1alpha1.Backend) error
	createContainer = func(backend *wekav1alpha1.Backend, container *wekav1alpha1.WekaContainer) (ctrl.Result, error)
	listNodes       = func(list *v1.NodeList) error
	refreshBackend  = func(key types.NamespacedName, backend *wekav1alpha1.Backend) error
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
	logger := r.Logger.WithName("Reconcile")
	logger.Info("Reconcile() called", "name", req.NamespacedName)

	cluster, err := r.refreshCluster(ctx, req.NamespacedName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, pretty.Errorf("refreshCluster failed", err)
	}
	if cluster == nil {
		panic("cluster is nil")
	}

	reconciliation := iteration{
		logger:         r.Logger.WithName("iteration"),
		cluster:        cluster,
		namespacedName: req.NamespacedName,
		listNodes:      r.listNodes(ctx),
	}
	scheduler, err := reconciliation.schedulerForNodes(logger)
	if err != nil {
		var notReadyError *domain.BackendDriveAssignmentsNotReadyError
		if errors.As(err, &notReadyError) {
			logger.Info("backends not ready", "error", err)
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, pretty.Errorf("schedulerForNodes failed", err)
	}

	result, err := reconciliation.createBackends(scheduler.Backends(), r.refreshBackend(ctx), r.createBackend(ctx))
	if err != nil {
		logger.Error(err, "createBackends failed")
		return ctrl.Result{}, pretty.Errorf("createBackends failed", err)
	}
	if result.Requeue {
		return result, nil
	}

	if err := r.Status().Update(ctx, cluster); err != nil {
		logger.Error(err, "updateStatus failed")
		return ctrl.Result{}, nil
	}

	// Assign containers to nodes
	container := &wekav1alpha1.WekaContainer{}
	result, err = reconciliation.reconcileContainers(scheduler, container, r.createContainer(ctx))
	if err != nil {
		return ctrl.Result{}, pretty.Errorf("reconcileContainers failed", err)
	}
	if result.Requeue {
		return result, nil
	}

	return ctrl.Result{}, nil
}

func (r *ClusterReconciler) refreshCluster(ctx context.Context, key types.NamespacedName) (*wekav1alpha1.Cluster, error) {
	cluster := &wekav1alpha1.Cluster{}
	if err := r.Get(ctx, key, cluster); err != nil {
		return nil, err
	}

	return cluster, nil
}

func (r *iteration) createBackends(backends []*wekav1alpha1.Backend, refreshBackend refreshBackend, createBackend createBackend) (ctrl.Result, error) {
	logger := r.logger.WithName("createBackends")
	cluster := r.cluster
	namespace := cluster.Namespace
	for i := range backends {
		backend := backends[i]
		key := client.ObjectKey{Namespace: namespace, Name: backend.Name}

		if err := refreshBackend(key, backend); err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("creating backend", "name", backend.Name)
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

func (r *ClusterReconciler) createContainer(ctx context.Context) createContainer {
	return func(backend *wekav1alpha1.Backend, container *wekav1alpha1.WekaContainer) (ctrl.Result, error) {
		key := client.ObjectKey{Namespace: backend.Namespace, Name: container.Name}
		if err := r.Get(ctx, key, container); err != nil {
			if apierrors.IsNotFound(err) {
				if err := r.Create(ctx, container); err != nil {
					return ctrl.Result{}, pretty.Errorf("failed to create container", err)
				}
				return ctrl.Result{Requeue: true}, nil
			}
		}
		return ctrl.Result{}, nil
	}
}

func (r *ClusterReconciler) listNodes(ctx context.Context) listNodes {
	return func(list *v1.NodeList) error {
		if err := r.List(context.Background(), list); err != nil {
			r.Logger.Error(err, "failed to list nodes")
			return err
		}
		for _, node := range list.Items {
			r.Logger.Info("node", "name", node.Name)
		}

		return nil
	}
}

func (r *ClusterReconciler) refreshBackend(ctx context.Context) refreshBackend {
	return func(key types.NamespacedName, backend *wekav1alpha1.Backend) error {
		existing := &wekav1alpha1.Backend{}
		if err := r.Get(ctx, key, existing); err != nil {
			return err // Preserve the not-found error
		}
		backend.DeepCopyInto(existing)
		if err := r.Update(ctx, existing); err != nil {
			return pretty.Errorf("Update(backend) failed", err, backend)
		}
		return nil
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

func newIteration(req ctrl.Request, logger logr.Logger) *iteration {
	return &iteration{
		logger:         logger.WithName("iteration"),
		cluster:        &wekav1alpha1.Cluster{},
		backends:       wekav1alpha1.BackendList{},
		namespacedName: req.NamespacedName,
	}
}

type iteration struct {
	logger         logr.Logger
	cluster        *wekav1alpha1.Cluster
	backends       wekav1alpha1.BackendList
	namespacedName client.ObjectKey
	listNodes      listNodes
}

func (r *iteration) schedulerForNodes(logger logr.Logger) (*domain.Scheduling, error) {
	// Reconcile available nodes
	// List all available nodes
	nodes := &v1.NodeList{}
	if err := r.listNodes(nodes); err != nil {
		return nil, pretty.Errorf("listNodes failed", err)
	}

	backends := []*wekav1alpha1.Backend{}
	for _, node := range nodes.Items {
		if isBackendNode(node) {
			backend, err := r.newBackendForNode(&node)
			if err != nil {
				return nil, pretty.Errorf("newBackendForNode failed", err)
			}
			backends = append(backends, backend)
		}
	}
	return domain.ForCluster(r.cluster, backends, logger)
}

func isBackendNode(node v1.Node) bool {
	roleLabel, ok := node.Labels["weka.io/role"]
	return ok && roleLabel == "backend"
}

func (i *iteration) newBackendForNode(node *v1.Node) (*wekav1alpha1.Backend, error) {
	logger := i.logger.WithName("newBackendForNode")
	logger.Info("for node", "name", node.Name)
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
	}, nil
}

func (r *iteration) reconcileContainers(scheduler *domain.Scheduling, container *wekav1alpha1.WekaContainer, createContainer createContainer) (ctrl.Result, error) {
	if scheduler == nil {
		return ctrl.Result{}, pretty.Errorf("scheduler is nil")
	}

	key := &v1.LocalObjectReference{Name: container.Name}
	if err := scheduler.AssignBackends(key); err != nil {
		return ctrl.Result{}, pretty.Errorf("AssignBackends failed", err)
	}

	for _, backend := range scheduler.Backends() {
		result, err := createContainer(backend, container)
		if err != nil {
			return ctrl.Result{}, pretty.Errorf("createContainer failed", err)
		}
		if result.Requeue {
			return result, nil
		}
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.Cluster{}).
		Complete(r)
}
