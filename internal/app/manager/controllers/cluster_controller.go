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
	"strconv"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"
	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func NewClusterReconciler(mgr ctrl.Manager) *ClusterReconciler {
	logger := ctrl.Log.WithName("controllers").WithName("Cluster")
	logger.V(2).Info("NewClusterReconciler() called")
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

type SizeClass struct {
	ContainerCount int
	DriveCount     int
}

var SizeClasses = map[wekav1alpha1.SizeClass]SizeClass{
	"dev":    {1, 1},
	"small":  {3, 3},
	"medium": {5, 5},
	"large":  {7, 7},
}

//+kubebuilder:rbac:groups=weka.weka.io,resources=clusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=clusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=clusters/finalizers,verbs=update
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekacontainers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekacontainers/status,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.Logger.V(2).Info("ClusterReconciler.Reconcile() called")

	cluster := &wekav1alpha1.Cluster{}
	if err := r.Get(ctx, req.NamespacedName, cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Reconcile available nodes
	result, err := r.reconcileAvailableNodes(ctx, req, cluster)
	if err != nil {
		return result, err
	}

	// Assign containers to nodes
	result, err = r.reconcileContainers(ctx, req, cluster)
	if err != nil {
		return result, errors.Wrap(err, "failed to reconcile containers")
	}

	return ctrl.Result{}, nil
}

func (r *ClusterReconciler) reconcileAvailableNodes(ctx context.Context, req ctrl.Request, cluster *wekav1alpha1.Cluster) (ctrl.Result, error) {
	r.Logger.V(2).Info("ClusterReconciler.reconcileAvailableNodes() called")
	// List all available nodes
	nodes := &v1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		return ctrl.Result{}, err
	}
	for _, node := range nodes.Items {
		r.createWekaNode(ctx, node, cluster)
	}

	return ctrl.Result{}, nil
}

// createWekaNode creates a Weka node for the given Kubernetes node
// Only backends for now
func (r *ClusterReconciler) createWekaNode(ctx context.Context, node v1.Node, cluster *wekav1alpha1.Cluster) error {
	r.Logger.V(2).Info("ClusterReconciler.createWekaNode() called")

	// Backends are identified with the "weka.io/role=backend" label
	roleLabel, ok := node.Labels["weka.io/role"]
	r.Logger.V(2).Info("Node: ", "name", node.Name, "roleLabel", roleLabel)
	if !ok || roleLabel != "backend" {
		r.Logger.V(2).Info("Node is not a backend")
		return nil
	}

	// Check if the backend already
	backend := &wekav1alpha1.Backend{}
	namespace := cluster.Namespace
	err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: node.Name}, backend)
	if err != nil && !apierrors.IsNotFound(err) {
		return errors.Wrap(err, "failed to get backend")
	}

	r.createBackend(ctx, node, cluster)
	return nil
}

func (r *ClusterReconciler) createBackend(ctx context.Context, node v1.Node, cluster *wekav1alpha1.Cluster) error {
	r.Logger.V(2).Info("ClusterReconciler.createBackend() called")

	backend := &wekav1alpha1.Backend{
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
			ClusterName: cluster.Name,
			NodeName:    node.Name,
		},
	}
	err := r.Create(ctx, backend)
	if err != nil {
		return errors.Wrap(err, "failed to create backend")
	}

	return nil
}

func (r *ClusterReconciler) reconcileContainers(ctx context.Context, req ctrl.Request, cluster *wekav1alpha1.Cluster) (ctrl.Result, error) {
	r.Logger.V(2).Info("ClusterReconciler.reconcileContainers() called")

	// Number of containers to create determined by the size class
	sizeClass, ok := SizeClasses[cluster.Spec.SizeClass]
	if !ok {
		return ctrl.Result{}, errors.Errorf("invalid size class: %s", cluster.Spec.SizeClass)
	}

	var errors error
	for i := 0; i < sizeClass.ContainerCount; i++ {
		container := &wekav1alpha1.WekaContainer{
			ObjectMeta: ctrl.ObjectMeta{
				Name:      cluster.Name + "-container-" + strconv.Itoa(i),
				Namespace: cluster.Namespace,
				Labels: map[string]string{
					"weka.io/cluster": cluster.Name,
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
			Spec: wekav1alpha1.ContainerSpec{
				Name:    cluster.Name + "-container-" + strconv.Itoa(i),
				Cluster: v1.ObjectReference{Name: cluster.Name, Namespace: cluster.Namespace},
			},
		}

		key := client.ObjectKey{Namespace: container.Namespace, Name: container.Name}
		if err := r.Get(ctx, key, container); err != nil {
			if apierrors.IsNotFound(err) {
				if err := r.Create(ctx, container); err != nil {
					errors = multierror.Append(errors, err)
					continue
				}
			} else {
				errors = multierror.Append(errors, err)
				continue
			}
		}

		container.Status = wekav1alpha1.ContainerStatus{
			AssignedNode: v1.Node{
				ObjectMeta: ctrl.ObjectMeta{
					Name: "node-" + strconv.Itoa(i),
				},
			},
		}

		if err := r.Status().Update(ctx, container); err != nil {
			errors = multierror.Append(errors, err)
			continue
		}
	}
	return ctrl.Result{}, errors
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.Cluster{}).
		Complete(r)
}
