package wekacontainer

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/pkg/util"
)

func GetNodeAgentName(nodeName weka.NodeName) string {
	return fmt.Sprintf("%s-node-agent-%s", config.Config.OperatorPrefix, nodeName)
}

func (r *containerReconcilerLoop) EnsureNodeAgent(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	// Skip if this is already a node-agent container
	if r.container.IsNodeAgentContainer() {
		return nil
	}

	// Get the node affinity - this should be set at this point
	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return fmt.Errorf("node affinity is not set")
	}

	// Check if node-agent WekaContainer already exists on this node
	nodeAgentName := GetNodeAgentName(nodeName)
	nodeAgentNamespace, err := util.GetPodNamespace()
	if err != nil {
		return errors.Wrap(err, "failed to get operator namespace")
	}

	existingNodeAgent := &weka.WekaContainer{}
	err = r.Get(ctx, client.ObjectKey{
		Name:      nodeAgentName,
		Namespace: nodeAgentNamespace,
	}, existingNodeAgent)

	if err == nil {
		// Node-agent already exists, check if it's on the correct node
		if existingNodeAgent.GetNodeAffinity() == nodeName {
			logger.Debug("Node-agent already exists", "node", nodeName)
			return nil
		} else {
			err := errors.New("Node-agent's node affinity does not match target node")
			logger.Error(err, "", "node", existingNodeAgent.GetNodeAffinity(), "target_node", nodeName)
			return err
		}
	}

	logger.Info("Creating node-agent on node", "node", nodeName)

	labels := factory.RequiredAnyWekaContainerLabels(weka.WekaContainerModeNodeAgent)

	operatorPod, err := util.GetOperatorPod(ctx, r.Client)
	if err != nil {
		return errors.Wrap(err, "failed to get operator pod")
	}

	// node agent has the same tolerations as the operator pod
	tolerations := operatorPod.Spec.Tolerations

	// Create node-agent WekaContainer
	nodeAgent := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeAgentName,
			Namespace: nodeAgentNamespace,
			Labels:    labels,
		},
		Spec: weka.WekaContainerSpec{
			Image:             resources.GetImageForNodeAgent(r.container),
			ImagePullSecret:   r.container.Spec.ImagePullSecret,
			Mode:              weka.WekaContainerModeNodeAgent,
			WekaContainerName: "node-agent",
			NodeAffinity:      nodeName,
			Resources: &weka.PodResourcesSpec{
				Limits: weka.PodResources{
					Cpu:    resource.MustParse(config.Config.NodeAgent.Resources.Limits.CPU),
					Memory: resource.MustParse(config.Config.NodeAgent.Resources.Limits.Memory),
				},
				Requests: weka.PodResources{
					Cpu:    resource.MustParse(config.Config.NodeAgent.Resources.Requests.CPU),
					Memory: resource.MustParse(config.Config.NodeAgent.Resources.Requests.Memory),
				},
			},
			Tolerations: tolerations,
		},
	}

	// Create the node-agent container (no owner reference - it should be independent)
	err = r.Create(ctx, nodeAgent)
	if err != nil {
		return errors.Wrap(err, "failed to create node-agent WekaContainer")
	}

	logger.Info("Successfully created node-agent WekaContainer", "node", nodeName, "container", nodeAgentName)
	return nil
}

// CleanupNodeAgentIfUnneeded checks if there are other WekaContainers on the same node
// and removes the node-agent if it's not needed
func (r *containerReconcilerLoop) CleanupNodeAgentIfUnneeded(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "CleanupNodeAgentIfUnneeded")
	defer end()

	// Only node-agent containers should clean themselves up
	if !r.container.IsNodeAgentContainer() {
		return nil
	}

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return nil
	}

	// Check if there are any other non-node-agent WekaContainers on this node
	wekaContainers, err := r.KubeService.GetWekaContainersSimple(ctx, "", string(nodeName), nil)
	if err != nil {
		return err
	}

	otherContainersOnNode := 0
	for _, container := range wekaContainers {
		// Skip self and other node-agent containers
		if container.GetUID() == r.container.GetUID() || container.IsNodeAgentContainer() {
			continue
		}

		// Check if container is on the same node
		if container.GetNodeAffinity() == nodeName {
			otherContainersOnNode++
		}
	}

	if otherContainersOnNode == 0 {
		logger.Info("No other WekaContainers on node, deleting node-agent", "node", nodeName)
		// Mark for deletion
		return r.Delete(ctx, r.container)
	}

	logger.Debug("WekaContainers exist on node, keeping node-agent",
		"node", nodeName, "weka_containers", otherContainersOnNode)
	return nil
}
