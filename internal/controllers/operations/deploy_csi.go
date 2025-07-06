package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/weka/go-weka-observability/instrumentation"

	"github.com/weka/weka-k8s-api/api/v1alpha1"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/operations/csi"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	util2 "github.com/weka/weka-operator/pkg/util"
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

func NewDeployCsiOperation(client client.Client, targetClient *v1alpha1.WekaClient, csiGroupName string, nodes []corev1.Node, undeploy bool) *DeployCsiOperation {
	namespace, _ := util2.GetPodNamespace()

	return &DeployCsiOperation{
		client:        client,
		wekaClient:    targetClient,
		csiDriverName: csi.GetCsiDriverName(csiGroupName),
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
			Predicates: lifecycle.Predicates{
				o.shouldDeployCSIDriver,
			},
			ContinueOnPredicatesFalse: true,
		},
		{
			Name: "DeployStorageClasses",
			Run:  o.deployStorageClasses,
			Predicates: lifecycle.Predicates{
				lifecycle.BoolValue(!config.Config.CsiStorageClassCreationDisabled),
				func() bool {
					emptyRef := weka.ObjectReference{}
					return o.wekaClient.Spec.TargetCluster != emptyRef && o.wekaClient.Spec.TargetCluster.Name != ""
				},
				o.shouldDeployStorageClass,
			},
			ContinueOnPredicatesFalse: true,
		},
		{
			Name: "DeployCsiController",
			Run:  o.deployCsiController,
			Predicates: lifecycle.Predicates{
				lifecycle.BoolValue(o.wekaClient.Spec.CsiConfig == nil || !o.wekaClient.Spec.CsiConfig.DisableControllerCreation),
				o.shouldDeployCSIController,
			},
			ContinueOnPredicatesFalse: true,
		},
	}
	undeploySteps := []lifecycle.Step{
		{
			Name:                      "UndeployCsiDriver",
			Run:                       o.undeployCsiDriver,
			ContinueOnPredicatesFalse: true,
		},
		{
			Name: "UndeployStorageClasses",
			Run:  o.undeployStorageClasses,
			Predicates: lifecycle.Predicates{
				lifecycle.BoolValue(!config.Config.CsiStorageClassCreationDisabled),
				func() bool {
					emptyRef := weka.ObjectReference{}
					return o.wekaClient.Spec.TargetCluster != emptyRef && o.wekaClient.Spec.TargetCluster.Name != ""
				},
			},
			ContinueOnPredicatesFalse: true,
		},
		{
			Name: "UndeployCsiController",
			Run:  o.undeployCsiController,
			Predicates: lifecycle.Predicates{
				lifecycle.BoolValue(o.wekaClient.Spec.CsiConfig == nil || !o.wekaClient.Spec.CsiConfig.DisableControllerCreation),
			},
			ContinueOnPredicatesFalse: true,
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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "deployCsiDriver")
	defer end()

	err := o.client.Create(ctx, csi.NewCsiDriver(o.csiDriverName))
	if err != nil {
		return fmt.Errorf("failed to create CSI driver: %w", err)
	}
	logger.Info("Created CSI driver successfully")
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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "deployStorageClasses")
	defer end()

	fileSystemName := config.Consts.CsiFileSystemName
	storageClassName := csi.GenerateStorageClassName(o.csiGroupName, fileSystemName)

	// Deploy default storage class
	err := o.client.Create(ctx, csi.NewCsiStorageClass(
		o.getCsiSecret(),
		o.csiDriverName,
		storageClassName,
		fileSystemName,
	))
	if err != nil {
		return fmt.Errorf("failed to create default storage class: %w", err)
	}
	logger.Info("Created default storage class successfully")

	// Deploy force direct storage class
	mountOptions := []string{"forcedirect"}
	storageClassForceDirectName := csi.GenerateStorageClassName(o.csiGroupName, fileSystemName, mountOptions...)

	err = o.client.Create(ctx, csi.NewCsiStorageClass(
		o.getCsiSecret(),
		o.csiDriverName,
		storageClassForceDirectName,
		fileSystemName,
		mountOptions...,
	))
	if err != nil {
		return fmt.Errorf("failed to create force direct storage class: %w", err)
	}
	logger.Info("Created force direct storage class successfully")
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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "deployCsiController")
	defer end()

	deploymentSpec := csi.NewCsiControllerDeployment(o.csiGroupName, o.wekaClient)

	err := o.client.Create(ctx, deploymentSpec)
	if err != nil {
		return fmt.Errorf("failed to create CSI controller deployment: %w", err)
	}
	logger.Info("Created CSI controller deployment successfully")

	return nil
}

