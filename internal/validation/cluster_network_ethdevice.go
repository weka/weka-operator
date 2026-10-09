package validation

import (
	"context"
	"fmt"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

// nicResourceName matches what ensure_nics.go advertises and pod.go
// requests (internal/pkg/domain/allocations.go: WEKANICs). "weka.io/nics"
// (without the "weka-" prefix) is not used anywhere on the node.
const nicResourceName = corev1.ResourceName(domain.WEKANICs)

// clusterNetworkEthdevice warns per-role when DPDK NIC requests don't fit
// (one NIC per core in DPDK). It compares against whichever count the
// spec makes knowable at admission time:
//
//   - ethDevice/ethDevices pin devices by name, so the count is the
//     number named. weka.io/weka-nics is not requested in this case, so
//     node allocatable says nothing.
//   - otherwise the role uses the VF-per-IO-node path and the count is
//     the node's allocatable weka.io/weka-nics.
//
// UdpMode roles need no data devices at all. Selectors/deviceSubnets
// resolve to a device count only on the node, so they are skipped.
// Bootstrap-skipped per role when ensure-nics hasn't yet populated the
// weka-nics annotation or weka.io/weka-nics allocatable.
//
// The original AC-007 named-device check (verify ethDevice/ethDevices
// exists) is dropped: domain.NIC has no Linux interface name field, so
// the annotation can't answer "is eth99 a real NIC". Future work.
type clusterNetworkEthdevice struct{}

// namedNetDeviceCount returns how many data devices the spec pins by name, or 0
// when it pins none by name (udp, selectors, deviceSubnets, or nothing at all).
func namedNetDeviceCount(n *weka.Network) int {
	if n == nil {
		return 0
	}
	if len(n.EthDevices) > 0 {
		return len(n.EthDevices)
	}
	if n.EthDevice != "" {
		return 1
	}
	return 0
}

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

	var errs field.ErrorList
	for _, ch := range rolesForTemplate(cluster.Spec.Dynamic) {
		if ch.cores <= 0 || ch.containers <= 0 {
			continue
		}
		roleNetwork := cluster.GetNetworkForRole(ch.role)
		if roleNetwork.UdpMode {
			continue
		}
		if named := namedNetDeviceCount(&roleNetwork); named > 0 {
			// Devices are pinned by name, so weka.io/weka-nics is never requested and the
			// node allocatable below says nothing. The container still needs one device per
			// IO node though, and falling short fails inside WEKA ("N slots need network
			// devices") long after admission, leaving the cluster waiting in Init.
			if ch.cores > named {
				errs = append(errs, field.Invalid(
					field.NewPath("spec", "dynamicTemplate", ch.coresField),
					ch.cores,
					fmt.Sprintf(
						"spec.dynamicTemplate.%s (%d cores → %d NICs per container in DPDK mode) "+
							"exceeds the %d device(s) pinned by network.ethDevice(s) for role %q. "+
							"Containers will start but their IO nodes will not. Reduce %s, "+
							"pin more devices, switch to udpMode, or add NICs.",
						ch.coresField, ch.cores, ch.cores, named, ch.role, ch.coresField,
					),
				))
			}
			continue
		}
		if utils.HasExplicitNetDevices(&roleNetwork) {
			// Selectors/deviceSubnets resolve to a device count only on the node, so there is
			// nothing to compare against here.
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
				"spec.dynamicTemplate.%s (%d cores → %d NICs per container in DPDK mode) "+
					"exceeds the smallest matched node's allocatable weka.io/weka-nics (%d) "+
					"for role %q. No matched node can host even one %s container; pods "+
					"will stay Pending. Reduce %s, switch to udpMode, or add NICs.",
				ch.coresField, ch.cores, ch.cores, minNodeAllocNics,
				ch.role, ch.role, ch.coresField,
			)
			errs = append(errs, field.Invalid(
				field.NewPath("spec", "dynamicTemplate", ch.coresField),
				ch.cores, detail,
			))
		}

		if totalRequested > totalAllocNics {
			detail := fmt.Sprintf(
				"spec.dynamicTemplate.%s × %s (%d × %d = %d NICs total) exceeds "+
					"total allocatable weka.io/weka-nics across %d matched node(s) (%d) for "+
					"role %q. Some containers will fail to schedule. Reduce %s, "+
					"%s, switch to udpMode, or add NICs.",
				ch.coresField, ch.containersField, ch.cores, ch.containers,
				ch.cores*ch.containers, len(nodes.Items), totalAllocNics,
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
