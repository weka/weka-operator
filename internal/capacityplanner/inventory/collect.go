// Package inventory collects, from Kubernetes, the per-node remaining-headroom view and this cluster's
// existing-container view that the pure capacity planner (internal/capacityplanner) consumes. It is the
// single source of truth shared by the wekacluster controller and the weka-capacity dry-run CLI: given a
// WekaCluster and its own WekaContainers, Collect returns the planner inputs plus a rich per-node detail
// (NodeDetail) used by `explore-nodes` and for feasibility narration.
//
// All Kubernetes reads live here; the algorithm stays pure in the parent package. The collector lists
// candidate nodes (drive + compute role selectors), reads each node's weka-shared-drives annotation and
// Status.Allocatable, and subtracts the footprint of EVERY WekaContainer on the node (other clusters'
// AND this cluster's own, all modes) so the planner sees pure remaining headroom.
package inventory

import (
	"context"
	"fmt"
	"maps"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

// Collector reads Kubernetes to build the capacity-planner inputs. It holds a controller-runtime client;
// the WekaContainer listing goes through kubernetes.KubeService built from that client.
type Collector struct {
	Client client.Client
}

// NewCollector returns a Collector backed by the given client.
func NewCollector(c client.Client) Collector { return Collector{Client: c} }

// Result bundles everything Collect produces: the pure-planner inputs plus the rich per-node detail.
type Result struct {
	// Planner inputs (fed directly to capacityplanner.PlanCapacity).
	Inventory       []capacityplanner.NodeCapacity
	ExistingDrives  []capacityplanner.ExistingContainer
	ExistingCompute []capacityplanner.ExistingComputeContainer
	FDByNode        map[string]string
	ComputeNodes    map[string]bool
	// Nodes is the rich per-node view for explore-nodes / feasibility narration (phys vs used vs free per
	// resource, FD, deleting flag, and the WekaContainers consuming the node). Not used by the planner.
	Nodes []NodeDetail
}

// Consumer is one WekaContainer charged against a node's headroom, with its per-pool / per-resource
// footprint as the collector charges it (drives via the shared sizing model, compute/other from spec).
type Consumer struct {
	Name         string
	Namespace    string
	Cluster      string // owning WekaCluster name ("" when not owned by a WekaCluster)
	Role         string // drive | compute | <other mode>
	TlcGiB       int
	QlcGiB       int
	Cores        int
	HugepagesMiB int
	MemoryMiB    int
	// NilRatio flags a drive container whose DriveTypesRatio is nil — its ContainerCapacity is attributed
	// 100% to TLC (see weka.GetTlcQlcCapacity), which skews both inventory and current. Surfaced so
	// explore-nodes can warn about it.
	NilRatio          bool
	MarkedForDeletion bool
}

// NodeDetail is the per-node breakdown explore-nodes renders. Free = Allocatable/phys − Used for each
// resource. For a compute-only node PhysTlc/PhysQlc are zero.
type NodeDetail struct {
	Node    string
	FDValue string
	// Physical shared-drive capacity from the node's weka-shared-drives annotation.
	PhysTlcGiB int
	PhysQlcGiB int
	// Used = summed footprint of the node's Consumers (net of marked-for-deletion, mirroring the planner).
	UsedTlcGiB       int
	UsedQlcGiB       int
	UsedCores        int
	UsedHugepagesMiB int
	UsedMemoryMiB    int
	// Allocatable node resources (Status.Allocatable): cpu, hugepages-2Mi (MiB), memory (MiB).
	AllocatableCores        int
	AllocatableHugepagesMiB int
	AllocatableMemoryMiB    int
	// Free remaining headroom for this cluster (clamped at 0), matching the planner's NodeCapacity.
	FreeTlcGiB                int
	FreeQlcGiB                int
	FreeCores                 int
	FreeHugepagesMiB          int
	FreeMemoryMiB             int
	HasDeletingDriveContainer bool
	IsDriveCandidate          bool // node carries usable shared-drive capacity
	Consumers                 []Consumer
}

// Collect runs the full collection: node inventory + this cluster's existing-container view + the rich
// per-node detail. ownContainers is this cluster's own WekaContainers (the controller passes its cached
// set; the CLI supplies discovery.GetClusterContainers(...)). cons carries the sizing knobs used to
// charge drive/compute footprints.
func (c Collector) Collect(ctx context.Context, cluster *weka.WekaCluster, ownContainers []*weka.WekaContainer, cons *capacityplanner.CapacityConstraints) (Result, error) {
	fdByNode, inv, computeNodes, err := c.NodeInventory(ctx, cluster, ownContainers, cons)
	if err != nil {
		return Result{}, err
	}
	nodes, err := c.nodeDetails(ctx, cluster, ownContainers, cons, inv, fdByNode)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Inventory:       inv,
		ExistingDrives:  ExistingDrives(ctx, cluster, ownContainers, fdByNode),
		ExistingCompute: ExistingCompute(ctx, ownContainers),
		FDByNode:        fdByNode,
		ComputeNodes:    computeNodes,
		Nodes:           nodes,
	}, nil
}

