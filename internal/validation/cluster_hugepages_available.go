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

const mib = int64(1) << 20

// clusterHugepagesAvailable warns per-role on capacity and single-fit
// failures, mirroring clusterCoresAvailable but for hugepages-2Mi.
//
// Unit gap: *Hugepages fields are MiB (pod.go formats them "%dMi"); Allocatable[hugepages-2Mi] is
// bytes — multiply MiB × mib to compare. Skipped when *Hugepages is 0 (operator-derived from drive
// capacity). Role mapping isn't 1:1 with cores: s3/nfs/smbw use the *Frontend* fields.
type clusterHugepagesAvailable struct{}

func (clusterHugepagesAvailable) ID() string {
	return "cluster_hugepages_available"
}

func (clusterHugepagesAvailable) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	if cluster.Spec.Dynamic == nil {
		return nil
	}
	hpResource := corev1.ResourceName(string(corev1.ResourceHugePagesPrefix) + "2Mi")

	var errs field.ErrorList
	for _, ch := range rolesForTemplate(cluster.Spec.Dynamic) {
		if ch.hugepages <= 0 || ch.containers <= 0 {
			continue
		}
		selector := cluster.GetNodeSelectorForRole(ch.role)
		var nodes corev1.NodeList
		if err := c.List(ctx, &nodes, client.MatchingLabels(selector)); err != nil {
			errs = append(errs, field.InternalError(
				field.NewPath("spec", "dynamicTemplate", ch.hugepagesField),
				fmt.Errorf("listing nodes for role %q: %w", ch.role, err),
			))
			continue
		}
		if len(nodes.Items) == 0 {
			continue
		}

		var totalAllocBytes int64
		var minNodeAllocBytes int64 = -1
		for i := range nodes.Items {
			qty := nodes.Items[i].Status.Allocatable[hpResource]
			a := qty.Value()
			totalAllocBytes += a
			if minNodeAllocBytes < 0 || a < minNodeAllocBytes {
				minNodeAllocBytes = a
			}
		}

		perContainerBytes := int64(ch.hugepages) * mib
		totalRequestedBytes := perContainerBytes * int64(ch.containers)

		if perContainerBytes > minNodeAllocBytes {
			detail := fmt.Sprintf(
				"spec.dynamicTemplate.%s (%d MiB) exceeds the smallest matched node's "+
					"allocatable hugepages-2Mi (%d MiB) for role %q. No matched "+
					"node can host even one %s container; pods will stay Pending. "+
					"Reduce %s or configure more hugepages on the nodes.",
				ch.hugepagesField, ch.hugepages, minNodeAllocBytes/mib, ch.role,
				ch.role, ch.hugepagesField,
			)
			errs = append(errs, field.Invalid(
				field.NewPath("spec", "dynamicTemplate", ch.hugepagesField),
				ch.hugepages, detail,
			))
		}

		if totalRequestedBytes > totalAllocBytes {
			detail := fmt.Sprintf(
				"spec.dynamicTemplate.%s × %s (%d × %d = %d MiB) exceeds "+
					"total allocatable hugepages-2Mi across %d matched node(s) "+
					"(%d MiB) for role %q. Some containers will fail to schedule. "+
					"Reduce %s, %s, or add more hugepages.",
				ch.hugepagesField, ch.containersField, ch.hugepages, ch.containers,
				ch.hugepages*ch.containers, len(nodes.Items),
				totalAllocBytes/mib, ch.role, ch.hugepagesField, ch.containersField,
			)
			errs = append(errs, field.Invalid(
				field.NewPath("spec", "dynamicTemplate", ch.hugepagesField),
				ch.hugepages, detail,
			))
		}
	}
	return errs
}
