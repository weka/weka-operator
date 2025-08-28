package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apiserver/pkg/storage/names"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/workers"
)

const (
	DefaultKernelLabelKey       = "weka.io/kernel"
	DefaultArchitectureLabelKey = "weka.io/architecture"
	PolicyNameLabelKey          = "weka.io/policy-name"
	DriverDistServiceSuffix     = "-dist"
	DriverDistContainerSuffix   = "-dist"
	DriversBuilderSuffix        = "builder"
)

type nodeAttributes struct {
	kernelVersion string
	architecture  string
	nodeSelector  map[string]string // Stores the first nodeSelector from payload that matched this node's attributes
}

// StringKey generates a canonical string representation for nodeAttributes, suitable for use as a map key.
func (na nodeAttributes) StringKey() string {
	// Create a slice of keys from the map
	selectorKeys := make([]string, 0, len(na.nodeSelector))
	for k := range na.nodeSelector {
		selectorKeys = append(selectorKeys, k)
	}
	// Sort the keys to ensure consistent order
	sort.Strings(selectorKeys)

	// Build the selector string part
	var selectorParts []string
	for _, k := range selectorKeys {
		selectorParts = append(selectorParts, fmt.Sprintf("%s:%s", k, na.nodeSelector[k]))
	}
	selectorStr := strings.Join(selectorParts, ",")

	// Use a distinct separator for the main parts of the key
	return fmt.Sprintf("kernel=%s|arch=%s|selector=%s", na.kernelVersion, na.architecture, selectorStr)
}

type EnsureDistServiceOperation struct {
	client           client.Client
	scheme           *runtime.Scheme
	payload          *weka.DriverDistPayload
	policy           *weka.WekaPolicy // This is WekaPolicy
	containerDetails weka.WekaOwnerDetails
	ownerStatus      string // Current status of the WekaPolicy (e.g., "Running", "Done")
	successCallback  lifecycle.StepFunc
	failureCallback  lifecycle.StepFunc
	mgr              ctrl.Manager
	kubeService      kubernetes.KubeService

	// Internal state
	distContainerName   string                    // Name of the drivers-dist container
	discoveredNodesAttr map[string]nodeAttributes // nodeName -> attributes
	discoveredImages    map[string]bool           // imageName -> true
	targetKernelArchs   map[string]nodeAttributes // unique kernel/arch pairs, key is na.StringKey()
	mutex               sync.Mutex
}

func NewEnsureDistServiceOperation(
	mgr ctrl.Manager,
	payload *weka.DriverDistPayload,
	policy *weka.WekaPolicy,
	containerDetails weka.WekaOwnerDetails,
	ownerStatus string,
	successCallback lifecycle.StepFunc,
	failureCallback lifecycle.StepFunc,
) *EnsureDistServiceOperation {
	return &EnsureDistServiceOperation{
		client:              mgr.GetClient(),
		scheme:              mgr.GetScheme(),
		mgr:                 mgr,
		payload:             payload,
		policy:              policy,
		containerDetails:    containerDetails,
		ownerStatus:         ownerStatus,
		successCallback:     successCallback,
		failureCallback:     failureCallback,
		kubeService:         kubernetes.NewKubeService(mgr.GetClient()),
		discoveredNodesAttr: make(map[string]nodeAttributes),
		discoveredImages:    make(map[string]bool),
		targetKernelArchs:   make(map[string]nodeAttributes),
	}
}

func (o *EnsureDistServiceOperation) GetJsonResult() string {
	// Could return a summary of builder statuses or errors
	// For now, an empty JSON object or a simple status
	status := map[string]interface{}{
		"message": "All builds completed",
		// TODO: Add more details like number of builders, errors, etc.
	}
	resultJSON, _ := json.Marshal(status)
	return string(resultJSON)
}

func (o *EnsureDistServiceOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SingleStep{Name: "DiscoverNodesAndLabel", Run: o.DiscoverNodesAndLabel},
		&lifecycle.SingleStep{Name: "DiscoverImages", Run: o.DiscoverImages},
		&lifecycle.SingleStep{Name: "EnsureDistService", Run: o.EnsureDistService},
		&lifecycle.SingleStep{Name: "EnsureDistContainer", Run: o.EnsureDistContainer},
		&lifecycle.SingleStep{Name: "EnsureBuilderContainers", Run: o.EnsureBuilderContainers},
		&lifecycle.SingleStep{Name: "PollBuilderContainersStatus", Run: o.PollBuilderContainersStatus},
		//{Name: "CleanupOldBuilderContainers", Run: o.CleanupOldBuilderContainers}, // Optional: remove builders for stale image/kernel/arch
		&lifecycle.SingleStep{Name: "UpdatePolicyStatusAndCallback", Run: o.UpdatePolicyStatusAndCallback},
	}
}

