package validation

import (
	"context"
	"fmt"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/pkg/domain"
)

// nicResourceName matches what ensure_nics.go advertises and pod.go
// requests (internal/pkg/domain/allocations.go: WEKANICs). "weka.io/nics"
// (without the "weka-" prefix) is not used anywhere on the node.
const nicResourceName = corev1.ResourceName(domain.WEKANICs)

// clusterNetworkEthdevice warns per-role when DPDK NIC requests don't fit
// on matched nodes (one NIC per core in DPDK). UdpMode is checked per
// role via cluster.GetNetworkForRole — UDP roles don't request NICs at
// pod creation (pod.go:1441) so the NIC check skips them. Bootstrap-
// skipped per role when ensure-nics hasn't yet populated the weka-nics
// annotation or weka.io/weka-nics allocatable.
//
// The original AC-007 named-device check (verify ethDevice/ethDevices
// exists) is dropped: domain.NIC has no Linux interface name field, so
// the annotation can't answer "is eth99 a real NIC". Future work.
type clusterNetworkEthdevice struct{}

func (clusterNetworkEthdevice) ID() string {
	return "cluster_network_ethdevice"
}

func (clusterNetworkEthdevice) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*weka.WekaCluster)
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
		cores               int
		containers          int
	}{
		{"drive", "driveCores", "driveContainers", d.DriveCores, d.DriveContainers},
		{"compute", "computeCores", "computeContainers", d.ComputeCores, d.ComputeContainers},
		{"s3", "s3Cores", "s3Containers", d.S3Cores, d.S3Containers},
		{"nfs", "nfsCores", "nfsContainers", d.NfsCores, d.NfsContainers},
		{"smbw", "smbwCores", "smbwContainers", d.SmbwCores, d.SmbwContainers},
		{"data-services", "dataServicesCores", "dataServicesContainers", d.DataServicesCores, d.DataServicesContainers},
	}

	var errs field.ErrorList
	for _, ch := range checks {
		if ch.cores <= 0 || ch.containers <= 0 {
			continue
		}
		if cluster.GetNetworkForRole(ch.role).UdpMode {
			// Operator won't request NICs for UDP-mode roles
			// (pod.go:1441 gates the WEKANICs request on
			// !UdpMode), so there is nothing to validate here.
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

		anyData := false
		for i := range nodes.Items {
			n := &nodes.Items[i]
			if _, ok := n.Annotations[domain.WEKANICs]; ok {
				anyData = true
				break
			}
			if _, ok := n.Status.Allocatable[nicResourceName]; ok {
				anyData = true
				break
			}
		}
		if !anyData {
			continue
		}

		var totalAllocNics int64
		var minNodeAllocNics int64 = -1
		for i := range nodes.Items {
			qty := nodes.Items[i].Status.Allocatable[nicResourceName]
			v := qty.Value()
			totalAllocNics += v
			if minNodeAllocNics < 0 || v < minNodeAllocNics {
				minNodeAllocNics = v
			}
		}

		perContainer := int64(ch.cores)
		totalRequested := perContainer * int64(ch.containers)

		if perContainer > minNodeAllocNics {
			detail := fmt.Sprintf(
				"spec.dynamic.%s (%d cores → %d NICs per container in DPDK mode) "+
					"exceeds the smallest matched node's allocatable weka.io/weka-nics (%d) "+
					"for role %q. No matched node can host even one %s container; pods "+
					"will stay Pending. Reduce %s, switch to udpMode, or add NICs.",
				ch.fieldName, ch.cores, ch.cores, minNodeAllocNics,
				ch.role, ch.role, ch.fieldName,
			)
			errs = append(errs, field.Invalid(
				field.NewPath("spec", "dynamic", ch.fieldName),
				ch.cores, detail,
			))
		}

		if totalRequested > totalAllocNics {
			detail := fmt.Sprintf(
				"spec.dynamic.%s × %s (%d × %d = %d NICs total) exceeds "+
					"total allocatable weka.io/weka-nics across %d matched node(s) (%d) for "+
					"role %q. Some containers will fail to schedule. Reduce %s, "+
					"%s, switch to udpMode, or add NICs.",
				ch.fieldName, ch.containersFieldName, ch.cores, ch.containers,
				ch.cores*ch.containers, len(nodes.Items), totalAllocNics,
				ch.role, ch.fieldName, ch.containersFieldName,
			)
			errs = append(errs, field.Invalid(
				field.NewPath("spec", "dynamic", ch.fieldName),
				ch.cores, detail,
			))
		}
	}
	return errs
}
