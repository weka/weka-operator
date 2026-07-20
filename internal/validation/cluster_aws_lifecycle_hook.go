package validation

import (
	"context"
	"fmt"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// clusterAWSLifecycleHook warns (never rejects) when a WekaCluster has matched
// drive-role nodes on AWS but the node-agent's ASG lifecycle hook name is not
// configured. Without the hook, an ASG scale-down of drive nodes can terminate
// instances before their drives finish draining, risking data loss during rebuild.
type clusterAWSLifecycleHook struct{}

func (clusterAWSLifecycleHook) ID() string {
	return "cluster_aws_lifecycle_hook"
}

func (clusterAWSLifecycleHook) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*weka.WekaCluster)
	if !ok {
		return nil
	}

	if config.Config.Aws.NodeLifecycleHookName != "" {
		return nil
	}

	fldPath := field.NewPath("spec")

	selector := cluster.GetNodeSelectorForRole("drive")
	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes, client.MatchingLabels(selector)); err != nil {
		return field.ErrorList{
			field.InternalError(fldPath, fmt.Errorf("listing drive-role nodes: %w", err)),
		}
	}
	if len(nodes.Items) == 0 {
		return nil
	}

	for i := range nodes.Items {
		node := &nodes.Items[i]
		if discovery.ProviderFromID(node.Spec.ProviderID) != discovery.ProviderAWS {
			continue
		}
		detail := fmt.Sprintf(
			"cluster is on AWS (node %q has an aws:// providerID) but the node-agent lifecycle "+
				"hook is not configured (nodeAgent.aws.lifecycleHook.name is empty). Without it, "+
				"an ASG scale-down of drive nodes can terminate instances before their drives "+
				"finish draining, risking data loss during rebuild. Set "+
				"nodeAgent.aws.lifecycleHook.name and register the hook.",
			node.Name,
		)
		return field.ErrorList{
			field.Invalid(fldPath, cluster.Name, detail),
		}
	}

	return nil
}