func (o *EnsureDistServiceOperation) getKernelLabelKey() string {
	if o.payload.KernelLabelKey != nil && *o.payload.KernelLabelKey != "" {
		return *o.payload.KernelLabelKey
	}
	return DefaultKernelLabelKey
}

func (o *EnsureDistServiceOperation) getArchLabelKey() string {
	if o.payload.ArchitectureLabelKey != nil && *o.payload.ArchitectureLabelKey != "" {
		return *o.payload.ArchitectureLabelKey
	}
	return DefaultArchitectureLabelKey
}

func (o *EnsureDistServiceOperation) DiscoverNodesAndLabel(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "DiscoverNodesAndLabel")
	defer end()

	logger.Info("Discovering nodes and ensuring labels")

	kernelLabelKey := o.getKernelLabelKey()
	archLabelKey := o.getArchLabelKey()

	type nodeProcessingInfo struct {
		node         corev1.Node
		nodeSelector map[string]string // Stores the first selector from payload that matched this node
	}
	nodesToProcessMap := make(map[string]nodeProcessingInfo)

	if len(o.payload.NodeSelectors) > 0 {
		for _, selectorMap := range o.payload.NodeSelectors {
			sel := labels.SelectorFromSet(selectorMap)
			var currentNodes corev1.NodeList
			if err := o.client.List(ctx, &currentNodes, &client.ListOptions{LabelSelector: sel}); err != nil {
				logger.Error(err, "Failed to list nodes with selector", "selector", sel.String())
				return err // Failing the step if any selector list fails
			}
			for _, node := range currentNodes.Items {
				if _, exists := nodesToProcessMap[node.Name]; !exists {
					// This node hasn't been matched by a previous selector in the payload list, so record it with current selector.
					nodesToProcessMap[node.Name] = nodeProcessingInfo{node: node, nodeSelector: selectorMap}
				}
			}
		}
	} else {
		// No selectors in payload, fetch all nodes
		var allNodesList corev1.NodeList
		if err := o.client.List(ctx, &allNodesList); err != nil {
			logger.Error(err, "Failed to list all nodes")
			return err
		}
		for _, node := range allNodesList.Items {
			nodesToProcessMap[node.Name] = nodeProcessingInfo{node: node, nodeSelector: nil} // No specific payload selector
		}
	}

	var processingItems []nodeProcessingInfo
	for _, item := range nodesToProcessMap {
		processingItems = append(processingItems, item)
	}

	return workers.ProcessConcurrently(ctx, processingItems, 32, func(ctx context.Context, item nodeProcessingInfo) error {
		node := item.node
		// We are only interested in AMD64 for now as per requirement
		if node.Status.NodeInfo.Architecture != "amd64" {
			logger.V(1).Info("Skipping node due to non-amd64 architecture", "node", node.Name, "architecture", node.Status.NodeInfo.Architecture)
			return nil
		}

		attrs := nodeAttributes{
			kernelVersion: node.Status.NodeInfo.KernelVersion,
			architecture:  node.Status.NodeInfo.Architecture,
			nodeSelector:  item.nodeSelector, // Store the captured nodeSelector (could be nil)
		}
		o.mutex.Lock()
		// Store attributes for the node if not already seen (primarily for o.targetKernelArchs)
		// o.discoveredNodesAttr might be redundant if not used elsewhere, but harmless to keep for now.
		if _, exists := o.discoveredNodesAttr[node.Name]; !exists {
			o.discoveredNodesAttr[node.Name] = attrs
		}
		key := attrs.StringKey()
		o.targetKernelArchs[key] = attrs // attrs now includes the nodeSelector, making it part of the unique key
		o.mutex.Unlock()

		// Ensure labels on the node
		nodeToUpdate := node.DeepCopy()
		updateNeeded := false
		if val, ok := nodeToUpdate.Labels[kernelLabelKey]; !ok || val != attrs.kernelVersion {
			if nodeToUpdate.Labels == nil {
				nodeToUpdate.Labels = make(map[string]string)
			}
			nodeToUpdate.Labels[kernelLabelKey] = attrs.kernelVersion
			updateNeeded = true
		}
		if val, ok := nodeToUpdate.Labels[archLabelKey]; !ok || val != attrs.architecture {
			if nodeToUpdate.Labels == nil {
				nodeToUpdate.Labels = make(map[string]string)
			}
			nodeToUpdate.Labels[archLabelKey] = attrs.architecture
			updateNeeded = true
		}

		if updateNeeded {
			logger.Info("Updating labels on node", "node", node.Name, kernelLabelKey, attrs.kernelVersion, archLabelKey, attrs.architecture)
			if err := o.client.Update(ctx, nodeToUpdate); err != nil {
				logger.Error(err, "Failed to update labels on node", "node", node.Name)
				return err
			}
		}

		return nil
	}).AsError()
}

