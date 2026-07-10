package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/operations/csi"
	"github.com/weka/weka-operator/pkg/util"
)

type DeployCsiOperation struct {
	client        client.Client
	results       DeployCsiResult
	wekaClient    *weka.WekaClient
	namespace     string
	csiGroupName  string
	csiDriverName string
	undeploy      bool
	// existing resources
	csiDriverExists     bool
	storageClassesExist bool
	csiControllerExists bool
}

type DeployCsiResult struct {
	Err    string `json:"err,omitempty"`
	Result string `json:"result"`
}

func NewDeployCsiOperation(k8sClient client.Client, targetClient *weka.WekaClient, csiGroupName string, undeploy bool) (*DeployCsiOperation, error) {
	namespace, err := util.GetPodNamespace()
	if err != nil {
		return nil, err
	}

	return &DeployCsiOperation{
		client:        k8sClient,
		wekaClient:    targetClient,
		csiDriverName: csi.GetCsiDriverName(csiGroupName),
		csiGroupName:  csiGroupName,
		namespace:     namespace,
		undeploy:      undeploy,
	}, nil
}

func (o *DeployCsiOperation) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "DeployCsi",
		Run:  AsRunFunc(o),
	}
}

func (o *DeployCsiOperation) GetSteps() []lifecycle.Step {
	deploySteps := []lifecycle.Step{
		&lifecycle.SimpleStep{
			Name: "GetExistingCsiResources",
			Run:  o.getExistingCsiResources,
		},
		&lifecycle.SimpleStep{
			Name: "DeployCsiDriver",
			Run:  o.deployCsiDriver,
			Predicates: lifecycle.Predicates{
				func() bool { return !o.csiDriverExists },
			}},
		&lifecycle.SimpleStep{
			Name: "DeployStorageClasses",
			Run:  o.deployStorageClasses,
			Predicates: lifecycle.Predicates{
				lifecycle.BoolValue(!config.Config.Csi.StorageClassCreationDisabled),
				func() bool {
					emptyRef := weka.ObjectReference{}
					return o.wekaClient.Spec.TargetCluster != emptyRef && o.wekaClient.Spec.TargetCluster.Name != ""
				},
				func() bool { return !o.storageClassesExist },
			}},
		&lifecycle.SimpleStep{
			Name: "DeployCsiController",
			Run:  o.deployCsiController,
			Predicates: lifecycle.Predicates{
				lifecycle.BoolValue(o.wekaClient.Spec.CsiConfig == nil || !o.wekaClient.Spec.CsiConfig.DisableControllerCreation),
				func() bool { return !o.csiControllerExists },
			}},
		&lifecycle.SimpleStep{
			Name: "CleanupLegacyCsiNodePods",
			Run:  o.cleanupLegacyCsiNodePods,
		},
		&lifecycle.SimpleStep{
			Name: "CleanupOldSharedCsiNodeDaemonSet",
			Run:  o.cleanupOldSharedCsiNodeDaemonSet,
		},
	}
	undeploySteps := []lifecycle.Step{
		&lifecycle.SimpleStep{
			Name: "UndeployCsiDriver",
			Run:  o.undeployCsiDriver},
		&lifecycle.SimpleStep{
			Name: "UndeployStorageClasses",
			Run:  o.undeployStorageClasses,
			Predicates: lifecycle.Predicates{
				lifecycle.BoolValue(!config.Config.Csi.StorageClassCreationDisabled),
				func() bool {
					emptyRef := weka.ObjectReference{}
					return o.wekaClient.Spec.TargetCluster != emptyRef && o.wekaClient.Spec.TargetCluster.Name != ""
				},
			}},
		&lifecycle.SimpleStep{
			Name: "UndeployCsiController",
			Run:  o.undeployCsiController,
			Predicates: lifecycle.Predicates{
				lifecycle.BoolValue(o.wekaClient.Spec.CsiConfig == nil || !o.wekaClient.Spec.CsiConfig.DisableControllerCreation),
			}},
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
	resultJSON, _ := json.Marshal(o.results) //nolint:errcheck // marshal of known-serializable struct; error not possible
	return string(resultJSON)
}

func (o *DeployCsiOperation) deployCsiDriver(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "deployCsiDriver")
	defer logger.End()

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
	ctx, logger := instrumentation.CreateLogSpan(ctx, "deployStorageClasses")
	defer logger.End()

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
	ctx, logger := instrumentation.CreateLogSpan(ctx, "deployCsiController")
	defer logger.End()

	deploymentSpec, err := csi.NewCsiControllerDeployment(ctx, o.csiGroupName, o.wekaClient)
	if err != nil {
		return err
	}

	operatorDeployment, err := util.GetOperatorDeployment(ctx, o.client)
	if err != nil {
		return errors.Wrap(err, "failed to get operator deployment")
	}

	// set owner reference to the operator deployment
	err = controllerutil.SetControllerReference(operatorDeployment, deploymentSpec, o.client.Scheme())
	if err != nil {
		return err
	}

	err = o.client.Create(ctx, deploymentSpec)
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

func (o *DeployCsiOperation) getExistingCsiResources(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "getExistingCsiResources")
	defer logger.End()

	// Get existing CSIDriver
	listOpts := []client.ListOption{
		client.MatchingLabels(csi.GetCsiLabels(o.csiDriverName, csi.CSIDriver, nil, nil)),
	}
	var existingList storagev1.CSIDriverList
	if err := o.client.List(ctx, &existingList, listOpts...); err != nil {
		logger.Error(err, "failed to list existing CSIDrivers")
		return err
	}
	if len(existingList.Items) > 0 {
		o.csiDriverExists = true
		logger.Debug("Found existing CSIDriver", "name", existingList.Items[0].Name)
	}

	// Get existing StorageClasses
	listOpts = []client.ListOption{
		client.MatchingLabels(csi.GetCsiLabels(o.csiDriverName, csi.CSIStorageclass, nil, nil)),
	}
	var existingScList storagev1.StorageClassList
	if err := o.client.List(ctx, &existingScList, listOpts...); err != nil {
		logger.Error(err, "failed to list existing StorageClasses")
		return err
	}
	if len(existingScList.Items) > 0 {
		o.storageClassesExist = true
		var scNames []string
		for i := range existingScList.Items {
			scNames = append(scNames, existingScList.Items[i].Name)
		}
		logger.Debug("Found existing StorageClasses", "names", scNames)
	}

	// Get existing CSI Controller Deployment
	listOpts = []client.ListOption{
		client.MatchingLabels(csi.GetCsiLabels(o.csiDriverName, csi.CSIController, nil, nil)),
	}
	var existingDepList appsv1.DeploymentList
	if err := o.client.List(ctx, &existingDepList, listOpts...); err != nil {
		logger.Error(err, "failed to list existing Deployments")
		return err
	}
	if len(existingDepList.Items) > 0 {
		o.csiControllerExists = true
		logger.Debug("Found existing CSI Controller Deployment", "name", existingDepList.Items[0].Name)
	}

	return nil
}

