package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/operations/csi"
	util2 "github.com/weka/weka-operator/pkg/util"
)

type DeployCsiOperation struct {
	client        client.Client
	results       DeployCsiResult
	wekaClient    *v1alpha1.WekaClient
	namespace     string
	csiBaseName   string
	csiDriverName string
	csiSecret     client.ObjectKey
	undeploy      bool
}

type DeployCsiResult struct {
	Err    string                        `json:"err,omitempty"`
	Result v1alpha1.CsiDeploymentOutputs `json:"result,omitempty"`
}

func NewDeployCsiOperation(mgr ctrl.Manager, targetClient *v1alpha1.WekaClient, csiDriverName string, csiSecret client.ObjectKey, undeploy bool) *DeployCsiOperation {
	csiBaseName := csi.GetBaseNameFromDriverName(csiDriverName)
	namespace, _ := util2.GetPodNamespace()

	return &DeployCsiOperation{
		client:        mgr.GetClient(),
		wekaClient:    targetClient,
		csiDriverName: csiDriverName,
		csiSecret:     csiSecret,
		csiBaseName:   csiBaseName,
		namespace:     namespace,
		undeploy:      undeploy,
	}
}

func (o *DeployCsiOperation) AsStep() lifecycle.Step {
	return &lifecycle.SingleStep{
		Name: "DeployCsi",
		Run:  lifecycle.AsRunFunc(o),
	}
}

func (o *DeployCsiOperation) GetSteps() []lifecycle.Step {
	deploySteps := []lifecycle.Step{
		&lifecycle.SingleStep{
			Name: "DeployCsiDriver",
			Run:  o.deployCsiDriver,
		},
		&lifecycle.SingleStep{
			Name: "DeployStorageClasses",
			Run:  o.deployStorageClasses,
		},
		&lifecycle.SingleStep{
			Name: "DeployCsiController",
			Run:  o.deployCsiController,
		},
		&lifecycle.SingleStep{
			Name: "UpdateCsiController",
			Run:  o.updateCsiController,
		},
	}
	undeploySteps := []lifecycle.Step{
		&lifecycle.SingleStep{
			Name: "UndeployCsiDriver",
			Run:  o.undeployCsiDriver,
		},
		&lifecycle.SingleStep{
			Name: "UndeployStorageClasses",
			Run:  o.undeployStorageClasses,
		},
		&lifecycle.SingleStep{
			Name: "UndeployCsiController",
			Run:  o.undeployCsiController,
		},
	}
	if o.undeploy {
		return undeploySteps
	}
	return deploySteps
}

func (o *DeployCsiOperation) GetResult() DeployCsiResult {
	return o.results
}

func (o *DeployCsiOperation) GetJsonResult() string {
	resultJSON, _ := json.Marshal(o.results)
	return string(resultJSON)
}

func (o *DeployCsiOperation) deployCsiDriver(ctx context.Context) error {
	if err := o.createIfNotExists(ctx, client.ObjectKey{Name: o.csiDriverName},
		func() client.Object { return csi.NewCsiDriver(o.csiDriverName) }); err != nil {
		return err
	}
	o.results.Result.CsiDriverName = o.csiDriverName
	return nil
}

func (o *DeployCsiOperation) undeployCsiDriver(ctx context.Context) error {
	if err := o.client.Delete(ctx, &storagev1.CSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name: o.csiDriverName,
		},
	}); err != nil {
		return fmt.Errorf("failed to delete CSI driver %s: %w", o.csiDriverName, err)
	}
	return nil
}

func (o *DeployCsiOperation) deployStorageClasses(ctx context.Context) error {
	fileSystemName := config.Consts.CsiFileSystemName
	storageClassName := csi.GenerateStorageClassName(o.csiDriverName, fileSystemName)
	if err := o.createIfNotExists(ctx, client.ObjectKey{Name: storageClassName},
		func() client.Object {
			return csi.NewCsiStorageClass(o.csiDriverName, storageClassName, fileSystemName, o.csiSecret)
		}); err != nil {
		return err
	}

	if !slices.Contains(o.results.Result.StorageClassesNames, storageClassName) {
		if o.results.Result.StorageClassesNames == nil {
			o.results.Result.StorageClassesNames = []string{storageClassName}
		} else {
			o.results.Result.StorageClassesNames = append(o.results.Result.StorageClassesNames, storageClassName)
		}
	}

	mountOptions := []string{"forcedirect"}
	storageClassForceDirectName := csi.GenerateStorageClassName(o.csiDriverName, fileSystemName, mountOptions...)
	if err := o.createIfNotExists(ctx, client.ObjectKey{Name: storageClassForceDirectName},
		func() client.Object {
			return csi.NewCsiStorageClass(o.csiDriverName,
				storageClassForceDirectName, fileSystemName, o.csiSecret, mountOptions...)
		}); err != nil {
		return err
	}

	if !slices.Contains(o.results.Result.StorageClassesNames, storageClassForceDirectName) {
		o.results.Result.StorageClassesNames = append(o.results.Result.StorageClassesNames, storageClassForceDirectName)
	}

	return nil
}

func (o *DeployCsiOperation) undeployStorageClasses(ctx context.Context) error {
	fileSystemName := config.Consts.CsiFileSystemName
	storageClassName := csi.GenerateStorageClassName(o.csiDriverName, fileSystemName)
	if err := o.client.Delete(ctx, &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: storageClassName,
		},
	}); err != nil {
		return fmt.Errorf("failed to delete CSI storage class %s: %w", storageClassName, err)
	}

	mountOptions := []string{"forcedirect"}
	storageClassForceDirectName := csi.GenerateStorageClassName(o.csiDriverName, fileSystemName, mountOptions...)
	if err := o.client.Delete(ctx, &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: storageClassForceDirectName,
		},
	}); err != nil {
		return fmt.Errorf("failed to delete CSI storage class %s: %w", storageClassForceDirectName, err)
	}

	return nil
}

func (o *DeployCsiOperation) deployCsiController(ctx context.Context) error {
	controllerDeploymentName := o.csiBaseName + "-csi-controller"
	tolerations := util.ExpandTolerations([]corev1.Toleration{}, o.wekaClient.Spec.Tolerations, o.wekaClient.Spec.RawTolerations)
	if err := o.createIfNotExists(ctx, client.ObjectKey{Name: controllerDeploymentName, Namespace: o.namespace},
		func() client.Object {
			return csi.NewCsiControllerDeployment(controllerDeploymentName, o.namespace,
				o.csiDriverName, o.wekaClient.Spec.NodeSelector, tolerations,
			)
		}); err != nil {
		return err
	}

	o.results.Result.CsiControllerName = controllerDeploymentName

	return nil
}

func (o *DeployCsiOperation) undeployCsiController(ctx context.Context) error {
	controllerDeploymentName := o.csiBaseName + "-csi-controller"
	if err := o.client.Delete(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      controllerDeploymentName,
			Namespace: o.namespace,
		},
	}); err != nil {
		return fmt.Errorf("failed to delete CSI controller deployment %s: %w", controllerDeploymentName, err)
	}
	return nil
}

func (o *DeployCsiOperation) updateCsiController(ctx context.Context) error {
	return nil
}

func (o *DeployCsiOperation) createIfNotExists(ctx context.Context, key client.ObjectKey,
	objectFactory func() client.Object) error {

	newObj := objectFactory()
	typeName := fmt.Sprintf("%T", newObj)
	err := o.client.Create(ctx, newObj)
	if client.IgnoreAlreadyExists(err) != nil {
		return fmt.Errorf("failed to create %s %s: %w", typeName, key.Name, err)
	}

	return nil
}