func (o *EnsureDistServiceOperation) DiscoverImages(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "DiscoverImages")
	defer end()

	logger.Info("Discovering Weka images")

	// Add images from payload
	for _, img := range o.payload.EnsureImages {
		o.discoveredImages[img] = true
	}

	// Discover images from WekaCluster CRs
	var wekaClusterList weka.WekaClusterList
	if err := o.client.List(ctx, &wekaClusterList); err != nil {
		logger.Error(err, "Failed to list WekaClusters")
		// Not returning error, proceed with EnsureImages if any
	} else {
		for _, cluster := range wekaClusterList.Items {
			if cluster.Spec.Image != "" {
				o.discoveredImages[cluster.Spec.Image] = true
			}
			if cluster.Status.LastAppliedImage != "" {
				o.discoveredImages[cluster.Status.LastAppliedImage] = true
			}
		}
	}

	var wekaClientList weka.WekaClientList
	if err := o.client.List(ctx, &wekaClientList); err != nil {
		logger.Error(err, "Failed to list WekaClients")
	} else {
		for _, wc := range wekaClientList.Items {
			if wc.Spec.Image != "" {
				o.discoveredImages[wc.Spec.Image] = true
			}
		}
	}

	logger.Info("Image discovery complete", "totalImages", len(o.discoveredImages))
	if len(o.discoveredImages) == 0 {
		logger.Info("No images specified or discovered. Driver building might not occur.")
	}
	return nil
}

func (o *EnsureDistServiceOperation) getDistServiceName() string {
	return fmt.Sprintf("%s%s", o.policy.GetName(), DriverDistServiceSuffix)
}

func (o *EnsureDistServiceOperation) getDistContainerName(ctx context.Context) (string, error) {
	// look for existing dist container (by labels)
	if o.distContainerName != "" {
		return o.distContainerName, nil
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getDistContainerName")
	defer end()

	wekaContainerList := &weka.WekaContainerList{}
	listOpts := &client.ListOptions{
		Namespace:     o.policy.GetNamespace(),
		LabelSelector: labels.SelectorFromSet(o.getDistContainerLabels()),
	}

	if err := o.client.List(ctx, wekaContainerList, listOpts); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to list WekaContainers for dist container")
			return "", err
		}
	}

	var wekaContainers []*weka.WekaContainer
	for i := range wekaContainerList.Items {
		wekaContainers = append(wekaContainers, &wekaContainerList.Items[i])
	}

	nonDeletedContainers := discovery.SelectNonDeletedWekaContainers(wekaContainers)

	if len(nonDeletedContainers) == 0 {
		// No existing dist container found, generate a new name
		name := fmt.Sprintf("%s%s-", o.policy.GetName(), DriverDistContainerSuffix)
		o.distContainerName = names.SimpleNameGenerator.GenerateName(name)
	} else {
		o.distContainerName = nonDeletedContainers[0].Name
		logger.Info("Found existing dist container", "name", o.distContainerName)
	}

	return o.distContainerName, nil
}

func (o *EnsureDistServiceOperation) getDistAppName() string {
	return fmt.Sprintf("%s%s", o.policy.GetName(), DriverDistContainerSuffix)
}