// NodeInventory returns (fdByNode, inventory, computeNodes). fdByNode maps every drive candidate node to
// its FD key (resolved label value in label-based mode, else the node name = AUTO/FD-per-host). inventory
// is the UNION of drive candidates (nodes with usable shared-drive capacity) and compute candidates
// (nodes matching the compute role selector — which may be diskless), with capacity/cores/hugepages/memory
// NET of every weka drive container already on the node (other clusters AND this cluster's own).
// computeNodes marks which inventory nodes the compute layout may use; it is always non-nil.
func (c Collector) NodeInventory(ctx context.Context, cluster *weka.WekaCluster, ownContainers []*weka.WekaContainer, cons *capacityplanner.CapacityConstraints) (fds map[string]string, inv []capacityplanner.NodeCapacity, eligible map[string]bool, err error) {
	driveSelector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeDrive)
	computeSelector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeCompute)

	driveNodes, err := listNodesForSelector(ctx, c.Client, driveSelector)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("NodeInventory: %w", err)
	}
	computeNodeList := driveNodes // equal selectors: list once and reuse
	if !maps.Equal(driveSelector, computeSelector) {
		if computeNodeList, err = listNodesForSelector(ctx, c.Client, computeSelector); err != nil {
			return nil, nil, nil, fmt.Errorf("NodeInventory: %w", err)
		}
	}

	consumed, err := c.consumedNodeResources(ctx, cons)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("NodeInventory: %w", err)
	}

	fdConfig := cluster.Spec.FailureDomain

	// Nodes still hosting THIS cluster's drive container being deleted. Such a container is excluded from
	// existingDrives AND from the node resource charge, so the node re-enters the fresh-candidate pool with
	// its footprint freed. Flagging the drive entry still lets the planner deprioritize the node for fresh
	// placement (so a replacement FD is not recreated on the node it was just deleted from).
	deletingDriveNodes := map[string]bool{}
	for _, cont := range ownContainers {
		if cont.Spec.Mode == weka.WekaContainerModeDrive && cont.IsMarkedForDeletion() {
			if n := string(cont.GetNodeAffinity()); n != "" {
				deletingDriveNodes[n] = true
			}
		}
	}

	headroom := func(node *corev1.Node) (cpu, hugepagesMiB, memoryMiB int) {
		cpu = max(0, int(node.Status.Allocatable.Cpu().Value())-consumed.cores[node.Name])
		hugepagesMiB = max(0, nodeAllocatableHugepagesMiB(node)-consumed.hugepages[node.Name])
		memoryMiB = max(0, nodeAllocatableMemoryMiB(node)-consumed.memory[node.Name])
		return cpu, hugepagesMiB, memoryMiB
	}

	// Drive candidates: nodes with usable shared-drive capacity, carrying TLC/QLC headroom and an FD key.
	fdByNode := map[string]string{}
	var driveInv []capacityplanner.NodeCapacity
	for i := range driveNodes {
		node := &driveNodes[i]
		nodeName := weka.NodeName(node.Name)

		fdValue, skip := resolveInventoryFDValue(node, fdConfig)
		if skip {
			continue
		}

		info, err := allocator.ParseAllocatorNodeInfo(node)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("NodeInventory: reading node info for %q: %w", nodeName, err)
		}

		physTLC, physQLC := sumSharedDriveCapacity(info.SharedDrives)
		if physTLC == 0 && physQLC == 0 {
			continue // no usable shared-drive capacity (may still be a compute candidate below)
		}
		fdByNode[node.Name] = fdValue

		// Keep fully drive-consumed nodes in the inventory (TlcGiB/QlcGiB clamp to 0) so compute sizing
		// still sees their CPU/hugepage headroom.
		tlcGiB := max(0, physTLC-consumed.tlc[node.Name])
		qlcGiB := max(0, physQLC-consumed.qlc[node.Name])
		cpu, hugepagesMiB, memoryMiB := headroom(node)
		driveInv = append(driveInv, capacityplanner.NodeCapacity{
			NodeName:                  node.Name,
			FDValue:                   fdValue,
			TlcGiB:                    tlcGiB,
			QlcGiB:                    qlcGiB,
			AllocatableCPU:            cpu,
			AvailableHugepagesMiB:     hugepagesMiB,
			AvailableMemoryMiB:        memoryMiB,
			HasDeletingDriveContainer: deletingDriveNodes[node.Name],
		})
	}

	// Compute candidates: every node matching the compute selector, with zero drive capacity.
	var computeInv []capacityplanner.NodeCapacity
	for i := range computeNodeList {
		node := &computeNodeList[i]
		fdValue, skip := resolveInventoryFDValue(node, fdConfig)
		if skip {
			continue
		}
		cpu, hugepagesMiB, memoryMiB := headroom(node)
		computeInv = append(computeInv, capacityplanner.NodeCapacity{
			NodeName:              node.Name,
			FDValue:               fdValue,
			AllocatableCPU:        cpu,
			AvailableHugepagesMiB: hugepagesMiB,
			AvailableMemoryMiB:    memoryMiB,
		})
	}

	inventory, computeNodes := mergeRoleNodes(driveInv, computeInv)
	return fdByNode, inventory, computeNodes, nil
}

