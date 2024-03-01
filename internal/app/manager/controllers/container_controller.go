package controllers

import (
	"context"

	"github.com/go-logr/logr"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
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
	return ctrl.Result{}, nil
}

func (c *ContainerController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.WekaContainer{}).
		Complete(c)
}
