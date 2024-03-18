package nodeLabeller

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/go-logr/logr"
	"github.com/hashicorp/go-multierror"
	"github.com/kr/pretty"
	"github.com/weka/weka-operator/internal/pkg/device"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
)

const SystemNamespace = "weka-operator-system"

type Controller struct {
	client.Client
	Scheme   *runtime.Scheme
	logger   logr.Logger
	nodeName string

	// State
	node   *v1.Node
	drives *wekav1alpha1.DriveList
}

func NewController(mgr ctrl.Manager, logger logr.Logger, nodeName string) *Controller {
	return &Controller{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		logger:   logger.WithName("controllers").WithName("NodeLabeller"),
		nodeName: nodeName,

		drives: &wekav1alpha1.DriveList{},
		node:   &v1.Node{},
	}
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Node{}).
		Complete(c)
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := c.logger.WithName("Reconcile")
	if req.Name != c.nodeName {
		return ctrl.Result{}, nil
	}

	c.node = &v1.Node{}

	logger.Info("Reconcile() called", "name", req.NamespacedName)
	if err := c.Get(ctx, req.NamespacedName, c.node); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, pretty.Errorf("node not found", err)
		} else {
			return ctrl.Result{}, pretty.Errorf("failed to get node", err)
		}
	}

	podName := os.Getenv("HOSTNAME")
	c.node.Labels["weka.io/labeller"] = podName

	result, err := c.reconcileDrives(ctx)
	if err != nil {
		return result, pretty.Errorf("failed to reconcile drives", err)
	}
	if result.Requeue {
		return result, nil
	}

	result, err = c.reconcileBackend(ctx)
	if err != nil {
		return result, pretty.Errorf("failed to reconcile backends", err)
	}
	if result.Requeue {
		return result, nil
	}

	return ctrl.Result{}, nil
}

func (c *Controller) reconcileDrives(ctx context.Context) (ctrl.Result, error) {
	logger := c.logger.WithName("reconcileDrives")
	drives, err := device.ListDrives()
	if err != nil {
		logger.Error(err, "failed to list drives")
		return ctrl.Result{}, err
	}

	if err := c.addDriveAnnotations(c.node, drives); err != nil {
		return ctrl.Result{}, pretty.Errorf("failed to add drive annotations", err)
	}

	if err := c.Update(ctx, c.node); err != nil {
		return ctrl.Result{}, pretty.Errorf("failed to update node", err)
	}

	result, err := c.registerDrives(ctx, c.node, drives)
	if err != nil {
		return ctrl.Result{}, pretty.Errorf("failed to register drives", err)
	}
	if result.Requeue {
		return result, nil
	}

	driveCrds := wekav1alpha1.DriveList{}
	if err := c.List(ctx, &driveCrds, client.InNamespace(SystemNamespace)); err != nil {
		logger.Error(err, "failed to list drives", "namespace", SystemNamespace)
		return ctrl.Result{}, err
	}
	c.drives = &wekav1alpha1.DriveList{}
	for _, drive := range driveCrds.Items {
		if drive.Spec.NodeName == c.node.Name {
			c.drives.Items = append(c.drives.Items, drive)
		}
	}

	if len(c.drives.Items) != len(drives) {
		logger.Info("drives mismatch", "expected", len(drives), "actual", len(c.drives.Items))
		panic("drives mismatch")
	}

	return ctrl.Result{}, nil
}

func (c *Controller) addDriveAnnotations(node *v1.Node, drives []device.Drive) error {
	logger := c.logger.WithName("addDriveAnnotations")
	for _, drive := range drives {
		logger.Info("Drive", "name", drive.Name, "path", drive.Path)
	}
	node.Annotations["weka.io/drives"] = strconv.Itoa(len(drives))

	return nil
}

func (c *Controller) registerDrives(ctx context.Context, node *v1.Node, drives []device.Drive) (ctrl.Result, error) {
	logger := c.logger.WithName("registerDrives")
	var errors error
	for _, d := range drives {
		logger.Info("Registering drive", "name", d.Name, "path", d.Path, "uuid", d.UUID)

		drive := &wekav1alpha1.Drive{}
		key := types.NamespacedName{Name: fmt.Sprintf("%s-%s", node.Name, d.Name), Namespace: SystemNamespace}
		if err := c.Get(ctx, key, drive); err != nil {
			if apierrors.IsNotFound(err) {

				drive := &wekav1alpha1.Drive{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("%s-%s", node.Name, d.Name),
						Namespace: SystemNamespace,
					},
					Spec: wekav1alpha1.DriveSpec{
						NodeName: node.Name,
						Name:     d.Name,
					},
					Status: wekav1alpha1.DriveStatus{
						Path: d.Path,
						UUID: d.UUID,
					},
				}
				if err := c.Create(ctx, drive); err != nil {
					logger.Error(err, "Failed to create drive", "name", d.Name, "path", d.Path)
					errors = multierror.Append(errors, err)
					continue
				} else {
					return ctrl.Result{Requeue: true}, nil
				}
			} else {
				errors = multierror.Append(errors, err)
				continue
			}
		}

		drive.Status.Path = d.Path
		drive.Status.UUID = d.UUID
		if err := c.Status().Update(ctx, drive); err != nil {
			logger.Error(err, "Failed to update drive", "name", d.Name, "path", d.Path)
			errors = multierror.Append(errors, err)
			continue
		}
	}
	return ctrl.Result{}, errors
}

func (c *Controller) reconcileBackend(ctx context.Context) (ctrl.Result, error) {
	logger := c.logger.WithName("reconcileBackend")
	if c.drives.Items == nil {
		err := pretty.Errorf("drives not found")
		logger.Error(err, "drives not found")
		return ctrl.Result{Requeue: true}, err
	}

	backend := &wekav1alpha1.Backend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.node.Name,
			Namespace: SystemNamespace,
		},
		Spec: wekav1alpha1.BackendSpec{
			NodeName: c.node.Name,
		},
	}

	if err := c.Get(ctx, client.ObjectKeyFromObject(backend), backend); err != nil {
		if apierrors.IsNotFound(err) {
			if err := c.Create(ctx, backend); err != nil {
				return ctrl.Result{}, pretty.Errorf("failed to create backend", err)
			}
			logger.Info("created backend", "name", backend.Name)
			return ctrl.Result{Requeue: true}, nil
		} else {
			return ctrl.Result{}, pretty.Errorf("failed to get backend", err)
		}
	}

	if len(backend.Status.DriveAssignments) == 0 {
		logger.Info("initializing drive assignments", "name", backend.Name, "drives", len(c.drives.Items))
		backend.Status.DriveAssignments = make(map[wekav1alpha1.DriveName]*v1.LocalObjectReference)
		for _, drive := range c.drives.Items {
			backend.Status.DriveAssignments[wekav1alpha1.DriveName(drive.Name)] = &v1.LocalObjectReference{}
		}

		if err := c.Status().Update(ctx, backend); err != nil {
			return ctrl.Result{}, pretty.Errorf("failed to update backend", err)
		}
		logger.Info("initialized drive assignments", "name", backend.Name, "drives", len(backend.Status.DriveAssignments))
		return ctrl.Result{Requeue: true}, nil
	}

	if len(backend.Status.DriveAssignments) != len(c.drives.Items) {
		err := pretty.Errorf("drive assignments mismatch")
		logger.Error(err, "drive assignments mismatch", "expected", len(c.drives.Items), "actual", len(backend.Status.DriveAssignments))
		return ctrl.Result{Requeue: true}, err
	}

	return ctrl.Result{}, nil
}