func (o *EnsureDistServiceOperation) getServiceUrl() string {
	return fmt.Sprintf("https://%s.%s.svc.cluster.local:60002", o.getDistServiceName(), o.policy.GetNamespace())
}

func (o *EnsureDistServiceOperation) EnsureDistService(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureDistService")
	defer end()

	serviceName := o.getDistServiceName()
	namespace := o.policy.GetNamespace()
	logger.Info("Ensuring drivers distribution service", "service", serviceName, "namespace", namespace)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: namespace,
			Labels:    o.getCommonLabels(),
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, o.client, svc, func() error {
		svc.Spec.Selector = o.getDistContainerLabels()
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Name:       "weka-drivers-dist",
				Port:       60002,
				TargetPort: intstr.FromInt32(60002),
				Protocol:   corev1.ProtocolTCP,
			},
		}
		return controllerutil.SetControllerReference(o.policy, svc, o.scheme)
	})

	if err != nil {
		logger.Error(err, "Failed to create or update drivers distribution service")
		return err
	}
	logger.Info("Drivers distribution service ensured")
	return nil
}

func (o *EnsureDistServiceOperation) EnsureDistContainer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureDistContainer")
	defer end()

	containerName, err := o.getDistContainerName(ctx)
	if err != nil {
		return err
	}

	namespace := o.policy.GetNamespace()
	logger.Info("Ensuring drivers distribution (dist) container", "container", containerName, "namespace", namespace)

	if o.containerDetails.Image == "" {
		err := fmt.Errorf("image for drivers-dist container is not specified in WekaPolicy spec")
		logger.Error(err, "Cannot create dist container")
		return err
	}

	wc := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      containerName,
			Namespace: namespace,
			Labels:    o.getDistContainerLabels(),
		},
	}

	err = o.DeleteIfNodeNotReady(ctx, wc)
	if err != nil {
		return err
	}

	_, err = controllerutil.CreateOrUpdate(ctx, o.client, wc, func() error {
		wc.Spec = weka.WekaContainerSpec{
			Image:             o.containerDetails.Image, // Image from WekaPolicy.Spec.Image
			ImagePullSecret:   o.containerDetails.ImagePullSecret,
			Mode:              weka.WekaContainerModeDriversDist,
			WekaContainerName: "dist",
			AgentPort:         60001,
			Port:              60002,
			NumCores:          1,
			Tolerations:       o.containerDetails.Tolerations,
			NodeSelector:      o.payload.DistNodeSelector, // Use the new DistNodeSelector
			// Affinity, Resources etc. as needed for a dist service
			// This container needs to run on a node that can host the service.
		}
		// Add other necessary spec fields for drivers-dist container
		// For example, port configuration, resource requests/limits.
		return controllerutil.SetControllerReference(o.policy, wc, o.scheme)
	})

	if err != nil {
		logger.Error(err, "Failed to create or update drivers distribution container")
		return err
	}
	logger.Info("Drivers distribution (dist) container ensured")
	return nil
}

func (o *EnsureDistServiceOperation) DeleteIfNodeNotReady(ctx context.Context, container *weka.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleNodeNotReady")
	defer end()

	var wc weka.WekaContainer
	err := o.client.Get(ctx, client.ObjectKey{Namespace: container.Namespace, Name: container.Name}, &wc)
	if err != nil && apierrors.IsNotFound(err) {
		return nil // Container does not exist, nothing to delete
	}
	if err != nil {
		return fmt.Errorf("failed to get WekaContainer %s: %w", container.Name, err)
	}

	// check dist container's node state and delete container if node is not ready
	nodeName := wc.GetNodeAffinity()
	if nodeName == "" {
		logger.Info("Dist container has no node affinity set, skipping node readiness check", "container", wc.Name)
		return nil
	}

	node := &corev1.Node{}
	if err := o.client.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Node not found, deleting dist container", "container", wc.Name, "node", nodeName)
			err := o.client.Delete(ctx, container)

			return lifecycle.NewWaitErrorWithDuration(
				fmt.Errorf("node %s not found, deleting dist container %s, err: %w", nodeName, wc.Name, err),
				time.Second*10,
			)
		}
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	if NodeNotReady(node) {
		logger.Info("Node is not ready, deleting dist container", "container", wc.Name, "node", nodeName)
		err := o.client.Delete(ctx, container)

		return lifecycle.NewWaitErrorWithDuration(
			fmt.Errorf("node %s is not ready, deleting dist container %s, err: %w", nodeName, wc.Name, err),
			time.Second*10,
		)
	}

	return nil // Node is ready, no action needed
}

