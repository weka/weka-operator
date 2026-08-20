package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clusterSelectedNodesCount checks per-role that *Containers does not
// exceed the count of nodes matching that role's selector. Per-role
// rather than summed because Weka co-locates one container of each role
// per node — a coarse total would false-fire on a 6+6-on-6-nodes
// baseline.
type clusterSelectedNodesCount struct{}

func (clusterSelectedNodesCount) ID() string {
	return "cluster_selected_nodes_count"
}

func (clusterSelectedNodesCount) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	if cluster.Spec.Dynamic == nil {
		return nil
	}
	var errs field.ErrorList
	for _, r := range rolesForTemplate(cluster.Spec.Dynamic) {
		if r.containers <= 0 {
			continue
		}
		selector := cluster.GetNodeSelectorForRole(r.role)
		var nodes corev1.NodeList
		if err := c.List(ctx, &nodes, client.MatchingLabels(selector)); err != nil {
			errs = append(errs, field.InternalError(
				field.NewPath("spec", "dynamicTemplate", r.containersField),
				fmt.Errorf("listing nodes for role %q: %w", r.role, err),
			))
			continue
		}
		matched := len(nodes.Items)
		if r.containers <= matched {
			continue
		}
		detail := fmt.Sprintf(
			"spec.dynamicTemplate.%s (%d) exceeds the number of nodes matching the "+
				"%q-role selector (%d). The cluster cannot deploy %d %s containers "+
				"on %d node(s); some containers will fail to schedule. Reduce "+
				"%s or label more nodes.",
			r.containersField, r.containers, r.role, matched, r.containers, r.role, matched, r.containersField,
		)
		errs = append(errs, field.Invalid(
			field.NewPath("spec", "dynamicTemplate", r.containersField),
			r.containers,
			detail,
		))
	}
	return errs
}
