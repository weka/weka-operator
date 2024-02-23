package nodeLabeller

import (
	"context"
	"os"
	"strconv"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/device"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Controller struct {
	client.Client
	logger   logr.Logger
	nodeName string
}

func NewController(mgr ctrl.Manager, logger logr.Logger, nodeName string) *Controller {
	return &Controller{
		Client:   mgr.GetClient(),
		logger:   logger.WithName("controllers").WithName("NodeLabeller"),
		nodeName: nodeName,
	}
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Node{}).
		Complete(c)
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Name != c.nodeName {
		return ctrl.Result{}, nil
	}

	c.logger.Info("Reconcile", "name", req.Name)
	node := &v1.Node{}
	if err := c.Get(ctx, req.NamespacedName, node); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, errors.Wrap(err, "node not found")
		} else {
			return ctrl.Result{}, errors.Wrap(err, "failed to get node")
		}
	}

	podName := os.Getenv("HOSTNAME")
	node.Labels["weka.io/labeller"] = podName

	if err := c.addDriveAnnotations(node); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to add drive labels")
	}

	if err := c.Update(ctx, node); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to update node")
	}

	return ctrl.Result{}, nil
}

func (c *Controller) addDriveAnnotations(node *v1.Node) error {
	drives, err := device.ListDrives()
	if err != nil {
		return errors.Wrap(err, "failed to list drives")
	}
	for _, drive := range drives {
		c.logger.Info("Drive", "name", drive.Name, "path", drive.Path)
	}
	node.Annotations["weka.io/drives"] = strconv.Itoa(len(drives))

	return nil
}