func (o *EnsureDistServiceOperation) EnsureBuilderContainers(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureBuilderContainers")
	defer end()

	logger.Info("Ensuring drivers builder containers")

	if len(o.targetKernelArchs) == 0 {
		logger.Info("No target kernel/architectures discovered. Skipping builder container creation.")
		return nil
	}
	if len(o.discoveredImages) == 0 {
		logger.Info("No images discovered or specified. Skipping builder container creation.")
		return nil
	}

	kernelLabelKey := o.getKernelLabelKey()
	archLabelKey := o.getArchLabelKey()

	for image := range o.discoveredImages {
		for _, ka := range o.targetKernelArchs {
			builderName := o.getBuilderContainerName(image, ka.kernelVersion, ka.architecture)
			namespace := o.policy.GetNamespace()

			wc := &weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      builderName,
					Namespace: namespace,
					Labels:    o.getBuilderContainerLabels(image, ka.kernelVersion, ka.architecture),
				},
			}

			distContainerName, err := o.getDistContainerName(ctx)
			if err != nil {
				return err
			}

			_, err = controllerutil.CreateOrUpdate(ctx, o.client, wc, func() error {
				wc.Spec = weka.WekaContainerSpec{
					Image:             image,                              // The Weka image for which to build drivers
					ImagePullSecret:   o.containerDetails.ImagePullSecret, // Assuming same pull secret for all
					Mode:              weka.WekaContainerModeDriversBuilder,
					WekaContainerName: "dist",
					AgentPort:         60001,
					Port:              60002,
					NumCores:          1,
					UploadResultsTo:   distContainerName,
					Tolerations:       o.containerDetails.Tolerations,
					// NodeSelector logic:
					// Merge payload selector (ka.nodeSelector), specific kernel/arch, and amd64.
					// Specifics (kernel, arch, amd64) take precedence if keys overlap.
					NodeSelector: func() map[string]string {
						builderNodeSelector := make(map[string]string)
						// 1. Add the nodeSelector from the payload (if any) that led to this kernel/arch combination
						if ka.nodeSelector != nil {
							for k, v := range ka.nodeSelector {
								builderNodeSelector[k] = v
							}
						}
						// 2. Add specific kernel and architecture labels (overrides if keys exist in ka.nodeSelector)
						builderNodeSelector[archLabelKey] = ka.architecture
						builderNodeSelector[kernelLabelKey] = ka.kernelVersion
						// 3. Ensure it runs on amd64 (overrides if kubernetes.io/arch was in ka.nodeSelector)
						builderNodeSelector["kubernetes.io/arch"] = "amd64"
						return builderNodeSelector
					}(),
					// Instructions to tell the builder what kernel/arch to build for
					Instructions: &weka.Instructions{
						Type:    "build-drivers",
						Payload: fmt.Sprintf(`{"kernel": "%s", "arch": "%s"}`, ka.kernelVersion, ka.architecture),
					},
					// PreRunScript for kernel verification if needed, though scheduling should handle it.
					// Example: wc.Spec.Overrides = &weka.WekaContainerSpecOverrides{ PreRunScript: "..." }
					// Resources: Define appropriate resources for a builder container
				}

				// Construct PreRunScript
				kernelValidationScript := fmt.Sprintf(`#!/bin/sh
set -e
TARGET_KERNEL_VERSION_FROM_PAYLOAD="%s"
ACTUAL_KERNEL_VERSION=$(uname -r)
if [ "$ACTUAL_KERNEL_VERSION" = "$TARGET_KERNEL_VERSION_FROM_PAYLOAD" ]; then
    echo "Kernel version matches: $ACTUAL_KERNEL_VERSION"
else
    echo "Kernel version mismatch: Expected $TARGET_KERNEL_VERSION_FROM_PAYLOAD, got $ACTUAL_KERNEL_VERSION" >&2
    exit 1
fi
`, ka.kernelVersion)

				finalPreRunScript := kernelValidationScript
				if o.payload.BuilderPreRunScript != nil && *o.payload.BuilderPreRunScript != "" {
					finalPreRunScript = fmt.Sprintf("%s\n\n%s", kernelValidationScript, *o.payload.BuilderPreRunScript)
				}

				if finalPreRunScript != "" {
					if wc.Spec.Overrides == nil {
						wc.Spec.Overrides = &weka.WekaContainerSpecOverrides{}
					}
					wc.Spec.Overrides.PreRunScript = finalPreRunScript
				}

				// This builder container should output to a location accessible by the drivers-dist container,
				// or upload to a shared location. This might involve PVCs or specific configurations.
				// For now, assume the builder container handles its output correctly.
				return controllerutil.SetControllerReference(o.policy, wc, o.scheme)
			})
			if err != nil {
				logger.Error(err, "Failed to create or update builder container", "builder", builderName)
				// Continue with other builders
			} else {
				logger.Info("Ensured builder container", "builder", builderName)
			}
		}
	}
	return nil
}

