package controllers

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/kr/pretty"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
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
	logger := c.Logger.WithName("Reconcile")
	logger.Info("ContainerController.Reconcile() called")
	container, err := c.refreshContainer(ctx, req)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Container not found", "name", req.Name)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Error refreshing container")
		return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
	}

	desiredPod, err := resources.NewContainerFactory(container, logger).Create()
	if err != nil {
		logger.Error(err, "Error creating deployment spec")
		return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
	}
	if err := ctrl.SetControllerReference(container, desiredPod, c.Scheme); err != nil {
		logger.Error(err, "Error setting controller reference")
		return ctrl.Result{}, pretty.Errorf("Error setting controller reference", err, desiredPod)
	}

	actualPod, err := c.refreshPod(ctx, container)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Creating deployment", "name", container.Name)
			if err := c.Create(ctx, desiredPod); err != nil {
				return ctrl.Result{},
					pretty.Errorf("Error creating deployment", err, desiredPod)
			}
			logger.Info("Deployment created", "name", container.Name)
			return ctrl.Result{Requeue: true}, nil
		} else {
			logger.Info("Error refreshing deployment", "name", container.Name)
			return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
		}
	}

	// Diff the actual and desired Deploymen
	hasChanges := desiredPod.Spec.Containers[0].Image !=
		actualPod.Spec.Containers[0].Image

	if hasChanges {
		logger.Info("Updating deployment", "name", container.Name)
		if err := c.updatePod(ctx, desiredPod); err != nil {
			logger.Error(err, "Error updating deployment", "name", container.Name)
			return ctrl.Result{}, err
		}
		logger.Info("Deployment updated", "name", container.Name)
		return ctrl.Result{Requeue: true}, nil
	}

	logger.Info("Reconcile completed", "name", container.Name)
	return ctrl.Result{}, nil
}

func (c *ContainerController) refreshContainer(ctx context.Context, req ctrl.Request) (*wekav1alpha1.WekaContainer, error) {
	container := &wekav1alpha1.WekaContainer{}
	if err := c.Get(ctx, req.NamespacedName, container); err != nil {
		return nil, errors.Wrap(err, "refreshContainer")
	}
	return container, nil
}

func (c *ContainerController) refreshPod(ctx context.Context, container *wekav1alpha1.WekaContainer) (*v1.Pod, error) {
	logger := c.Logger.WithName("refreshPod")
	pod := &v1.Pod{}
	key := client.ObjectKey{Name: container.Name, Namespace: container.Namespace}
	if err := c.Get(ctx, key, pod); err != nil {
		logger.Error(err, "Error refreshing pod", "key", key)
		return nil, err
	}

	return pod, nil
}

func (c *ContainerController) createDeployment(ctx context.Context, deployment *appsv1.Deployment) error {
	if err := c.Create(ctx, deployment); err != nil {
		return errors.Wrap(err, "createDeployment")
	}

	return nil
}

func (c *ContainerController) updatePod(ctx context.Context, pod *v1.Pod) error {
	logger := c.Logger.WithName("updatePod")
	if err := c.Update(ctx, pod); err != nil {
		logger.Error(err, "Error updating pod", "pod", pod)
		return err
	}
	return nil
}

func (c *ContainerController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.WekaContainer{}).
		Owns(&appsv1.Deployment{}).
		Complete(c)
}