func (o *DeployCsiOperation) getCsiSecret() client.ObjectKey {
	emptyRef := weka.ObjectReference{}
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

type CsiTopologyLabelsService struct {
	nodeName  string
	container *weka.WekaContainer

	nodeLabelKey       string
	transportLabelKey  string
	accessibleLabelKey string

	expectedLabels map[string]string
}

func NewCsiTopologyLabelsService(csiDriverName, nodeName string, container *weka.WekaContainer) *CsiTopologyLabelsService {
	return &CsiTopologyLabelsService{
		nodeName:  nodeName,
		container: container,
		// get the label keys for the given CSI driver name
		nodeLabelKey:       fmt.Sprintf("topology.%s/node", csiDriverName),
		transportLabelKey:  fmt.Sprintf("topology.%s/transport", csiDriverName),
		accessibleLabelKey: fmt.Sprintf("topology.%s/accessible", csiDriverName),
	}
}

func (s *CsiTopologyLabelsService) GetExpectedCsiTopologyLabels() map[string]string {
	if s.expectedLabels != nil {
		return s.expectedLabels
	}

	labels := make(map[string]string)

	if s.nodeIsCsiAccessible() {
		labels[s.accessibleLabelKey] = "true"
		labels[s.nodeLabelKey] = s.nodeName
		labels[s.transportLabelKey] = "wekafs"
	}

	s.expectedLabels = labels

	return labels
}

func (s *CsiTopologyLabelsService) UpdateNodeLabels(node *corev1.Node, expectedLabels map[string]string) *corev1.Node {
	// copy expected labels
	maps.Copy(node.Labels, expectedLabels)

	// remove unexpected labels
	for key := range node.Labels {
		if key == s.nodeLabelKey || key == s.transportLabelKey || key == s.accessibleLabelKey {
			if _, exists := expectedLabels[key]; !exists {
				delete(node.Labels, key)
			}
		}
	}

	return node
}

func (s *CsiTopologyLabelsService) NodeHasExpectedCsiTopologyLabels(node *corev1.Node) bool {
	expectedLabels := s.GetExpectedCsiTopologyLabels()

	// check if node has no expected label key or has wrong value
	for key, expectedValue := range expectedLabels {
		actualValue, exists := node.Labels[key]
		if !exists || actualValue != expectedValue {
			return false
		}
	}

	// check if node has any unexpected label key
	for key := range node.Labels {
		if key == s.nodeLabelKey || key == s.transportLabelKey || key == s.accessibleLabelKey {
			if _, exists := expectedLabels[key]; !exists {
				return false
			}
		}
	}

	return true
}

func (s *CsiTopologyLabelsService) nodeIsCsiAccessible() bool {
	container := s.container

	if config.Config.Csi.PreventNewWorkloadOnClientContainerNotRunning && container.Status.Status != weka.Running {
		return false
	}

	// we should have stats and they should be recent
	if container.Status.Stats == nil || container.Status.Stats.LastUpdate.IsZero() {
		return false
	}

	processes := container.Status.Stats.Processes
	allProcessesActive := processes.Active > 0 && processes.Active == processes.Desired

	return allProcessesActive && time.Since(container.Status.Stats.LastUpdate.Time) < 5*time.Minute
}

// isDaemonSetRolledOut reports whether every desired pod of the DaemonSet has
// been updated to the current generation and is available — mirroring
// `kubectl rollout status daemonset`. A DaemonSet whose selector matches no
// nodes (DesiredNumberScheduled == 0) is treated as rolled out so it does not
// block cleanup forever.
func isDaemonSetRolledOut(ds *appsv1.DaemonSet) bool {
	if ds.Status.ObservedGeneration < ds.Generation {
		return false
	}
	if ds.Status.UpdatedNumberScheduled != ds.Status.DesiredNumberScheduled {
		return false
	}
	if ds.Status.NumberAvailable != ds.Status.DesiredNumberScheduled {
		return false
	}
	return true
}

// cleanupOldSharedCsiNodeDaemonSet removes the old shared CSI node DaemonSet
// that was deployed per CSI group (before the per-client migration).
// Deletion is deferred until every WekaClient in this CSI group has its own
// per-client node DaemonSet deployed, preventing sibling clients from losing
// the CSI node plugin during a rolling upgrade.
func (o *DeployCsiOperation) cleanupOldSharedCsiNodeDaemonSet(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "cleanupOldSharedCsiNodeDaemonSet")
	defer logger.End()

	oldName := csi.GetCSINodeDaemonSetName(o.csiGroupName)
	existing := &appsv1.DaemonSet{}
	err := o.client.Get(ctx, client.ObjectKey{
		Name:      oldName,
		Namespace: o.namespace,
	}, existing)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil // already cleaned up
		}
		return fmt.Errorf("failed to get old shared CSI node daemonset: %w", err)
	}

	// Only clean up the old shared DaemonSet once every WekaClient in this CSI
	// group has its own per-client DaemonSet deployed, to avoid a gap where
	// sibling clients lose CSI node plugin coverage during upgrades.
	allClients := &weka.WekaClientList{}
	if err := o.client.List(ctx, allClients); err != nil {
		return fmt.Errorf("failed to list WekaClients: %w", err)
	}

	for i := range allClients.Items {
		sibling := &allClients.Items[i]
		if sibling.DeletionTimestamp != nil {
			// Being deleted — it will never carry a per-client DaemonSet.
			continue
		}

		siblingGroup, err := o.resolveClientCsiGroup(ctx, sibling)
		if err != nil {
			if apierrors.IsNotFound(err) {
				// Target cluster is gone — this sibling can no longer belong to
				// this CSI group, so skip it rather than blocking cleanup forever.
				continue
			}
			return fmt.Errorf("failed to resolve CSI group for client %s/%s: %w", sibling.Namespace, sibling.Name, err)
		}
		if siblingGroup != o.csiGroupName {
			continue
		}

		perClientName := csi.GetCSINodeDaemonSetNameForClient(o.csiGroupName, sibling.Name, sibling.Namespace)
		perClientDS := &appsv1.DaemonSet{}
		err = o.client.Get(ctx, client.ObjectKey{
			Name:      perClientName,
			Namespace: o.namespace,
		}, perClientDS)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.Debug("Deferring shared CSI node daemonset cleanup: sibling per-client daemonset not yet deployed",
					"siblingClient", sibling.Name, "oldName", oldName)
				return nil
			}
			return fmt.Errorf("failed to check per-client CSI node daemonset for client %s/%s: %w", sibling.Namespace, sibling.Name, err)
		}
		if !isDaemonSetRolledOut(perClientDS) {
			logger.Debug("Deferring shared CSI node daemonset cleanup: sibling per-client daemonset not yet fully rolled out",
				"siblingClient", sibling.Name, "oldName", oldName,
				"desired", perClientDS.Status.DesiredNumberScheduled,
				"updated", perClientDS.Status.UpdatedNumberScheduled,
				"available", perClientDS.Status.NumberAvailable)
			return nil
		}
	}

	logger.Info("Cleaning up old shared CSI node daemonset", "name", oldName)
	if err := o.client.Delete(ctx, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete old shared CSI node daemonset %s: %w", oldName, err)
		}
	}
	logger.Info("Deleted old shared CSI node daemonset successfully", "name", oldName)
	return nil
}