func (o *EnsureDistServiceOperation) PollBuilderContainersStatus(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "PollBuilderContainersStatus")
	defer end()

	logger.Info("Polling builder containers status")

	var builderList weka.WekaContainerList
	listOpts := &client.ListOptions{
		Namespace:     o.policy.GetNamespace(),
		LabelSelector: labels.SelectorFromSet(map[string]string{PolicyNameLabelKey: o.policy.GetName(), "weka.io/mode": "drivers-builder"}),
	}
	if err := o.client.List(ctx, &builderList, listOpts); err != nil {
		logger.Error(err, "Failed to list builder containers")
		return err
	}

	completed := 0
	notCompleted := 0
	totalExpectedBuilders := len(o.discoveredImages) * len(o.targetKernelArchs)

	policy := o.policy

	if totalExpectedBuilders == 0 && len(builderList.Items) == 0 {
		logger.Info("No builders expected and none found.")
		// Update status accordingly
		progressStr := "0/0"
		policy.Status.Progress = progressStr
	}
	// ensure correct url
	if policy.Status.TypedStatus == nil {
		policy.Status.TypedStatus = &weka.TypedPolicyStatus{}
	}
	if policy.Status.TypedStatus.DistService == nil {
		policy.Status.TypedStatus.DistService = &weka.DistServiceStatus{}
	}
	policy.Status.TypedStatus.DistService.ServiceUrl = o.getServiceUrl()

	activeBuilders := make(map[string]bool)
	for image := range o.discoveredImages {
		for _, ka := range o.targetKernelArchs {
			activeBuilders[o.getBuilderContainerName(image, ka.kernelVersion, ka.architecture)] = true
		}
	}

	for _, wc := range builderList.Items {
		// Only count builders that are part of the current desired set
		if !activeBuilders[wc.Name] {
			continue
		}
		if wc.Status.Status == weka.Completed {
			completed++
		} else {
			// Consider Error, Building, PodNotRunning etc. as NotCompleted
			notCompleted++
		}
	}

	actualNotCompleted := totalExpectedBuilders - completed
	if actualNotCompleted < 0 { // Should not happen if logic is correct
		actualNotCompleted = 0
	}

	progressStr := fmt.Sprintf("%d/%d", completed, totalExpectedBuilders)
	policy.Status.Progress = progressStr

	logger.Info("Builder containers status", "completed", completed, "notCompleted", actualNotCompleted, "totalExpected", totalExpectedBuilders, "progress", progressStr)
	if err := o.client.Status().Update(ctx, o.policy); err != nil {
		return err
	}

	if actualNotCompleted > 0 {
		return lifecycle.NewWaitError(fmt.Errorf("%d builder containers are not yet completed", actualNotCompleted))
	}

	return nil // All expected builders are completed
}

func (o *EnsureDistServiceOperation) CleanupOldBuilderContainers(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "CleanupOldBuilderContainers")
	defer end()

	logger.Info("Cleaning up old builder containers")

	var existingBuilderList weka.WekaContainerList
	listOpts := &client.ListOptions{
		Namespace:     o.policy.GetNamespace(),
		LabelSelector: labels.SelectorFromSet(map[string]string{PolicyNameLabelKey: o.policy.GetName(), "weka.io/mode": "drivers-builder"}),
	}
	if err := o.client.List(ctx, &existingBuilderList, listOpts); err != nil {
		logger.Error(err, "Failed to list existing builder containers for cleanup")
		return err
	}

	currentBuilders := make(map[string]bool)
	for image := range o.discoveredImages {
		for _, ka := range o.targetKernelArchs {
			currentBuilders[o.getBuilderContainerName(image, ka.kernelVersion, ka.architecture)] = true
		}
	}

	for _, wc := range existingBuilderList.Items {
		if !currentBuilders[wc.Name] {
			logger.Info("Deleting stale builder container", "name", wc.Name)
			if err := o.client.Delete(ctx, &wc); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "Failed to delete stale builder container", "name", wc.Name)
				// Log and continue
			}
		}
	}
	return nil
}