func (o *DeployCsiOperation) undeployCsiController(ctx context.Context) error {
	controllerDeploymentName := csi.GetCSIControllerName(o.csiGroupName)
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

func (o *DeployCsiOperation) shouldDeployCSIDriver() bool {
	ctx, logger, end := instrumentation.GetLogSpan(context.Background(), "shouldDeployCSIDriver")
	defer end()

	listOpts := []client.ListOption{
		client.MatchingLabels(csi.GetCsiLabels(o.csiDriverName, csi.CSIDriver, nil, nil)),
	}
	var existingList storagev1.CSIDriverList
	if err := o.client.List(ctx, &existingList, listOpts...); err != nil {
		logger.Error(err, "failed to list existing CSIDrivers")
		return false
	}
	if len(existingList.Items) > 0 {
		return false
	}
	return true
}

func (o *DeployCsiOperation) shouldDeployStorageClass() bool {
	ctx, logger, end := instrumentation.GetLogSpan(context.Background(), "shouldDeployStorageClass")
	defer end()

	listOpts := []client.ListOption{
		client.MatchingLabels(csi.GetCsiLabels(o.csiDriverName, csi.CSIStorageclass, nil, nil)),
	}
	var existingList storagev1.StorageClassList
	if err := o.client.List(ctx, &existingList, listOpts...); err != nil {
		logger.Error(err, "failed to list existing StorageClasses")
		return false
	}
	if len(existingList.Items) > 0 {
		return false
	}
	return true
}

func (o *DeployCsiOperation) shouldDeployCSIController() bool {
	ctx, logger, end := instrumentation.GetLogSpan(context.Background(), "shouldDeployCSIController")
	defer end()

	listOpts := []client.ListOption{
		client.MatchingLabels(csi.GetCsiLabels(o.csiDriverName, csi.CSIController, nil, nil)),
	}
	var existingList appsv1.DeploymentList
	if err := o.client.List(ctx, &existingList, listOpts...); err != nil {
		logger.Error(err, "failed to list existing Deployments")
		return false
	}
	if len(existingList.Items) > 0 {
		return false
	}
	return true
}

func (o *DeployCsiOperation) getCsiSecret() client.ObjectKey {
	emptyRef := v1alpha1.ObjectReference{}
	if o.wekaClient.Spec.TargetCluster != emptyRef && o.wekaClient.Spec.TargetCluster.Name != "" {
		name := fmt.Sprintf("weka-csi-%s", o.wekaClient.Spec.TargetCluster.Name)
		return client.ObjectKey{
			Name:      name,
			Namespace: o.wekaClient.Spec.TargetCluster.Namespace,
		}
	}
	name := fmt.Sprintf("weka-csi-%s", o.csiGroupName)
	return client.ObjectKey{
		Name:      name,
		Namespace: o.wekaClient.Namespace,
	}
}

func getCsiTopologyLabelKeys(csiDriverName string) (nodeLabel, transportLabel, accessibleLabel string) {
	return fmt.Sprintf("topology.%s/node", csiDriverName),
		fmt.Sprintf("topology.%s/transport", csiDriverName),
		fmt.Sprintf("topology.%s/accessible", csiDriverName)
}

func CheckCsiNodeTopologyLabelsSet(node corev1.Node, csiDriverName string) (bool, error) {
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
