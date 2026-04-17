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
// Unit gap: *Hugepages fields are MiB (pod.go formats them as "%dMi");
// Allocatable[hugepages-2Mi].Value() is bytes. Multiply MiB × mib to
// compare. Skipped when *Hugepages is 0 (operator-derived from drive
// capacity). Role mapping isn't 1:1 with cores: s3/nfs/smbw use the
// *Frontend* fields. DataServicesFeHugepages is intentionally excluded.
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
	d := cluster.Spec.Dynamic
	checks := []struct {
		role                string
		fieldName           string
		containersFieldName string
		hugepages           int // MiB per container
		containers          int
	}{
		{"drive", "driveHugepages", "driveContainers", d.DriveHugepages, d.DriveContainers},
		{"compute", "computeHugepages", "computeContainers", d.ComputeHugepages, d.ComputeContainers},
		{"s3", "s3FrontendHugepages", "s3Containers", d.S3FrontendHugepages, d.S3Containers},
		{"nfs", "nfsFrontendHugepages", "nfsContainers", d.NfsFrontendHugepages, d.NfsContainers},
		{"smbw", "smbwFrontendHugepages", "smbwContainers", d.SmbwFrontendHugepages, d.SmbwContainers},
		{"data-services", "dataServicesHugepages", "dataServicesContainers", d.DataServicesHugepages, d.DataServicesContainers},
	}

	hpResource := corev1.ResourceName(string(corev1.ResourceHugePagesPrefix) + "2Mi")

	var errs field.ErrorList
	for _, ch := range checks {
		if ch.hugepages <= 0 || ch.containers <= 0 {
			continue
		}
		selector := cluster.GetNodeSelectorForRole(ch.role)
		var nodes corev1.NodeList
		if err := c.List(ctx, &nodes, client.MatchingLabels(selector)); err != nil {
			errs = append(errs, field.InternalError(
				field.NewPath("spec", "dynamic", ch.fieldName),
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
				"spec.dynamic.%s (%d MiB) exceeds the smallest matched node's "+
					"allocatable hugepages-2Mi (%d MiB) for role %q. No matched "+
					"node can host even one %s container; pods will stay Pending. "+
					"Reduce %s or configure more hugepages on the nodes.",
				ch.fieldName, ch.hugepages, minNodeAllocBytes/mib, ch.role,
				ch.role, ch.fieldName,
			)
			errs = append(errs, field.Invalid(
				field.NewPath("spec", "dynamic", ch.fieldName),
				ch.hugepages, detail,
			))
		}

		if totalRequestedBytes > totalAllocBytes {
			detail := fmt.Sprintf(
				"spec.dynamic.%s × %s (%d × %d = %d MiB) exceeds "+
					"total allocatable hugepages-2Mi across %d matched node(s) "+
					"(%d MiB) for role %q. Some containers will fail to schedule. "+
					"Reduce %s, %s, or add more hugepages.",
				ch.fieldName, ch.containersFieldName, ch.hugepages, ch.containers,
				ch.hugepages*ch.containers, len(nodes.Items),
				totalAllocBytes/mib, ch.role, ch.fieldName, ch.containersFieldName,
			)
			errs = append(errs, field.Invalid(
				field.NewPath("spec", "dynamic", ch.fieldName),
				ch.hugepages, detail,
			))
		}
	}
	return errs
}
