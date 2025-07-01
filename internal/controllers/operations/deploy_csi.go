package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/util"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/operations/csi"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	util2 "github.com/weka/weka-operator/pkg/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	Csi_Controller_Suffix = "-csi-controller"
)

type DeployCsiOperation struct {
	client        client.Client
	results       DeployCsiResult
	wekaClient    *v1alpha1.WekaClient
	namespace     string
	csiGroupName  string
	csiDriverName string
	undeploy      bool
	nodes         []corev1.Node
}

type DeployCsiResult struct {
	Err    string `json:"err,omitempty"`
	Result string `json:"result"`
}

func NewDeployCsiOperation(mgr ctrl.Manager, targetClient *v1alpha1.WekaClient, csiDriverName string, nodes []corev1.Node, undeploy bool) *DeployCsiOperation {
	csiGroupName := strings.TrimSuffix(csiDriverName, ".weka.io")
	namespace, _ := util2.GetPodNamespace()

	return &DeployCsiOperation{
		client:        mgr.GetClient(),
		wekaClient:    targetClient,
		csiDriverName: csiDriverName,
		csiGroupName:  csiGroupName,
		namespace:     namespace,
		undeploy:      undeploy,
		nodes:         nodes,
	}
}

func (o *DeployCsiOperation) AsStep() lifecycle.Step {
	return lifecycle.Step{
		Name: "DeployCsi",
		Run:  AsRunFunc(o),
	}
}

func (o *DeployCsiOperation) GetSteps() []lifecycle.Step {
	deploySteps := []lifecycle.Step{
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
	}
	undeploySteps := []lifecycle.Step{
		{
			Name: "UndeployCsiDriver",
			Run:  o.undeployCsiDriver,
		},
		{
			Name: "UndeployStorageClasses",
			Run:  o.undeployStorageClasses,
		},
		{
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
	return nil
}

func (o *DeployCsiOperation) undeployCsiDriver(ctx context.Context) error {
	if err := o.client.Delete(ctx, &storagev1.CSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name: o.csiDriverName,
		},
	}); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete CSI driver %s: %w", o.csiDriverName, err)
		}
	}
	return nil
}

func (o *DeployCsiOperation) deployStorageClasses(ctx context.Context) error {
	fileSystemName := config.Consts.CsiFileSystemName
	storageClassName := csi.GenerateStorageClassName(o.csiGroupName, fileSystemName)
	if err := o.createIfNotExists(ctx, client.ObjectKey{Name: storageClassName},
		func() client.Object {
			return csi.NewCsiStorageClass(o.getSecretName(), o.csiDriverName, storageClassName, fileSystemName)
		}); err != nil {
		return err
	}

	mountOptions := []string{"forcedirect"}
	storageClassForceDirectName := csi.GenerateStorageClassName(o.csiGroupName, fileSystemName, mountOptions...)
	if err := o.createIfNotExists(ctx, client.ObjectKey{Name: storageClassForceDirectName},
		func() client.Object {
			return csi.NewCsiStorageClass(o.getSecretName(), o.csiDriverName,
				storageClassForceDirectName, fileSystemName, mountOptions...)
		}); err != nil {
		return err
	}

	return nil
}

func (o *DeployCsiOperation) undeployStorageClasses(ctx context.Context) error {
	fileSystemName := config.Consts.CsiFileSystemName
	storageClassName := csi.GenerateStorageClassName(o.csiGroupName, fileSystemName)
	if err := o.client.Delete(ctx, &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: storageClassName,
		},
	}); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete storage class %s: %w", storageClassName, err)
		}
	}

	mountOptions := []string{"forcedirect"}
	storageClassForceDirectName := csi.GenerateStorageClassName(o.csiGroupName, fileSystemName, mountOptions...)
	if err := o.client.Delete(ctx, &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: storageClassForceDirectName,
		},
	}); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete storage class %s: %w", storageClassName, err)
		}
	}
	return nil
}

func (o *DeployCsiOperation) deployCsiController(ctx context.Context) error {
	controllerDeploymentName := o.csiGroupName + Csi_Controller_Suffix
	tolerations := util.ExpandTolerations([]corev1.Toleration{}, o.wekaClient.Spec.Tolerations, o.wekaClient.Spec.RawTolerations)
	if err := o.createIfNotExists(ctx, client.ObjectKey{Name: controllerDeploymentName, Namespace: o.namespace},
		func() client.Object {
			return csi.NewCsiControllerDeployment(controllerDeploymentName, o.namespace,
				o.csiDriverName, o.wekaClient.Spec.NodeSelector, tolerations,
			)
		}); err != nil {
		return err
	}

	return o.client.Update(ctx, o.wekaClient)
}

func (o *DeployCsiOperation) undeployCsiController(ctx context.Context) error {
	controllerDeploymentName := o.csiGroupName + Csi_Controller_Suffix
	if err := o.client.Delete(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      controllerDeploymentName,
			Namespace: o.namespace,
		},
	}); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete CSI controller deployment %s: %w", controllerDeploymentName, err)
		}
	}
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

func (o *DeployCsiOperation) getSecretName() string {
	emptyRef := v1alpha1.ObjectReference{}
	if o.wekaClient.Spec.TargetCluster != emptyRef && o.wekaClient.Spec.TargetCluster.Name != "" {
		return fmt.Sprintf("weka-csi-%s", o.wekaClient.Spec.TargetCluster.Name)
	}
	return fmt.Sprintf("weka-csi-%s", o.csiGroupName)
}

func getCsiTopologyLabelKeys(csiDriverName string) (nodeLabel, transportLabel, accessibleLabel string) {
	return fmt.Sprintf("topology.%s/node", csiDriverName),
		fmt.Sprintf("topology.%s/transport", csiDriverName),
		fmt.Sprintf("topology.%s/accessible", csiDriverName)
}

func CheckCsiNodeTopologyLabelsSet(ctx context.Context, node corev1.Node, csiDriverName string) (bool, error) {
	labels := node.Labels
	if labels == nil {
		return false, nil
	}

	nodeLabel, transportLabel, accessibleLabel := getCsiTopologyLabelKeys(csiDriverName)
	_, hasNodeLabel := labels[nodeLabel]
	_, hasTransportLabel := labels[transportLabel]
	_, hasAccessibleLabel := labels[accessibleLabel]

	return hasNodeLabel && hasTransportLabel && hasAccessibleLabel, nil
}

func SetCsiNodeTopologyLabels(ctx context.Context, client client.Client, node corev1.Node, csiDriverName string) error {
	labels := node.Labels
	if labels == nil {
		labels = make(map[string]string)
	}

	nodeLabel, transportLabel, accessibleLabel := getCsiTopologyLabelKeys(csiDriverName)
	labels[nodeLabel] = node.Name
	labels[transportLabel] = "wekafs"
	labels[accessibleLabel] = "true"

	node.Labels = labels

	if err := client.Update(ctx, &node); err != nil {
		return fmt.Errorf("failed to update node %s with topology labels: %w", node.Name, err)
	}

	return nil
}

func UnsetCsiNodeTopologyLabels(ctx context.Context, client client.Client, node corev1.Node, csiDriverName string) error {
	labels := node.Labels
	if labels == nil {
		return nil
	}

	nodeLabel, transportLabel, accessibleLabel := getCsiTopologyLabelKeys(csiDriverName)
	delete(labels, nodeLabel)
	delete(labels, transportLabel)
	delete(labels, accessibleLabel)

	node.Labels = labels

	if err := client.Update(ctx, &node); err != nil {
		return fmt.Errorf("failed to update node %s with topology labels: %w", node.Name, err)
	}

	return nil
}