// ExploreNodes builds a per-node NodeDetail view for every node matching selector, independent of any
// WekaCluster — the payload the weka-capacity `explore-nodes` command renders. FD keys resolve via
// fdConfig (label-based) or fall back to the node name (AUTO); in label-based mode a node without the FD
// label is skipped (belongs to no FD). Free headroom is each node's physical / allocatable resources
// minus the footprint of every WekaContainer charged to it (all clusters, all modes).
func (c Collector) ExploreNodes(ctx context.Context, selector map[string]string, fdConfig *weka.FailureDomain, cons *capacityplanner.CapacityConstraints) ([]NodeDetail, error) {
	nodes, err := listNodesForSelector(ctx, c.Client, selector)
	if err != nil {
		return nil, fmt.Errorf("ExploreNodes: %w", err)
	}
	kubeService := kubernetes.NewKubeService(c.Client)
	allContainers, err := kubeService.GetWekaContainersSimple(ctx, "", "", nil)
	if err != nil {
		return nil, fmt.Errorf("ExploreNodes: listing weka containers: %w", err)
	}
	used := aggregateContainerResources(allContainers, cons)
	consumersByNode := map[string][]Consumer{}
	for i := range allContainers {
		wc := &allContainers[i]
		node := string(wc.GetNodeAffinity())
		if node == "" {
			continue
		}
		consumersByNode[node] = append(consumersByNode[node], consumerFrom(wc, cons))
	}

	out := make([]NodeDetail, 0, len(nodes))
	for i := range nodes {
		node := &nodes[i]
		fdValue, skip := resolveInventoryFDValue(node, fdConfig)
		if skip {
			continue
		}
		var physTLC, physQLC int
		if info, perr := allocator.ParseAllocatorNodeInfo(node); perr == nil {
			physTLC, physQLC = sumSharedDriveCapacity(info.SharedDrives)
		}
		allocCPU := int(node.Status.Allocatable.Cpu().Value())
		allocHP := nodeAllocatableHugepagesMiB(node)
		allocMem := nodeAllocatableMemoryMiB(node)
		out = append(out, NodeDetail{
			Node:                    node.Name,
			FDValue:                 fdValue,
			PhysTlcGiB:              physTLC,
			PhysQlcGiB:              physQLC,
			UsedTlcGiB:              used.tlc[node.Name],
			UsedQlcGiB:              used.qlc[node.Name],
			UsedCores:               used.cores[node.Name],
			UsedHugepagesMiB:        used.hugepages[node.Name],
			UsedMemoryMiB:           used.memory[node.Name],
			AllocatableCores:        allocCPU,
			AllocatableHugepagesMiB: allocHP,
			AllocatableMemoryMiB:    allocMem,
			FreeTlcGiB:              max(0, physTLC-used.tlc[node.Name]),
			FreeQlcGiB:              max(0, physQLC-used.qlc[node.Name]),
			FreeCores:               max(0, allocCPU-used.cores[node.Name]),
			FreeHugepagesMiB:        max(0, allocHP-used.hugepages[node.Name]),
			FreeMemoryMiB:           max(0, allocMem-used.memory[node.Name]),
			IsDriveCandidate:        physTLC > 0 || physQLC > 0,
			Consumers:               consumersByNode[node.Name],
		})
	}
	return out, nil
}

