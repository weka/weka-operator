package node

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// resyncPeriod is the fallback for a missed node event while clientsTolerateAll is on; flag changes arrive
// through the configuration WekaPolicy watch.
const resyncPeriod = 5 * time.Minute

// NodeReconciler labels nodes whose Ready condition is not True, while scheduling.clientsTolerateAll is on.
type NodeReconciler struct {
	client.Client
}

func NewNodeController(mgr ctrl.Manager) *NodeReconciler {
	return &NodeReconciler{Client: mgr.GetClient()}
}

var readinessChanged = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldNode, okOld := e.ObjectOld.(*corev1.Node)
		newNode, okNew := e.ObjectNew.(*corev1.Node)
		if !okOld || !okNew {
			return false
		}
		return resources.NodeIsReady(oldNode) != resources.NodeIsReady(newNode) || isLabelled(oldNode) != isLabelled(newNode)
	},
	DeleteFunc:  func(event.DeleteEvent) bool { return false },
	GenericFunc: func(event.GenericEvent) bool { return false },
}

func isLabelled(node *corev1.Node) bool {
	_, ok := node.Labels[domain.WekaLabelNotReady]
	return ok
}

func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager, wrappedReconcile reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("weka-node-readiness").
		For(&corev1.Node{}, builder.WithPredicates(readinessChanged)).
		Watches(&weka.WekaPolicy{}, handler.EnqueueRequestsFromMapFunc(r.allNodesOnConfigurationChange),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(wrappedReconcile)
}

// allNodesOnConfigurationChange re-resolves the settings before enqueueing, because the WekaPolicy controller
// does it concurrently and a node reconciled first would still read the old flag. Both read the informer cache.
func (r *NodeReconciler) allNodesOnConfigurationChange(ctx context.Context, obj client.Object) []reconcile.Request {
	policy, ok := obj.(*weka.WekaPolicy)
	if !ok || policy.Spec.Payload.Configuration == nil {
		return nil
	}
	logger := instrumentation.CurrentSpanLogger(ctx)
	// Enqueue even when the refresh fails: Reconcile retries every 10s while the settings are unresolved, so no
	// node is left without a retry.
	if err := services.RefreshSettings(ctx, r.Client); err != nil {
		logger.Error(err, "Failed to refresh the operator configuration, nodes retry until it resolves")
	}

	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		logger.Error(err, "Failed to list nodes, they are labelled on the next resync")
		return nil
	}
	requests := make([]reconcile.Request, len(nodes.Items))
	for i := range nodes.Items {
		requests[i] = reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&nodes.Items[i])}
	}
	return requests
}

func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	settings, ok, err := services.TryGetSettings()
	if !ok {
		instrumentation.CurrentSpanLogger(ctx).Info("Operator configuration unresolved, retrying", "error", err)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	return r.reconcileNode(ctx, req.Name, settings.Scheduling.ClientsTolerateAll)
}

func (r *NodeReconciler) reconcileNode(ctx context.Context, name string, clientsTolerateAll bool) (ctrl.Result, error) {
	node := &corev1.Node{}
	if err := r.Get(ctx, client.ObjectKey{Name: name}, node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	want := clientsTolerateAll && !resources.NodeIsReady(node)
	if want != isLabelled(node) {
		patch := client.MergeFrom(node.DeepCopy())
		if want {
			if node.Labels == nil {
				node.Labels = map[string]string{}
			}
			node.Labels[domain.WekaLabelNotReady] = "true"
		} else {
			delete(node.Labels, domain.WekaLabelNotReady)
		}
		if err := r.Patch(ctx, node, patch); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}
	if !clientsTolerateAll {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: resyncPeriod}, nil
}
