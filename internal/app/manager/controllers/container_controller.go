package controllers

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/kr/pretty"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func NewContainerController(mgr ctrl.Manager) *ContainerController {
	return &ContainerController{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Logger: mgr.GetLogger().WithName("controllers").WithName("Container"),
	}
}

type ContainerController struct {
	client.Client
	Scheme *runtime.Scheme
	Logger logr.Logger
}

func (c *ContainerController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	c.Logger.Info("ContainerController.Reconcile() called")
	container, err := c.refreshContainer(ctx, req)
	if err != nil {
		if apierrors.IsNotFound(err) {
			c.Logger.Info("Container not found", "name", req.Name)
			return ctrl.Result{}, nil
		}
		c.Logger.Error(err, "Error refreshing container")
		return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
	}

	desiredDeployment, err := resources.NewContainerFactory(container).NewDeployment()
	if err != nil {
		c.Logger.Error(err, "Error creating deployment spec")
		return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
	}

	actualDeployment, err := c.refreshDeployment(ctx, container)
	if err != nil {
		if apierrors.IsNotFound(err) {
			c.Logger.Info("Creating deployment", "name", container.Name)
			if err := c.Create(ctx, desiredDeployment); err != nil {
				return ctrl.Result{},
					pretty.Errorf("Error creating deployment", err, desiredDeployment)
			}
			c.Logger.Info("Deployment created", "name", container.Name)
			return ctrl.Result{Requeue: true}, nil
		} else {
			c.Logger.Info("Error refreshing deployment", "name", container.Name)
			return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
		}
	}

	// Diff the actual and desired Deploymen
	hasChanges := desiredDeployment.Spec.Template.Spec.Containers[0].Image !=
		actualDeployment.Spec.Template.Spec.Containers[0].Image

	if hasChanges {
		c.Logger.Info("Updating deployment", "name", container.Name)
		if err := c.updateDeployment(ctx, desiredDeployment); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
		}
		c.Logger.Info("Deployment updated", "name", container.Name)
		return ctrl.Result{Requeue: true}, nil
	}

	c.Logger.Info("Reconcile completed", "name", container.Name)
	return ctrl.Result{}, nil
}

func (c *ContainerController) refreshContainer(ctx context.Context, req ctrl.Request) (*wekav1alpha1.WekaContainer, error) {
	container := &wekav1alpha1.WekaContainer{}
	if err := c.Get(ctx, req.NamespacedName, container); err != nil {
		return nil, errors.Wrap(err, "refreshContainer")
	}
	return container, nil
}

func (c *ContainerController) refreshDeployment(ctx context.Context, container *wekav1alpha1.WekaContainer) (*appsv1.Deployment, error) {
	deployment := &appsv1.Deployment{}
	key := client.ObjectKey{Name: container.Name, Namespace: container.Namespace}
	if err := c.Get(ctx, key, deployment); err != nil {
		return nil, errors.Wrap(err, "refreshDeployment")
	}

	return deployment, nil
}

func (c *ContainerController) createDeployment(ctx context.Context, deployment *appsv1.Deployment) error {
	if err := c.Create(ctx, deployment); err != nil {
		return errors.Wrap(err, "createDeployment")
	}

	return nil
}

func (c *ContainerController) updateDeployment(ctx context.Context, deployment *appsv1.Deployment) error {
	if err := c.Update(ctx, deployment); err != nil {
		return errors.Wrap(err, "updateDeployment")
	}
	return nil
}

func (c *ContainerController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.WekaContainer{}).
		Complete(c)
}