// ExistingDrives builds the planner's view of this cluster's healthy drive containers.
func ExistingDrives(ctx context.Context, cluster *weka.WekaCluster, ownContainers []*weka.WekaContainer, fdByNode map[string]string) []capacityplanner.ExistingContainer {
	fdConfig := cluster.Spec.FailureDomain
	var existingDrives []capacityplanner.ExistingContainer
	for _, c := range ownContainers {
		if c.Spec.Mode != weka.WekaContainerModeDrive {
			continue
		}
		if unhealthy, _, _ := utils.IsUnhealthy(ctx, c); unhealthy { //nolint:errcheck // intentional
			continue
		}
		node := string(c.GetNodeAffinity())
		fd := fdByNode[node]
		if fd == "" && fdConfig == nil {
			fd = node // auto mode: one failure domain per host
		}

		tlcGiB, qlcGiB := DriveContainerCapacities(c)

		existingDrives = append(existingDrives, capacityplanner.ExistingContainer{
			Name:        c.Name,
			Node:        node,
			FDValue:     fd,
			TlcGiB:      tlcGiB,
			QlcGiB:      qlcGiB,
			NumCores:    c.Spec.NumCores,
			Unscheduled: c.Status.NodeAffinity == "",
		})
	}
	return existingDrives
}

// ExistingCompute builds the planner's view of this cluster's healthy compute containers.
func ExistingCompute(ctx context.Context, ownContainers []*weka.WekaContainer) []capacityplanner.ExistingComputeContainer {
	var existing []capacityplanner.ExistingComputeContainer
	for _, c := range ownContainers {
		if c.Spec.Mode != weka.WekaContainerModeCompute {
			continue
		}
		if unhealthy, _, _ := utils.IsUnhealthy(ctx, c); unhealthy { //nolint:errcheck // intentional
			continue
		}
		existing = append(existing, capacityplanner.ExistingComputeContainer{
			Name:         c.Name,
			Node:         string(c.GetNodeAffinity()),
			NumCores:     c.Spec.NumCores,
			HugepagesMiB: c.Spec.Hugepages,
			Unscheduled:  c.Status.NodeAffinity == "",
		})
	}
	return existing
}

// nodeResources holds per-node resource consumption across all weka containers.
type nodeResources struct {
	tlc, qlc, cores, hugepages, memory map[string]int
}

// consumedNodeResources returns, per node, the TLC/QLC shared-drive capacity, CPU cores, hugepages (MiB)
// and memory (MiB) already claimed by EVERY WekaContainer scheduled or pinned to the node (all modes,
// both other clusters' and this cluster's own).
func (c Collector) consumedNodeResources(ctx context.Context, cons *capacityplanner.CapacityConstraints) (nodeResources, error) {
	kubeService := kubernetes.NewKubeService(c.Client)
	containers, err := kubeService.GetWekaContainersSimple(ctx, "", "", nil)
	if err != nil {
		return nodeResources{}, fmt.Errorf("listing weka containers: %w", err)
	}
	return aggregateContainerResources(containers, cons), nil
}

