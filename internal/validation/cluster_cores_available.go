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

// clusterCoresAvailable checks per-role, against matched nodes' Allocatable[cpu] (the kubelet view,
// which doesn't reflect Weka's isolcpus pinning): total requested cores must fit total allocatable
// (no bin-packing across roles attempted), and the smallest node must fit at least one container.
// Skipped when *Cores is 0 (operator-derived) or no nodes match (clusterSelectedNodesCount covers that).
type clusterCoresAvailable struct{}

func (clusterCoresAvailable) ID() string {
	return "cluster_cores_available"
}

func (clusterCoresAvailable) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	if cluster.Spec.Dynamic == nil {
		return nil
	}
	var errs field.ErrorList
	for _, ch := range rolesForTemplate(cluster.Spec.Dynamic) {
		if ch.cores <= 0 || ch.containers <= 0 {
			continue
		}
		selector := cluster.GetNodeSelectorForRole(ch.role)
		var nodes corev1.NodeList
		if err := c.List(ctx, &nodes, client.MatchingLabels(selector)); err != nil {
			errs = append(errs, field.InternalError(
				field.NewPath("spec", "dynamicTemplate", ch.coresField),
				fmt.Errorf("listing nodes for role %q: %w", ch.role, err),
			))
			continue
		}
		if len(nodes.Items) == 0 {
			continue
		}

		var totalAllocMilli int64
		var minNodeAllocMilli int64 = -1
		for i := range nodes.Items {
			cpuQty := nodes.Items[i].Status.Allocatable[corev1.ResourceCPU]
			a := cpuQty.MilliValue()
			totalAllocMilli += a
			if minNodeAllocMilli < 0 || a < minNodeAllocMilli {
				minNodeAllocMilli = a
			}
		}

		perContainerMilli := int64(ch.cores) * 1000
		totalRequestedMilli := perContainerMilli * int64(ch.containers)

		if perContainerMilli > minNodeAllocMilli {
			detail := fmt.Sprintf(
				"spec.dynamicTemplate.%s (%d cores) exceeds the smallest matched node's "+
					"allocatable CPU (%dm) for role %q. No matched node can host "+
					"even one %s container; pods will stay Pending. Reduce %s or "+
					"use larger nodes.",
				ch.coresField, ch.cores, minNodeAllocMilli, ch.role, ch.role, ch.coresField,
			)
			errs = append(errs, field.Invalid(
				field.NewPath("spec", "dynamicTemplate", ch.coresField),
				ch.cores, detail,
			))
		}

		if totalRequestedMilli > totalAllocMilli {
			detail := fmt.Sprintf(
				"spec.dynamicTemplate.%s × %s (%d × %d = %d cores) exceeds "+
					"total allocatable CPU across %d matched node(s) (%dm) for "+
					"role %q. Some containers will fail to schedule. Reduce %s, "+
					"%s, or label more nodes.",
				ch.coresField, ch.containersField, ch.cores, ch.containers,
				ch.cores*ch.containers, len(nodes.Items), totalAllocMilli,
				ch.role, ch.coresField, ch.containersField,
			)
			errs = append(errs, field.Invalid(
				field.NewPath("spec", "dynamicTemplate", ch.coresField),
				ch.cores, detail,
			))
		}
	}
	return errs
}