// cleanupLegacyCsiNodePods removes old CSI node pods that were created before
// migrating to DaemonSet. This is needed for upgrade scenarios.
func (o *DeployCsiOperation) cleanupLegacyCsiNodePods(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "cleanupLegacyCsiNodePods")
	defer logger.End()

	// List all pods with CSI node labels for this driver
	podList := &corev1.PodList{}
	labelSelector := client.MatchingLabels{
		"weka.io/csi-driver-name": o.csiDriverName,
		"weka.io/mode":            string(csi.CSINode),
	}

	err := o.client.List(ctx, podList, labelSelector, client.InNamespace(o.namespace))
	if err != nil {
		return fmt.Errorf("failed to list CSI node pods: %w", err)
	}

	// Filter legacy pods (those NOT owned by a DaemonSet)
	var legacyPods []corev1.Pod
	for i := range podList.Items {
		isOwnedByDaemonSet := false
		for _, ownerRef := range podList.Items[i].OwnerReferences {
			if ownerRef.Kind == "DaemonSet" {
				isOwnedByDaemonSet = true
				break
			}
		}
		if !isOwnedByDaemonSet {
			legacyPods = append(legacyPods, podList.Items[i])
		}
	}

	if len(legacyPods) == 0 {
		logger.Info("No legacy CSI node pods found, skipping cleanup")
		return nil
	}

	logger.Info("Found legacy CSI node pods to clean up", "count", len(legacyPods))

	// Delete legacy pods
	for i := range legacyPods {
		logger.Info("Deleting legacy CSI node pod", "pod", legacyPods[i].Name, "node", legacyPods[i].Spec.NodeName)
		err := o.client.Delete(ctx, &legacyPods[i], client.PropagationPolicy(metav1.DeletePropagationForeground))
		if err != nil && !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to delete legacy CSI node pod", "pod", legacyPods[i].Name)
			return fmt.Errorf("failed to delete legacy CSI node pod %s: %w", legacyPods[i].Name, err)
		}
	}

	// Wait for pods to be deleted (with timeout)
	timeout := time.After(2 * time.Minute)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for legacy CSI node pods to be deleted")
		case <-ticker.C:
			podList := &corev1.PodList{}
			err := o.client.List(ctx, podList, labelSelector, client.InNamespace(o.namespace))
			if err != nil {
				return fmt.Errorf("failed to list CSI node pods: %w", err)
			}

			// Check if any legacy pods still exist
			remainingLegacyPods := 0
			for j := range podList.Items {
				isOwnedByDaemonSet := false
				for _, ownerRef := range podList.Items[j].OwnerReferences {
					if ownerRef.Kind == "DaemonSet" {
						isOwnedByDaemonSet = true
						break
					}
				}
				if !isOwnedByDaemonSet {
					remainingLegacyPods++
				}
			}

			if remainingLegacyPods == 0 {
				logger.Info("All legacy CSI node pods have been cleaned up successfully")
				return nil
			}
			logger.Info("Waiting for legacy CSI node pods to be deleted", "remaining", remainingLegacyPods)
		}
	}
}

// resolveClientCsiGroup resolves the CSI group for an arbitrary client,
// fetching the referenced target cluster when one is set. The group choice
// itself lives in csi.ResolveGroup.
func (o *DeployCsiOperation) resolveClientCsiGroup(ctx context.Context, wc *weka.WekaClient) (string, error) {
	var cluster *weka.WekaCluster
	if ref := wc.Spec.TargetCluster; ref.Name != "" {
		// Use the target cluster namespace as-is, matching the other
		// target-cluster reads in this repo (FetchTargetCluster, getCsiSecret).
		// Defaulting an empty namespace here would diverge from those lookups
		// and could resolve the wrong CSI group during cleanup.
		cluster = &weka.WekaCluster{}
		if err := o.client.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}, cluster); err != nil {
			return "", err
		}
	}
	return csi.ResolveGroup(cluster, wc), nil
}