// aggregateContainerResources sums, per node, the resource footprint of weka containers by mode (skipping
// containers marked for deletion — their resources are about to be freed):
//   - drive (drive-sharing only): cores/hugepages/memory derived from per-pool CAPACITY via the shared
//     sizing model (capacityplanner.RequiredDriveResources), plus TLC/QLC capacity.
//   - compute: cores/hugepages from spec; memory from the shared base+per-core model.
//   - other modes (e.g. ssdproxy): cores and 2Mi hugepages from spec; memory not charged.
func aggregateContainerResources(containers []weka.WekaContainer, cons *capacityplanner.CapacityConstraints) nodeResources {
	res := nodeResources{
		tlc:       map[string]int{},
		qlc:       map[string]int{},
		cores:     map[string]int{},
		hugepages: map[string]int{},
		memory:    map[string]int{},
	}
	for i := range containers {
		c := &containers[i]
		if c.IsMarkedForDeletion() {
			continue
		}
		node := string(c.GetNodeAffinity())
		if node == "" {
			continue
		}
		switch c.Spec.Mode {
		case weka.WekaContainerModeDrive:
			if !c.UsesDriveSharing() {
				continue
			}
			t, q := DriveContainerCapacities(c)
			cores, hugepagesMiB, memoryMiB := capacityplanner.RequiredDriveResources(t, q, cons)
			res.cores[node] += cores
			res.hugepages[node] += hugepagesMiB
			res.memory[node] += memoryMiB
			res.tlc[node] += t
			res.qlc[node] += q
		case weka.WekaContainerModeCompute:
			res.cores[node] += c.Spec.NumCores
			res.hugepages[node] += spec2MiHugepages(c)
			res.memory[node] += capacityplanner.ComputeMemoryFootprintMiB(c.Spec.NumCores, cons)
		default:
			res.cores[node] += c.Spec.NumCores
			res.hugepages[node] += spec2MiHugepages(c)
		}
	}
	return res
}

// nodeDetails builds the per-node NodeDetail slice for explore-nodes from the drive-candidate inventory,
// the node's physical shared-drive capacity, and the per-node consuming WekaContainers. It re-lists all
// weka containers (CLI path only; not on the controller's hot reconcile path).
func (c Collector) nodeDetails(ctx context.Context, cluster *weka.WekaCluster, ownContainers []*weka.WekaContainer, cons *capacityplanner.CapacityConstraints, inv []capacityplanner.NodeCapacity, fdByNode map[string]string) ([]NodeDetail, error) {
	kubeService := kubernetes.NewKubeService(c.Client)
	allContainers, err := kubeService.GetWekaContainersSimple(ctx, "", "", nil)
	if err != nil {
		return nil, fmt.Errorf("nodeDetails: listing weka containers: %w", err)
	}

	// Per-node consumers + per-resource used totals (net of marked-for-deletion, mirroring the planner).
	consumersByNode := map[string][]Consumer{}
	used := aggregateContainerResources(allContainers, cons)
	for i := range allContainers {
		wc := &allContainers[i]
		node := string(wc.GetNodeAffinity())
		if node == "" {
			continue
		}
		consumersByNode[node] = append(consumersByNode[node], consumerFrom(wc, cons))
	}

	deletingDriveNodes := map[string]bool{}
	for _, cont := range ownContainers {
		if cont.Spec.Mode == weka.WekaContainerModeDrive && cont.IsMarkedForDeletion() {
			if n := string(cont.GetNodeAffinity()); n != "" {
				deletingDriveNodes[n] = true
			}
		}
	}

	// Index the planner inventory by node for free-headroom + FD lookup.
	invByNode := make(map[string]capacityplanner.NodeCapacity, len(inv))
	for _, nc := range inv {
		invByNode[nc.NodeName] = nc
	}

	// Physical shared-drive capacity per drive-candidate node (from the annotation).
	driveSelector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeDrive)
	computeSelector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeCompute)
	nodes, err := listNodesForSelector(ctx, c.Client, driveSelector)
	if err != nil {
		return nil, fmt.Errorf("nodeDetails: %w", err)
	}
	if !maps.Equal(driveSelector, computeSelector) {
		computeOnly, err := listNodesForSelector(ctx, c.Client, computeSelector)
		if err != nil {
			return nil, fmt.Errorf("nodeDetails: %w", err)
		}
		nodes = append(nodes, computeOnly...)
	}

	seen := map[string]struct{}{}
	var out []NodeDetail
	for i := range nodes {
		node := &nodes[i]
		if _, dup := seen[node.Name]; dup {
			continue
		}
		seen[node.Name] = struct{}{}

		var physTLC, physQLC int
		if info, perr := allocator.ParseAllocatorNodeInfo(node); perr == nil {
			physTLC, physQLC = sumSharedDriveCapacity(info.SharedDrives)
		}

		nc, inInv := invByNode[node.Name]
		d := NodeDetail{
			Node:                      node.Name,
			FDValue:                   fdByNode[node.Name],
			PhysTlcGiB:                physTLC,
			PhysQlcGiB:                physQLC,
			UsedTlcGiB:                used.tlc[node.Name],
			UsedQlcGiB:                used.qlc[node.Name],
			UsedCores:                 used.cores[node.Name],
			UsedHugepagesMiB:          used.hugepages[node.Name],
			UsedMemoryMiB:             used.memory[node.Name],
			AllocatableCores:          int(node.Status.Allocatable.Cpu().Value()),
			AllocatableHugepagesMiB:   nodeAllocatableHugepagesMiB(node),
			AllocatableMemoryMiB:      nodeAllocatableMemoryMiB(node),
			HasDeletingDriveContainer: deletingDriveNodes[node.Name],
			IsDriveCandidate:          physTLC > 0 || physQLC > 0,
			Consumers:                 consumersByNode[node.Name],
		}
		if d.FDValue == "" {
			// Node not in fdByNode (compute-only or drive-empty): resolve its FD key for display.
			if fdv, skip := resolveInventoryFDValue(node, cluster.Spec.FailureDomain); !skip {
				d.FDValue = fdv
			}
		}
		if inInv {
			d.FreeTlcGiB = nc.TlcGiB
			d.FreeQlcGiB = nc.QlcGiB
			d.FreeCores = nc.AllocatableCPU
			d.FreeHugepagesMiB = nc.AvailableHugepagesMiB
			d.FreeMemoryMiB = nc.AvailableMemoryMiB
		} else {
			d.FreeTlcGiB = max(0, physTLC-used.tlc[node.Name])
			d.FreeQlcGiB = max(0, physQLC-used.qlc[node.Name])
			d.FreeCores = max(0, d.AllocatableCores-used.cores[node.Name])
			d.FreeHugepagesMiB = max(0, d.AllocatableHugepagesMiB-used.hugepages[node.Name])
			d.FreeMemoryMiB = max(0, d.AllocatableMemoryMiB-used.memory[node.Name])
		}
		out = append(out, d)
	}
	return out, nil
}

