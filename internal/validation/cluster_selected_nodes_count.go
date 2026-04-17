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

type roleSpec struct {
	role       string
	fieldName  string
	containers int
}

func (clusterSelectedNodesCount) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	if cluster.Spec.Dynamic == nil {
		return nil
	}
	d := cluster.Spec.Dynamic
	roles := []roleSpec{
		{"drive", "driveContainers", d.DriveContainers},
		{"compute", "computeContainers", d.ComputeContainers},
		{"s3", "s3Containers", d.S3Containers},
		{"nfs", "nfsContainers", d.NfsContainers},
		{"smbw", "smbwContainers", d.SmbwContainers},
		{"data-services", "dataServicesContainers", d.DataServicesContainers},
	}

	var errs field.ErrorList
	for _, r := range roles {
		if r.containers <= 0 {
			continue
		}
		selector := cluster.GetNodeSelectorForRole(r.role)
		var nodes corev1.NodeList
		if err := c.List(ctx, &nodes, client.MatchingLabels(selector)); err != nil {
			errs = append(errs, field.InternalError(
				field.NewPath("spec", "dynamic", r.fieldName),
				fmt.Errorf("listing nodes for role %q: %w", r.role, err),
			))
			continue
		}
		matched := len(nodes.Items)
		if r.containers <= matched {
			continue
		}
		detail := fmt.Sprintf(
			"spec.dynamic.%s (%d) exceeds the number of nodes matching the "+
				"%q-role selector (%d). The cluster cannot deploy %d %s containers "+
				"on %d node(s); some containers will fail to schedule. Reduce "+
				"%s or label more nodes.",
			r.fieldName, r.containers, r.role, matched, r.containers, r.role, matched, r.fieldName,
		)
		errs = append(errs, field.Invalid(
			field.NewPath("spec", "dynamic", r.fieldName),
			r.containers,
			detail,
		))
	}
	return errs
}
