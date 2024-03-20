package controllers

import (
	"context"
	"fmt"
	"github.com/weka/weka-operator/util"
	"log"
	"strings"

	"github.com/go-logr/logr"
	"github.com/kr/pretty"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const bootScriptConfigName = "weka-boot-scripts"

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

//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;update;create

// Reconcile reconciles a WekaContainer resource
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

	if container.GetDeletionTimestamp() != nil {
		logger.Info("Container is being deleted", "name", container.Name)
		return ctrl.Result{}, nil
	}

	desiredPod, err := resources.NewContainerFactory(container, logger).Create()
	if err != nil {
		logger.Error(err, "Error creating pod spec")
		return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
	}
	if err := ctrl.SetControllerReference(container, desiredPod, c.Scheme); err != nil {
		logger.Error(err, "Error setting controller reference")
		return ctrl.Result{}, pretty.Errorf("Error setting controller reference", err, desiredPod)
	}

	err = c.ensureBootConfigMapInTargetNamespace(ctx, container)
	if err != nil {
		return ctrl.Result{}, pretty.Errorf("Error ensuring boot config map", err)
	}

	actualPod, err := c.refreshPod(ctx, container)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Creating pod", "name", container.Name)
			if err := c.Create(ctx, desiredPod); err != nil {
				return ctrl.Result{},
					pretty.Errorf("Error creating pod", err, desiredPod)
			}
			logger.Info("Pod created", "name", container.Name)
			return ctrl.Result{Requeue: true}, nil
		} else {
			logger.Info("Error refreshing pod", "name", container.Name)
			return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
		}
	}

	hasChanges := desiredPod.Spec.Containers[0].Image !=
		actualPod.Spec.Containers[0].Image

	if hasChanges {
		logger.Info("Updating pod", "name", container.Name)
		if err := c.updatePod(ctx, desiredPod); err != nil {
			logger.Error(err, "Error updating pod", "name", container.Name)
			return ctrl.Result{}, err
		}
		logger.Info("Pod updated", "name", container.Name)
		return ctrl.Result{Requeue: true}, nil
	}

	if container.Status.ManagementIP == "" {
		executor, err := util.NewExecInPod(actualPod)
		logger.Info("Updating ManagementIP")
		var getIpCmd string
		if err != nil {
			return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
		}
		if container.Spec.Network.EthDevice != "" {
			getIpCmd = fmt.Sprintf("ip addr show dev %s | grep 'inet ' | awk '{print $2}' | cut -d/ -f1", container.Spec.Network.EthDevice)
		} else {
			getIpCmd = fmt.Sprintf("ip route show default | grep src | awk '/default/ {print $9}'")
		}
		stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", getIpCmd})
		if err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "ClientController.Reconcile.IPResolution: %s", stderr.String())
		}
		container.Status.ManagementIP = strings.TrimSpace(stdout.String())
		logger.Info("Got IP: " + container.Status.ManagementIP)
		if err := c.Status().Update(ctx, container); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile.UpdateIp")
		}
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
		Owns(&v1.Pod{}).
		Complete(c)
}

func (c *ContainerController) ensureBootConfigMapInTargetNamespace(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
	bundledConfigMap := &v1.ConfigMap{}
	err := c.Get(ctx, client.ObjectKey{Namespace: util.GetPodNamespace(), Name: bootScriptConfigName}, bundledConfigMap)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Fatalln("Could not find operator-namespaced configmap for boot scripts")
		}
		return err
	}

	bootScripts := &v1.ConfigMap{}
	err = c.Get(ctx, client.ObjectKey{Namespace: container.Namespace, Name: bootScriptConfigName}, bootScripts)
	if err != nil {
		if apierrors.IsNotFound(err) {
			bootScripts.Namespace = container.Namespace
			bootScripts.Name = bootScriptConfigName
			bootScripts.Data = bundledConfigMap.Data
			if err := c.Create(ctx, bootScripts); err != nil {
				c.Logger.Error(err, "Error creating boot scripts config map")
			}
		}
	}

	if !util.IsEqualConfigMapData(bootScripts, bundledConfigMap) {
		bootScripts.Data = bundledConfigMap.Data
		if err := c.Update(ctx, bootScripts); err != nil {
			c.Logger.Error(err, "Error updating boot scripts config map")
		}
	}
	return nil
}