// consumerFrom builds a Consumer describing how the collector charges one WekaContainer.
func consumerFrom(c *weka.WekaContainer, cons *capacityplanner.CapacityConstraints) Consumer {
	out := Consumer{
		Name:              c.Name,
		Namespace:         c.Namespace,
		Cluster:           owningClusterName(c),
		Role:              string(c.Spec.Mode),
		MarkedForDeletion: c.IsMarkedForDeletion(),
	}
	switch c.Spec.Mode {
	case weka.WekaContainerModeDrive:
		if !c.UsesDriveSharing() {
			return out
		}
		t, q := DriveContainerCapacities(c)
		cores, hp, mem := capacityplanner.RequiredDriveResources(t, q, cons)
		out.TlcGiB, out.QlcGiB, out.Cores, out.HugepagesMiB, out.MemoryMiB = t, q, cores, hp, mem
		out.NilRatio = c.Spec.DriveCapacity <= 0 && c.Spec.DriveTypesRatio == nil
	case weka.WekaContainerModeCompute:
		out.Cores = c.Spec.NumCores
		out.HugepagesMiB = spec2MiHugepages(c)
		out.MemoryMiB = capacityplanner.ComputeMemoryFootprintMiB(c.Spec.NumCores, cons)
	default:
		out.Cores = c.Spec.NumCores
		out.HugepagesMiB = spec2MiHugepages(c)
	}
	return out
}

// owningClusterName returns the name of the WekaCluster that owns c, or "" when none.
func owningClusterName(c *weka.WekaContainer) string {
	for _, ref := range c.OwnerReferences {
		if ref.Kind == "WekaCluster" {
			return ref.Name
		}
	}
	return ""
}

// DriveContainerCapacities returns a drive container's per-pool capacity (GiB) from its spec: legacy
// driveCapacity×numDrives is TLC-only, otherwise containerCapacity split by driveTypesRatio (a zero
// containerCapacity yields (0,0)). Single source of truth for the controller's and CLI's capacity reads.
func DriveContainerCapacities(c *weka.WekaContainer) (tlcGiB, qlcGiB int) {
	if c.Spec.DriveCapacity > 0 {
		return c.Spec.DriveCapacity * c.Spec.NumDrives, 0
	}
	return weka.GetTlcQlcCapacity(c.Spec.ContainerCapacity, c.Spec.DriveTypesRatio)
}