func (o *EnsureDistServiceOperation) UpdatePolicyStatusAndCallback(ctx context.Context) error {
	ctx, logger, _ := instrumentation.GetLogSpan(ctx, "UpdatePolicyStatusAndCallback")
	logger.Info("Driver builders are still processing or some have failed. Policy will re-evaluate.")
	return o.successCallback(ctx) // For an ongoing policy, success means it has reconciled the current state.
}

func (o *EnsureDistServiceOperation) getCommonLabels() map[string]string {
	baseLabels := make(map[string]string)

	// Add labels from WekaPolicy.ObjectMeta.Labels first
	if o.policy.ObjectMeta.Labels != nil {
		for k, v := range o.policy.ObjectMeta.Labels {
			baseLabels[k] = v
		}
	}

	// Then add/override with specific labels like PolicyNameLabelKey
	baseLabels[PolicyNameLabelKey] = o.policy.GetName()
	// Add other common operator-defined labels if needed in the future

	return baseLabels
}

func (o *EnsureDistServiceOperation) getDistContainerLabels() map[string]string {
	labels := o.getCommonLabels()
	labels["app"] = o.getDistAppName() // For service selector
	labels["weka.io/mode"] = weka.WekaContainerModeDriversDist
	return labels
}

func (o *EnsureDistServiceOperation) getBuilderContainerLabels(image, kernel, arch string) map[string]string {
	labels := o.getCommonLabels()
	labels["weka.io/mode"] = weka.WekaContainerModeDriversBuilder
	labels["weka.io/target-image-hash"] = hashFNV(image) // To avoid overly long label values
	labels["weka.io/target-kernel"] = strings.ReplaceAll(kernel, ".", "-")
	labels["weka.io/target-arch"] = arch
	return labels
}

func (o *EnsureDistServiceOperation) getBuilderContainerName(image, kernel, arch string) string {
	// Name must be DNS-1123 compliant
	imgHash := hashFNV(image)
	kernelNorm := strings.ReplaceAll(kernel, ".", "-")
	kernelNorm = strings.ReplaceAll(kernelNorm, "_", "-") // Further sanitize kernel

	// Remove any leading or trailing hyphens from kernelNorm itself.
	// This prevents kernelNorm from causing the full name to end with a hyphen.
	kernelNorm = strings.Trim(kernelNorm, "-")

	// If kernelNorm became empty (e.g., kernel was originally "-" or ".-."),
	// use a placeholder to ensure the final segment of the name is valid.
	if kernelNorm == "" {
		kernelNorm = "unknownkernel"
	}

	name := fmt.Sprintf("%s-%s-%s-%s-%s", o.policy.GetName(), DriversBuilderSuffix, imgHash, arch, kernelNorm)

	// Truncate if the name is too long.
	if len(name) > 63 {
		name = name[:63]
	}

	// After potential truncation or if the original construction ended with a hyphen,
	// ensure the name does not end with a hyphen.
	// DriverBuilderPrefix ensures the name won't be empty or start with a hyphen.
	name = strings.TrimRight(name, "-")

	return name
}

func hashFNV(s string) string {
	h := fnv.New32a()
	h.Write([]byte(s))
	return strconv.FormatUint(uint64(h.Sum32()), 10)
}

func (o *EnsureDistServiceOperation) AsStep() lifecycle.Step {
	return &lifecycle.SingleStep{
		Name: "EnableLocalDriversDistribution",
		Run:  AsRunFunc(o), // Assuming AsRunFunc helper exists
	}
}

func NodeNotReady(node *corev1.Node) bool {
	if node == nil {
		return true // If node is nil, consider it not ready
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
			return true // Node is not ready
		}
	}
	return false // Node is ready
}
