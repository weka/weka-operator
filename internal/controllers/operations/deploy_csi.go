package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/operations/csi"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	util2 "github.com/weka/weka-operator/pkg/util"
	v1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type DeployCsiOperation struct {
	client        client.Client
	results       DeployCsiResult
	wekaClient    *v1alpha1.WekaClient
	namespace     string
	csiBaseName   string
	csiDriverName string
}

type DeployCsiResult struct {
	Err    string `json:"err,omitempty"`
	Result string `json:"result"`
}

func NewDeployCsiOperation(mgr ctrl.Manager, targetClient *v1alpha1.WekaClient, csiDriverName string) *DeployCsiOperation {

	csiBaseName := csi.GetBaseNameFromDriverName(csiDriverName)

	namespace, _ := util2.GetPodNamespace()

	return &DeployCsiOperation{
		client:        mgr.GetClient(),
		wekaClient:    targetClient,
		csiDriverName: csiDriverName,
		csiBaseName:   csiBaseName,
		namespace:     namespace,
	}
}

func (o *DeployCsiOperation) AsStep() lifecycle.Step {
	return lifecycle.Step{
		Name: "DeployCsi",
		Run:  AsRunFunc(o),
	}
}

func (o *DeployCsiOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		{
			Name: "DeployCsiDriver",
			Run:  o.deployCsiDriver,
		},
		{
			Name: "DeployStorageClasses",
			Run:  o.deployStorageClasses,
		},
		{
			Name: "DeployCsiController",
			Run:  o.deployCsiController,
		},
		{
			Name: "UpdateCsiController",
			Run:  o.updateCsiController,
		},
	}
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
		func() client.Object { return csi.NewCSIDriver(o.csiDriverName) }); err != nil {
		return err
	}
	return nil
}

func (o *DeployCsiOperation) deployStorageClasses(ctx context.Context) error {
	fileSystemName := config.Consts.CSIFileSystemName
	storageClassName := csi.GenerateStorageClassName(o.csiDriverName, fileSystemName)
	if err := o.createIfNotExists(ctx, client.ObjectKey{Name: storageClassName},
		func() client.Object {
			return csi.NewCSIStorageClass(o.csiBaseName, o.csiDriverName, storageClassName, fileSystemName)
		}); err != nil {
		return err
	}

	mountOptions := []string{"forcedirect"}
	storageClassForceDirectName := csi.GenerateStorageClassName(o.csiDriverName, fileSystemName, mountOptions...)
	if err := o.createIfNotExists(ctx, client.ObjectKey{Name: storageClassForceDirectName},
		func() client.Object {
			return csi.NewCSIStorageClass(o.csiBaseName, o.csiDriverName,
				storageClassForceDirectName, fileSystemName, mountOptions...)
		}); err != nil {
		return err
	}

	return nil
}

func (o *DeployCsiOperation) deployCsiController(ctx context.Context) error {
	tolerations := tolerationsToObj(o.wekaClient.Spec.Tolerations)
	controllerDeploymentName := o.csiBaseName + "-csi-controller"
	if err := o.createIfNotExists(ctx, client.ObjectKey{Name: controllerDeploymentName, Namespace: o.namespace},
		func() client.Object {
			return csi.NewCSIControllerDeployment(controllerDeploymentName, o.namespace,
				o.csiDriverName, tolerations)
		}); err != nil {
		return err
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

func tolerationsToObj(tolerations []string) []v1.Toleration {
	tolerationObjects := make([]v1.Toleration, len(tolerations))
	for i, toleration := range tolerations {
		tolerationObjects[i] = v1.Toleration{
			Effect:   v1.TaintEffectNoSchedule,
			Key:      toleration,
			Operator: v1.TolerationOpExists,
		}
	}
	return tolerationObjects
}