// spec2MiHugepages returns the container's 2Mi hugepage request (MiB). The planner's headroom tracks
// hugepages-2Mi only; a container reserving 1Gi hugepages draws from a distinct pool.
func spec2MiHugepages(c *weka.WekaContainer) int {
	if c.Spec.Hugepages <= 0 || c.Spec.HugepagesSize == "1Gi" {
		return 0
	}
	return c.Spec.Hugepages
}

// sumSharedDriveCapacity totals a node's shared-drive annotation entries by drive type.
func sumSharedDriveCapacity(drives []domain.SharedDriveInfo) (tlcGiB, qlcGiB int) {
	for _, sd := range drives {
		switch sd.Type {
		case "TLC":
			tlcGiB += sd.CapacityGiB
		case "QLC":
			qlcGiB += sd.CapacityGiB
		}
	}
	return tlcGiB, qlcGiB
}

// resolveInventoryFDValue resolves a node's failure-domain key and reports whether the node must be
// skipped. In label-based mode (fdConfig != nil) the FD key is the resolved label value, and a node
// carrying no FD label belongs to no failure domain and is skipped. In AUTO mode (fdConfig == nil) every
// host is its own FD, so the key falls back to the node name.
func resolveInventoryFDValue(node *corev1.Node, fdConfig *weka.FailureDomain) (fdValue string, skip bool) {
	fdValue = allocator.ResolveNodeFDValue(node, fdConfig)
	if fdConfig != nil && fdValue == "" {
		return "", true
	}
	if fdValue == "" {
		fdValue = node.Name
	}
	return fdValue, false
}

// mergeRoleNodes unions the drive-candidate and compute-candidate node sets into one planner inventory and
// the compute-eligibility map. A node present in both keeps its drive entry (real TLC/QLC capacity); a
// compute-only node is appended with zero drive capacity so drive placement skips it while compute sizing
// can still use it. Every compute candidate is marked eligible.
func mergeRoleNodes(driveInv, computeInv []capacityplanner.NodeCapacity) (inventory []capacityplanner.NodeCapacity, computeNodes map[string]bool) {
	inventory = append([]capacityplanner.NodeCapacity(nil), driveInv...)
	index := make(map[string]struct{}, len(inventory))
	for _, nc := range inventory {
		index[nc.NodeName] = struct{}{}
	}
	computeNodes = make(map[string]bool, len(computeInv))
	for _, nc := range computeInv {
		computeNodes[nc.NodeName] = true
		if _, ok := index[nc.NodeName]; !ok {
			index[nc.NodeName] = struct{}{}
			inventory = append(inventory, nc)
		}
	}
	return inventory, computeNodes
}

// listNodesForSelector lists candidate nodes filtered by a role node selector. An empty selector matches
// every node in the cluster (standard Kubernetes label-selector semantics).
func listNodesForSelector(ctx context.Context, c client.Client, selector map[string]string) ([]corev1.Node, error) {
	listOpts := []client.ListOption{}
	if len(selector) > 0 {
		listOpts = append(listOpts, client.MatchingLabels(selector))
	}
	nodeList := &corev1.NodeList{}
	if err := c.List(ctx, nodeList, listOpts...); err != nil {
		return nil, fmt.Errorf("listNodesForSelector: failed to list nodes: %w", err)
	}
	return nodeList.Items, nil
}

// nodeAllocatableHugepagesMiB returns the node's allocatable 2Mi hugepages in MiB.
func nodeAllocatableHugepagesMiB(node *corev1.Node) int {
	name := corev1.ResourceName(string(corev1.ResourceHugePagesPrefix) + "2Mi")
	q := node.Status.Allocatable[name]
	return int(q.Value() / (1 << 20))
}

// nodeAllocatableMemoryMiB returns the node's allocatable memory in MiB.
func nodeAllocatableMemoryMiB(node *corev1.Node) int {
	q := node.Status.Allocatable[corev1.ResourceMemory]
	return int(q.Value() / (1 << 20))
}
