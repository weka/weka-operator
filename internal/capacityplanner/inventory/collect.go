// Package inventory collects, from Kubernetes, the per-node headroom view and this cluster's
// existing-container view for the pure capacity planner (internal/capacityplanner), plus rich
// per-node detail (NodeDetail) for explore-nodes/feasibility narration. Every WekaContainer and
// foreign pod is charged against node headroom exactly once; an auto-full-drives drive container charges from its spec, not its pod, so pending growth is never read as free headroom.
package inventory

import (
	"context"
	"fmt"
	"maps"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// Collector reads Kubernetes to build the capacity-planner inputs from one controller-runtime client. Every
// List it issues passes UnsafeDisableDeepCopy (contract in chargeForeignPods): helpers may only read the
// objects they are handed, and must not retain one.
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
	// NilRatio flags a drive container whose DriveTypesRatio is nil, so ContainerCapacity is attributed
	// 100% to TLC (weka.GetTlcQlcCapacity) — surfaced so explore-nodes can warn about it.
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
	// IneligibleReason is "cordoned"/"not ready"/"untolerated taint" when the node cannot currently
	// schedule a weka pod, "" when it can. Planner node lists still include ineligible nodes (their
	// existing capacity/containers must keep being accounted for); new placement is gated separately
	// via capacityplanner.NodeCapacity.IneligibleReason, not by omission here.
	IneligibleReason string

	// Mode is which drive-capacity model the node is signed under: "shared" (weka-shared-drives, matches
	// IsDriveCandidate), "full" (any signed weka-full-drives entry, free or allocated), or "-" for
	// neither; never both (allocator.ParseAllocatorNodeInfo). PhysTlcGiB/FreeTlcGiB stay 0 for "full" —
	// check the Full* fields instead.
	Mode string
	// Full-drives (auto-full-drives) capacity from the weka-full-drives annotation, meaningful when Mode == "full".
	// Phys* is the node's TOTAL signed full drives (free+allocated, any cluster/mode); Free* is the free
	// subset. Computed here rather than via FullDrivesInventory, which skips a node with zero free drives
	// (would hide "0/6 free, 6 total").
	FreeFullDriveCount int
	PhysFullDriveCount int
	FreeFullTlcGiB     int
	PhysFullTlcGiB     int
	// BlockedFullDriveCount is the serial count in weka.io/blocked-drives — already excluded from
	// PhysFullDriveCount/PhysFullTlcGiB, surfaced so an operator can confirm the annotation took effect.
	// Best-effort: a missing/malformed annotation reports 0.
	BlockedFullDriveCount int
	// FreeFullDriveCapacitiesGiB/ClaimedFullDriveCapacitiesGiB are the per-drive capacities (GiB) behind
	// FreeFullTlcGiB/PhysFullTlcGiB, sorted largest-first (auto-full-drives's consumption order), e.g.
	// "5x14.0TiB+1x7.0TiB". Meaningful only when Mode == "full"; render.go formats these raw ints.
	FreeFullDriveCapacitiesGiB    []int
	ClaimedFullDriveCapacitiesGiB []int
}

// Collect runs the full collection: node inventory + this cluster's existing-container view + the rich
// per-node detail. ownContainers is this cluster's own WekaContainers (controller: cached set; CLI:
// discovery.GetClusterContainers(...)). cons carries the sizing knobs. Collect is CLI-only: unlike
// NodeInventory, it lists nodes/containers once and shares them across nodeInventoryFromLists/nodeDetailsFromLists.
func (c Collector) Collect(ctx context.Context, cluster *weka.WekaCluster, ownContainers []*weka.WekaContainer, cons *capacityplanner.CapacityConstraints) (Result, error) {
	driveNodes, computeNodeList, topos, containers, err := c.listInventoryInputs(ctx, cluster, "Collect")
	if err != nil {
		return Result{}, err
	}
	fdByNode, inv, computeNodes, err := c.nodeInventoryFromLists(ctx, cluster, ownContainers, cons, driveNodes, computeNodeList, topos, containers)
	if err != nil {
		return Result{}, err
	}
	nodes, err := c.nodeDetailsFromLists(ctx, cluster, ownContainers, cons, inv, fdByNode, driveNodes, computeNodeList, topos, containers)
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

// listRoleNodesAndTopos resolves the drive/compute role node lists and their CPU topology map; shared by
// NodeInventory and FullDrivesInventory, which differ only in the error-message prefix. Both lists include
// every matching node whether currently schedulable or not — an ineligible node still carries capacity
// that must be accounted for; callers set IneligibleReason per node to bar it from new placement instead.
func (c Collector) listRoleNodesAndTopos(ctx context.Context, cluster *weka.WekaCluster, errPrefix string) (driveNodes, computeNodeList []corev1.Node, topos map[string]capacityplanner.NodeCPUTopology, err error) {
	driveSelector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeDrive)
	computeSelector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeCompute)

	driveNodes, err = listNodesForSelector(ctx, c.Client, driveSelector)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", errPrefix, err)
	}
	computeNodeList = driveNodes // equal selectors: list once and reuse
	if !maps.Equal(driveSelector, computeSelector) {
		if computeNodeList, err = listNodesForSelector(ctx, c.Client, computeSelector); err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", errPrefix, err)
		}
	}

	// Per-node CPU topology, union of both role node sets. computeNodeList aliases driveNodes when the
	// selectors match, so add compute-only nodes only when they differ (avoids re-parsing discovery.json
	// twice on the reconcile path).
	topos = buildNodeTopos(driveNodes)
	if !maps.Equal(driveSelector, computeSelector) {
		for i := range computeNodeList {
			if _, ok := topos[computeNodeList[i].Name]; !ok {
				topos[computeNodeList[i].Name] = nodeCPUTopology(&computeNodeList[i])
			}
		}
	}
	return driveNodes, computeNodeList, topos, nil
}

// NodeInventory returns (fdByNode, inventory, computeNodes). fdByNode maps every drive candidate node to
// its FD key (label value in label-based mode, else the node name in AUTO/FD-per-host mode). inventory is
// the union of drive candidates and compute candidates, net of every weka drive container already on the
// node (any cluster, including this one). computeNodes (always non-nil) marks which nodes compute may use.
func (c Collector) NodeInventory(ctx context.Context, cluster *weka.WekaCluster, ownContainers []*weka.WekaContainer, cons *capacityplanner.CapacityConstraints) (fds map[string]string, inv []capacityplanner.NodeCapacity, eligible map[string]bool, err error) {
	driveNodes, computeNodeList, topos, containers, err := c.listInventoryInputs(ctx, cluster, "NodeInventory")
	if err != nil {
		return nil, nil, nil, err
	}
	return c.nodeInventoryFromLists(ctx, cluster, ownContainers, cons, driveNodes, computeNodeList, topos, containers)
}

// nodeInventoryFromLists is NodeInventory's core, decoupled from K8s listing so Collect can share one
// node/container-listing pass across the inventory build and nodeDetailsFromLists instead of each
// independently re-listing.
func (c Collector) nodeInventoryFromLists(ctx context.Context, cluster *weka.WekaCluster, ownContainers []*weka.WekaContainer, cons *capacityplanner.CapacityConstraints, driveNodes, computeNodeList []corev1.Node, topos map[string]capacityplanner.NodeCPUTopology, containers []weka.WekaContainer) (fds map[string]string, inv []capacityplanner.NodeCapacity, eligible map[string]bool, err error) {
	fdConfig := cluster.Spec.FailureDomain

	consumed, err := c.consumedNodeResources(ctx, containers, cons, topos)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("NodeInventory: %w", err)
	}

	tolerations := resources.GetWekaPodTolerationsForCluster(cluster)

	// Nodes hosting THIS cluster's drive container being deleted: excluded from existingDrives, so its
	// TLC/QLC capacity re-enters the fresh-candidate pool — but while its pod lives, cores/hugepages/memory
	// stay charged via chargeForeignPods. Flagged so the planner deprioritizes fresh placement.
	deletingDriveNodes := NodesWithDeletingDriveContainer(ownContainers)
	// Nodes hosting THIS cluster's compute container being deleted: its pod still holds hugepages/CPU/memory
	// (charged via chargeForeignPods), so a drive container on the same node can fail a fit it would pass
	// once the deletion lands. Flagged so the auto-full-drives walk defers rather than fails the plan.
	deletingComputeNodes := NodesWithDeletingComputeContainer(ownContainers)

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
		cpu, hugepagesMiB, memoryMiB := nodeHeadroom(node, consumed)
		topo := topos[node.Name]
		driveInv = append(driveInv, capacityplanner.NodeCapacity{
			NodeName:                    node.Name,
			FDValue:                     fdValue,
			TlcGiB:                      tlcGiB,
			QlcGiB:                      qlcGiB,
			AllocatableCPU:              cpu,
			AvailableHugepagesMiB:       hugepagesMiB,
			AvailableMemoryMiB:          memoryMiB,
			IsHt:                        topo.IsHt,
			FullPcpusOnly:               topo.FullPcpusOnly,
			HasDeletingDriveContainer:   deletingDriveNodes[node.Name],
			HasDeletingComputeContainer: deletingComputeNodes[node.Name],
			IneligibleReason:            resources.NodeIneligibleReason(node, tolerations),
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
		cpu, hugepagesMiB, memoryMiB := nodeHeadroom(node, consumed)
		topo := topos[node.Name]
		computeInv = append(computeInv, capacityplanner.NodeCapacity{
			NodeName:              node.Name,
			FDValue:               fdValue,
			AllocatableCPU:        cpu,
			AvailableHugepagesMiB: hugepagesMiB,
			AvailableMemoryMiB:    memoryMiB,
			IsHt:                  topo.IsHt,
			FullPcpusOnly:         topo.FullPcpusOnly,
			IneligibleReason:      resources.NodeIneligibleReason(node, tolerations),
		})
	}

	inventory, computeNodes := mergeRoleNodes(driveInv, computeInv)
	return fdByNode, inventory, computeNodes, nil
}

// FullDrivesInventory is NodeInventory's auto-full-drives counterpart: same drive/compute-candidate +
// mergeRoleNodes shape, but reads info.AvailableDrives instead of info.SharedDrives. Consumed by
// PlanAutoFullDrives, not PlanCapacity. It duplicates NodeInventory's candidate-building rather than
// sharing it, to keep the clusterCapacity and auto-full-drives paths isolated; nodeHeadroom, listInventoryInputs, and resources.NodeIneligibleReason are the mode-agnostic pieces shared between them.
func (c Collector) FullDrivesInventory(ctx context.Context, cluster *weka.WekaCluster, ownContainers []*weka.WekaContainer, cons *capacityplanner.CapacityConstraints) (fds map[string]string, inv []capacityplanner.NodeCapacity, eligible map[string]bool, err error) {
	driveNodes, computeNodeList, topos, containers, err := c.listInventoryInputs(ctx, cluster, "FullDrivesInventory")
	if err != nil {
		return nil, nil, nil, err
	}

	fdConfig := cluster.Spec.FailureDomain

	consumed, err := c.consumedNodeResources(ctx, containers, cons, topos)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("FullDrivesInventory: %w", err)
	}
	// info.AvailableDrives carries no allocation exclusion of its own (unlike info.SharedDrives), so the
	// drive loop below subtracts what any WekaContainer already holds.
	allocatedDrives := allocatedNodeDrives(containers)
	tolerations := resources.GetWekaPodTolerationsForCluster(cluster)

	deletingDriveNodes := NodesWithDeletingDriveContainer(ownContainers)
	deletingComputeNodes := NodesWithDeletingComputeContainer(ownContainers)

	// ownDriveSerials is, per node, the serials allocated to THIS cluster's own drive container that still
	// holds them, using the same IsDeletingDriveContainer predicate as ExistingDrives (see that function
	// for the invariant). A container that no longer holds its drives keeps them charged via
	// allocatedDrives, so they are neither offered as free nor double-counted as a growth base.
	ownDriveSerials := map[string]map[string]bool{}
	for _, cont := range ownContainers {
		if cont.Spec.Mode != weka.WekaContainerModeDrive || IsDeletingDriveContainer(cont) {
			continue
		}
		if cont.Status.Allocations == nil {
			continue
		}
		node := string(cont.GetNodeAffinity())
		if node == "" {
			continue
		}
		set, ok := ownDriveSerials[node]
		if !ok {
			set = map[string]bool{}
			ownDriveSerials[node] = set
		}
		for _, drive := range cont.Status.Allocations.Drives {
			set[drive] = true
		}
	}

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
			return nil, nil, nil, fmt.Errorf("FullDrivesInventory: reading node info for %q: %w", nodeName, err)
		}

		// info.AvailableDrives carries no allocation exclusion of its own (unlike info.SharedDrives), so
		// exclude drives already committed to any WekaContainer here — otherwise PlanAutoFullDrives would request
		// unsatisfiable drives and the growth diff would re-fire forever.
		freeDrives := filterAllocatedDrives(info.AvailableDrives, allocatedDrives[node.Name])

		// ownDrives is the subset allocated to THIS cluster's own non-deleting container, disjoint from
		// freeDrives (so their sum recovers the node's true total); nil for a fresh-candidate node.
		// Computed before the emptiness check below, which needs it.
		ownDrives := selectDrivesBySerial(info.AvailableDrives, ownDriveSerials[node.Name])

		// Skip only when no full drives exist at all (neither free nor owned): len(freeDrives)==0 alone
		// would drop a fully-converged node, wrongly firing planAutoFullDrives's AutoFullDrivesNoSignedDrives guard and
		// losing OwnDriveCapacitiesGiB's hugepages term.
		if len(freeDrives) == 0 && len(ownDrives) == 0 {
			continue
		}
		fdByNode[node.Name] = fdValue
		driveCapacitiesGiB := fullDriveCapacities(freeDrives)
		tlcGiB := sumFullDriveCapacity(freeDrives)
		cpu, hugepagesMiB, memoryMiB := nodeHeadroom(node, consumed)
		topo := topos[node.Name]
		driveInv = append(driveInv, capacityplanner.NodeCapacity{
			NodeName:                    node.Name,
			FDValue:                     fdValue,
			TlcGiB:                      tlcGiB,
			QlcGiB:                      0,
			DriveCapacitiesGiB:          driveCapacitiesGiB,
			OwnDriveCapacitiesGiB:       fullDriveCapacities(ownDrives),
			AllocatableCPU:              cpu,
			AvailableHugepagesMiB:       hugepagesMiB,
			AvailableMemoryMiB:          memoryMiB,
			IsHt:                        topo.IsHt,
			FullPcpusOnly:               topo.FullPcpusOnly,
			HasDeletingDriveContainer:   deletingDriveNodes[node.Name],
			HasDeletingComputeContainer: deletingComputeNodes[node.Name],
			IneligibleReason:            resources.NodeIneligibleReason(node, tolerations),
		})
	}

	var computeInv []capacityplanner.NodeCapacity
	for i := range computeNodeList {
		node := &computeNodeList[i]
		fdValue, skip := resolveInventoryFDValue(node, fdConfig)
		if skip {
			continue
		}
		cpu, hugepagesMiB, memoryMiB := nodeHeadroom(node, consumed)
		topo := topos[node.Name]
		computeInv = append(computeInv, capacityplanner.NodeCapacity{
			NodeName:              node.Name,
			FDValue:               fdValue,
			AllocatableCPU:        cpu,
			AvailableHugepagesMiB: hugepagesMiB,
			AvailableMemoryMiB:    memoryMiB,
			IsHt:                  topo.IsHt,
			FullPcpusOnly:         topo.FullPcpusOnly,
			IneligibleReason:      resources.NodeIneligibleReason(node, tolerations),
		})
	}

	inventory, computeNodes := mergeRoleNodes(driveInv, computeInv)
	return fdByNode, inventory, computeNodes, nil
}

// ExploreNodes builds a per-node NodeDetail view for every node matching selector, independent of any
// WekaCluster — the payload the weka-capacity `explore-nodes` command renders. FD keys resolve via
// resolveInventoryFDValue. Free headroom is each node's physical/allocatable resources minus every
// WekaContainer charged to it (all clusters, all modes).
func (c Collector) ExploreNodes(ctx context.Context, selector map[string]string, fdConfig *weka.FailureDomain, cons *capacityplanner.CapacityConstraints) ([]NodeDetail, error) {
	nodes, err := listNodesForSelector(ctx, c.Client, selector)
	if err != nil {
		return nil, fmt.Errorf("ExploreNodes: %w", err)
	}
	allContainers, err := c.listAllWekaContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("ExploreNodes: listing weka containers: %w", err)
	}
	topos := buildNodeTopos(nodes)
	// Used/Free here are WekaContainer-only (matching Consumers): this is narration of "what weka
	// containers use", not a scheduling decision, so foreign pods are not folded in.
	used, _ := aggregateContainerResources(allContainers, cons, topos)
	consumersByNode := map[string][]Consumer{}
	for i := range allContainers {
		wc := &allContainers[i]
		node := string(wc.GetNodeAffinity())
		if node == "" {
			continue
		}
		consumersByNode[node] = append(consumersByNode[node], consumerFrom(wc, cons, topos[node]))
	}

	// Cluster-agnostic here, so (unlike FullDrivesInventory's per-cluster own/free split) only
	// total vs. allocated-by-anyone vs. free is needed.
	allocatedDrives := allocatedNodeDrives(allContainers)

	// ExploreNodes is cluster-agnostic, so eligibility here uses only the tolerations every weka pod gets,
	// never a specific cluster's extras — a node an actual cluster would tolerate may show ineligible here.
	// Nodes are still included either way: this is narration, not a candidate list.
	baseTolerations := resources.WekaPodBaseTolerations()

	out := make([]NodeDetail, 0, len(nodes))
	for i := range nodes {
		node := &nodes[i]
		fdValue, skip := resolveInventoryFDValue(node, fdConfig)
		if skip {
			continue
		}
		var physTLC, physQLC int
		var totalFullDrives, freeFullDrives, claimedFullDrives []domain.DriveEntry
		// info is always non-nil (ParseAllocatorNodeInfo initializes it before any error return), so
		// info.BlockedDriveCount below is valid even when perr != nil.
		info, perr := allocator.ParseAllocatorNodeInfo(node)
		if perr == nil {
			physTLC, physQLC = sumSharedDriveCapacity(info.SharedDrives)
			totalFullDrives = info.AvailableDrives
			freeFullDrives = filterAllocatedDrives(totalFullDrives, allocatedDrives[node.Name])
			claimedFullDrives = selectDrivesBySerial(totalFullDrives, allocatedDrives[node.Name])
		}
		mode := "-"
		switch {
		case physTLC > 0 || physQLC > 0:
			mode = "shared"
		case len(totalFullDrives) > 0:
			mode = "full"
		}
		allocCPU := int(node.Status.Allocatable.Cpu().Value())
		allocHP := nodeAllocatableHugepagesMiB(node)
		allocMem := nodeAllocatableMemoryMiB(node)
		out = append(out, NodeDetail{
			Node:                          node.Name,
			FDValue:                       fdValue,
			PhysTlcGiB:                    physTLC,
			PhysQlcGiB:                    physQLC,
			UsedTlcGiB:                    used.tlc[node.Name],
			UsedQlcGiB:                    used.qlc[node.Name],
			UsedCores:                     used.cores[node.Name],
			UsedHugepagesMiB:              used.hugepages[node.Name],
			UsedMemoryMiB:                 used.memory[node.Name],
			AllocatableCores:              allocCPU,
			AllocatableHugepagesMiB:       allocHP,
			AllocatableMemoryMiB:          allocMem,
			FreeTlcGiB:                    max(0, physTLC-used.tlc[node.Name]),
			FreeQlcGiB:                    max(0, physQLC-used.qlc[node.Name]),
			FreeCores:                     max(0, allocCPU-used.cores[node.Name]),
			FreeHugepagesMiB:              max(0, allocHP-used.hugepages[node.Name]),
			FreeMemoryMiB:                 max(0, allocMem-used.memory[node.Name]),
			IsDriveCandidate:              physTLC > 0 || physQLC > 0,
			Consumers:                     consumersByNode[node.Name],
			Mode:                          mode,
			FreeFullDriveCount:            len(freeFullDrives),
			PhysFullDriveCount:            len(totalFullDrives),
			FreeFullTlcGiB:                sumFullDriveCapacity(freeFullDrives),
			PhysFullTlcGiB:                sumFullDriveCapacity(totalFullDrives),
			BlockedFullDriveCount:         info.BlockedDriveCount,
			FreeFullDriveCapacitiesGiB:    capacityplanner.SortDriveCapacitiesDesc(fullDriveCapacities(freeFullDrives)),
			ClaimedFullDriveCapacitiesGiB: capacityplanner.SortDriveCapacitiesDesc(fullDriveCapacities(claimedFullDrives)),
			IneligibleReason:              resources.NodeIneligibleReason(node, baseTolerations),
		})
	}
	return out, nil
}

// IsDeletingDriveContainer reports whether c is a drive container on its way out. Such a container still
// physically holds its allocated drives — they stay out of the free pool via allocatedNodeDrives and out of
// the own pool via ownDriveSerials — but can neither be grown nor planned against.
//
// Every path that decides whether a drive container still holds its drives must ask through here:
// ExistingDrives, ownDriveSerials and summarizeDriveContainers each skip on it, and if they disagreed a
// node's drives would be invisible to both planner paths and the plan would silently under-size compute.
func IsDeletingDriveContainer(c *weka.WekaContainer) bool {
	return c.Spec.Mode == weka.WekaContainerModeDrive && c.IsMarkedForDeletion()
}

// NodesWithDeletingDriveContainer is the set of nodes hosting one — the source of each node's
// NodeCapacity.HasDeletingDriveContainer. A container with no node resolves to nowhere and is skipped.
func NodesWithDeletingDriveContainer(ownContainers []*weka.WekaContainer) map[string]bool {
	out := map[string]bool{}
	for _, cont := range ownContainers {
		if !IsDeletingDriveContainer(cont) {
			continue
		}
		if n := string(cont.GetNodeAffinity()); n != "" {
			out[n] = true
		}
	}
	return out
}

// IsDeletingComputeContainer reports whether c is a compute container on its way out. Such a container's
// pod still physically holds its hugepages/CPU/memory until the pod is actually gone, so a node hosting one
// can fail a drive-growth fit that would pass once the deletion lands.
func IsDeletingComputeContainer(c *weka.WekaContainer) bool {
	return c.Spec.Mode == weka.WekaContainerModeCompute && c.IsMarkedForDeletion()
}

// NodesWithDeletingComputeContainer is the set of nodes hosting one — the source of each node's
// NodeCapacity.HasDeletingComputeContainer. A container with no node resolves to nowhere and is skipped.
func NodesWithDeletingComputeContainer(ownContainers []*weka.WekaContainer) map[string]bool {
	out := map[string]bool{}
	for _, cont := range ownContainers {
		if !IsDeletingComputeContainer(cont) {
			continue
		}
		if n := string(cont.GetNodeAffinity()); n != "" {
			out[n] = true
		}
	}
	return out
}

// ExistingDrives builds the planner's view of this cluster's healthy drive containers.
func ExistingDrives(ctx context.Context, cluster *weka.WekaCluster, ownContainers []*weka.WekaContainer, fdByNode map[string]string) []capacityplanner.ExistingContainer {
	fdConfig := cluster.Spec.FailureDomain
	var existingDrives []capacityplanner.ExistingContainer
	for _, c := range ownContainers {
		if c.Spec.Mode != weka.WekaContainerModeDrive {
			continue
		}
		if IsDeletingDriveContainer(c) {
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
			// NumDrives is 0 for clusterCapacity/shared-drives containers (only ever set on auto-full-drives
			// containers); PlanAutoFullDrives diffs it against the node's live full-drives count.
			NumDrives: c.Spec.NumDrives,
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

// nodeResources holds per-node resource consumption: weka containers via aggregateContainerResources, every
// other scheduled pod via chargeForeignPods. Both charge cluster-wide, so every node key is complete.
type nodeResources struct {
	tlc, qlc, cores, hugepages, memory map[string]int
}

// nodeHeadroom returns node's free CPU (cores), hugepages (MiB), and memory (MiB): allocatable minus
// whatever consumed already charges against it. Shared by NodeInventory and FullDrivesInventory.
func nodeHeadroom(node *corev1.Node, consumed nodeResources) (cpu, hugepagesMiB, memoryMiB int) {
	cpu = max(0, int(node.Status.Allocatable.Cpu().Value())-consumed.cores[node.Name])
	hugepagesMiB = max(0, nodeAllocatableHugepagesMiB(node)-consumed.hugepages[node.Name])
	memoryMiB = max(0, nodeAllocatableMemoryMiB(node)-consumed.memory[node.Name])
	return cpu, hugepagesMiB, memoryMiB
}

// podKey identifies a Pod by name/namespace, recognizing a WekaContainer's own pod without a second API
// call: the wekacontainer controller always creates/reads it at client.ObjectKey{Name: container.Name,
// Namespace: container.Namespace} (refreshPod, internal/controllers/wekacontainer/funcs_pod_ensure.go).
type podKey struct {
	Namespace, Name string
}

func containerPodKey(c *weka.WekaContainer) podKey {
	return podKey{Namespace: c.Namespace, Name: c.Name}
}

// consumedNodeResources returns, per node, the TLC/QLC/CPU/hugepages/memory already claimed by every
// WekaContainer (aggregateContainerResources) plus every other scheduled pod (chargeForeignPods), both
// cluster-wide. It takes an already-listed container set so one listing serves both the inventory build
// and, in Collect, nodeDetailsFromLists.
func (c Collector) consumedNodeResources(ctx context.Context, containers []weka.WekaContainer, cons *capacityplanner.CapacityConstraints, topos map[string]capacityplanner.NodeCPUTopology) (nodeResources, error) {
	res, charged := aggregateContainerResources(containers, cons, topos)
	if err := c.chargeForeignPods(ctx, &res, charged); err != nil {
		return nodeResources{}, fmt.Errorf("charging foreign pods: %w", err)
	}
	return res, nil
}

// listAllWekaContainers lists every WekaContainer cluster-wide (all clusters, all modes) — the shared fetch
// consumedNodeResources, nodeDetailsFromLists, and ExploreNodes all build their per-node views from. Not
// scoped to the candidate nodes: a container on any node charges headroom there, and its own cluster is
// irrelevant to that.
//
// UnsafeDisableDeepCopy (contract in chargeForeignPods): aggregateContainerResources, allocatedNodeDrives,
// and consumerFrom read spec/status only, copying every value they keep into fresh maps.
func (c Collector) listAllWekaContainers(ctx context.Context) ([]weka.WekaContainer, error) {
	list := &weka.WekaContainerList{}
	if err := c.Client.List(ctx, list, client.UnsafeDisableDeepCopy); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// listInventoryInputs resolves everything the inventory builds start from: the drive/compute role node
// lists, their CPU topology map, and the cluster-wide WekaContainer list. errPrefix names the caller in
// wrapped errors.
func (c Collector) listInventoryInputs(ctx context.Context, cluster *weka.WekaCluster, errPrefix string) (driveNodes, computeNodeList []corev1.Node, topos map[string]capacityplanner.NodeCPUTopology, containers []weka.WekaContainer, err error) {
	driveNodes, computeNodeList, topos, err = c.listRoleNodesAndTopos(ctx, cluster, errPrefix)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	containers, err = c.listAllWekaContainers(ctx)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("%s: listing weka containers: %w", errPrefix, err)
	}
	return driveNodes, computeNodeList, topos, containers, nil
}

// chargeForeignPods adds, into res, the effective resource requests (cpu, memory, hugepages-2Mi) of every
// scheduled, non-terminal pod not already charged as a WekaContainer (charged, keyed by podKey). Without
// this, a node with a foreign pod holding resources reports full headroom and the planner schedules a
// container there that sits Pending forever.
//
// One cluster-wide List, not one per candidate node: on the cached client it is a single informer-store
// scan (cheaper than N indexed lookups), and being one snapshot it cannot double-charge or miss a pod that
// is rescheduled mid-pass. Charging a node the inventory never reads costs only a map key.
//
// The Pod cache must stay unfiltered for this to hold: a label-scoped cache would hide exactly the foreign
// pods charged here, leaving the planner to place containers that then sit Pending forever. See the Cache
// options in cmd/manager/main.go.
//
// UnsafeDisableDeepCopy: a cached client hands back pods sharing their maps and slices with the informer
// store, so this must only read them and must not retain one past it. effectivePodResourceRequests is
// read-only and every resource.Quantity it reaches is a map-value copy. An uncached client ignores the
// option and pays a real List anyway.
func (c Collector) chargeForeignPods(ctx context.Context, res *nodeResources, charged map[podKey]bool) error {
	podList := &corev1.PodList{}
	if err := c.Client.List(ctx, podList, client.UnsafeDisableDeepCopy); err != nil {
		return fmt.Errorf("listing pods: %w", err)
	}
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Spec.NodeName == "" {
			continue // not yet scheduled: holds no node resources
		}
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue // terminal: resources already released back to the node
		}
		if charged[podKey{Namespace: pod.Namespace, Name: pod.Name}] {
			continue // already charged above as a WekaContainer's own pod — do not double-count
		}
		cpu, hugepagesMiB, memoryMiB := effectivePodResourceRequests(pod)
		res.cores[pod.Spec.NodeName] += cpu
		res.hugepages[pod.Spec.NodeName] += hugepagesMiB
		res.memory[pod.Spec.NodeName] += memoryMiB
	}
	return nil
}

// effectivePodResourceRequests computes a pod's effective resource requests the way kube-scheduler does
// (mirrors k8s.io/kubernetes/pkg/api/v1/resource.PodRequests): for cpu, memory, and hugepages-2Mi
// independently, max(sum(regular + sidecar init containers), max(any single non-sidecar init container)).
// A sidecar init container (RestartPolicy: Always) stacks with regular containers instead of being sequenced away; only hugepages-2Mi is charged, hugepages-1Gi is untracked.
func effectivePodResourceRequests(pod *corev1.Pod) (cpuCores, hugepages2MiMiB, memoryMiB int) {
	var regularCPU, regularMem, regularHP int64
	for i := range pod.Spec.Containers {
		reqs := pod.Spec.Containers[i].Resources.Requests
		regularCPU += reqs.Cpu().MilliValue()
		regularMem += reqs.Memory().Value()
		regularHP += hugepages2MiValue(reqs)
	}

	var sidecarCPU, sidecarMem, sidecarHP int64
	var maxInitCPU, maxInitMem, maxInitHP int64
	for i := range pod.Spec.InitContainers {
		ic := &pod.Spec.InitContainers[i]
		reqs := ic.Resources.Requests
		cpu := reqs.Cpu().MilliValue()
		mem := reqs.Memory().Value()
		hp := hugepages2MiValue(reqs)
		if ic.RestartPolicy != nil && *ic.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sidecarCPU += cpu
			sidecarMem += mem
			sidecarHP += hp
			continue
		}
		maxInitCPU = max(maxInitCPU, cpu)
		maxInitMem = max(maxInitMem, mem)
		maxInitHP = max(maxInitHP, hp)
	}

	cpuMilli := max(regularCPU+sidecarCPU, maxInitCPU)
	memBytes := max(regularMem+sidecarMem, maxInitMem)
	hpBytes := max(regularHP+sidecarHP, maxInitHP)

	// Whole cores, rounding up — the same convention AllocatableCPU uses off node.Status.Allocatable
	// (int(Value()), not milli-aware): a 500m request charges as a full core.
	cpuCores = int((cpuMilli + 999) / 1000)
	memoryMiB = int(memBytes / (1 << 20))
	hugepages2MiMiB = int(hpBytes / (1 << 20))
	return cpuCores, hugepages2MiMiB, memoryMiB
}

// hugepages2MiValue returns the hugepages-2Mi quantity's value in bytes from a resource list, or 0 if the
// pod requests none.
func hugepages2MiValue(reqs corev1.ResourceList) int64 {
	name := corev1.ResourceName(string(corev1.ResourceHugePagesPrefix) + "2Mi")
	q, ok := reqs[name]
	if !ok {
		return 0
	}
	return q.Value()
}

// allocatedNodeDrives returns, per node, the drive serials already committed to any WekaContainer's
// Status.Allocations — both Drives (exclusive) and VirtualDrives[].PhysicalUUID (drive-sharing) — across
// every container regardless of cluster or deletion state (a deleting container still physically holds
// its drives until removed).
func allocatedNodeDrives(containers []weka.WekaContainer) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for i := range containers {
		c := &containers[i]
		if c.Status.Allocations == nil {
			continue
		}
		node := string(c.GetNodeAffinity())
		if node == "" {
			continue
		}
		set, ok := out[node]
		if !ok {
			set = map[string]bool{}
			out[node] = set
		}
		for _, drive := range c.Status.Allocations.Drives {
			set[drive] = true
		}
		for _, vd := range c.Status.Allocations.VirtualDrives {
			set[vd.PhysicalUUID] = true
		}
	}
	return out
}

func filterAllocatedDrives(drives []domain.DriveEntry, allocated map[string]bool) []domain.DriveEntry {
	if len(allocated) == 0 {
		return drives
	}
	out := make([]domain.DriveEntry, 0, len(drives))
	for _, d := range drives {
		if !allocated[d.Serial] {
			out = append(out, d)
		}
	}
	return out
}

// selectDrivesBySerial returns the subset of drives whose serial IS present in serials — the mirror of
// filterAllocatedDrives (which excludes by serial). Recovers the full domain.DriveEntry (with
// CapacityGiB) for a cluster's own already-allocated drives given just the serial set.
func selectDrivesBySerial(drives []domain.DriveEntry, serials map[string]bool) []domain.DriveEntry {
	if len(serials) == 0 {
		return nil
	}
	out := make([]domain.DriveEntry, 0, len(serials))
	for _, d := range drives {
		if serials[d.Serial] {
			out = append(out, d)
		}
	}
	return out
}

// aggregateContainerResources sums, per node, the resource footprint of weka containers by mode (skipping
// deleted ones): CPU is always the physical request via capacityplanner.CPURequestCores; a drive-sharing
// container adds hugepages/memory/TLC/QLC via RequiredDriveResources, an auto-full-drives one and a compute
// one take hugepages/memory from spec instead (no TLC/QLC); other modes take spec hugepages only. Returns the podKey set charged too, so chargeForeignPods can skip a WekaContainer's own pod.
func aggregateContainerResources(containers []weka.WekaContainer, cons *capacityplanner.CapacityConstraints, topos map[string]capacityplanner.NodeCPUTopology) (res nodeResources, charged map[podKey]bool) {
	res = nodeResources{
		tlc:       map[string]int{},
		qlc:       map[string]int{},
		cores:     map[string]int{},
		hugepages: map[string]int{},
		memory:    map[string]int{},
	}
	charged = map[podKey]bool{}
	for i := range containers {
		c := &containers[i]
		if c.IsMarkedForDeletion() {
			continue
		}
		node := string(c.GetNodeAffinity())
		if node == "" {
			continue
		}
		cpu := capacityplanner.CPURequestCores(&c.Spec, topos[node])
		switch c.Spec.Mode {
		case weka.WekaContainerModeDrive:
			if !c.UsesDriveSharing() {
				// auto-full-drives drive container: charged from spec, not its pod — growth raises the spec
				// first and the pod only catches up on recreation, so charging the pod would read pending
				// growth as free headroom. Mirrors the pod factory sizing (resources/pod.go); capacity comes
				// via the node's own-drive split, not RequiredDriveResources, so tlc/qlc stay 0 here.
				hugepagesMiB, memoryMiB := specHugepagesAndMemoryMiB(c, cons)
				res.cores[node] += cpu
				res.hugepages[node] += hugepagesMiB
				res.memory[node] += memoryMiB
			} else {
				t, q := DriveContainerCapacities(c)
				// Spec.NumDrives is the pod's own drive term: non-zero only under numDrives+driveCapacity (CEL
				// makes numDrives mutually exclusive with containerCapacity and clusterCapacity), which is
				// exactly the mode whose pod requests 200 MiB per drive on top of the per-core figure.
				hugepagesMiB, memoryMiB := capacityplanner.RequiredDriveResources(t, q, c.Spec.NumDrives, cons)
				res.cores[node] += cpu
				res.hugepages[node] += hugepagesMiB
				res.memory[node] += memoryMiB
				res.tlc[node] += t
				res.qlc[node] += q
			}
		case weka.WekaContainerModeCompute:
			hugepagesMiB, memoryMiB := specHugepagesAndMemoryMiB(c, cons)
			res.cores[node] += cpu
			res.hugepages[node] += hugepagesMiB
			res.memory[node] += memoryMiB
		default:
			res.cores[node] += cpu
			res.hugepages[node] += spec2MiHugepages(c)
		}
		charged[containerPodKey(c)] = true
	}
	return res, charged
}

// nodeCPUTopology reads a node's HT / full-pcpus-only topology from its weka.io/discovery.json annotation
// (config.Config.FullPcpusOnly forces full-pcpus on regardless). A node without the annotation (or an
// unparsable one) is treated as non-HT.
func nodeCPUTopology(node *corev1.Node) capacityplanner.NodeCPUTopology {
	topo := capacityplanner.NodeCPUTopology{}
	if ann, present := node.Annotations[discovery.DiscoveryAnnotation]; present {
		if info, ok := discovery.ParseNodeInfo(ann); ok {
			topo.IsHt = info.IsHt
			topo.FullPcpusOnly = info.NodeFullPcpusOnly
		} else {
			log.Log.Info("capacityplanner/inventory: unparsable discovery.json; treating node as non-HT for CPU accounting", "node", node.Name)
		}
	}
	if config.Config.FullPcpusOnly {
		topo.FullPcpusOnly = true
	}
	return topo
}

// buildNodeTopos indexes nodeCPUTopology by node name for the given node set.
func buildNodeTopos(nodes []corev1.Node) map[string]capacityplanner.NodeCPUTopology {
	m := make(map[string]capacityplanner.NodeCPUTopology, len(nodes))
	for i := range nodes {
		m[nodes[i].Name] = nodeCPUTopology(&nodes[i])
	}
	return m
}

// nodeDetailsFromLists is nodeDetails' core, taking the already-fetched role-node lists, CPU topology
// map, and cluster-wide container list instead of re-fetching them, so Collect can reuse the single
// listing pass it shares with nodeInventoryFromLists.
func (c Collector) nodeDetailsFromLists(ctx context.Context, cluster *weka.WekaCluster, ownContainers []*weka.WekaContainer, cons *capacityplanner.CapacityConstraints, inv []capacityplanner.NodeCapacity, fdByNode map[string]string, driveNodes, computeNodeList []corev1.Node, topos map[string]capacityplanner.NodeCPUTopology, allContainers []weka.WekaContainer) ([]NodeDetail, error) {
	deletingDriveNodes := NodesWithDeletingDriveContainer(ownContainers)

	// Index the planner inventory by node for free-headroom + FD lookup.
	invByNode := make(map[string]capacityplanner.NodeCapacity, len(inv))
	for i := range inv {
		invByNode[inv[i].NodeName] = inv[i]
	}

	// Union of driveNodes + computeNodeList: computeNodeList already aliases driveNodes when the
	// drive/compute selectors are equal (listRoleNodesAndTopos), so no append happens in that case.
	driveSelector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeDrive)
	computeSelector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeCompute)
	nodes := driveNodes
	if !maps.Equal(driveSelector, computeSelector) {
		nodes = append(append([]corev1.Node{}, driveNodes...), computeNodeList...)
	}

	// Used totals + consumers (net of marked-for-deletion, mirroring the planner); WekaContainer-only,
	// matching Consumers below, same scope as ExploreNodes.
	used, _ := aggregateContainerResources(allContainers, cons, topos)
	consumersByNode := map[string][]Consumer{}
	for i := range allContainers {
		wc := &allContainers[i]
		node := string(wc.GetNodeAffinity())
		if node == "" {
			continue
		}
		consumersByNode[node] = append(consumersByNode[node], consumerFrom(wc, cons, topos[node]))
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

// consumerFrom builds a Consumer describing how the collector charges one WekaContainer. Cores is the
// container's real PHYSICAL CPU request (CPURequestCores under topo), matching the per-node UsedCores.
func consumerFrom(c *weka.WekaContainer, cons *capacityplanner.CapacityConstraints, topo capacityplanner.NodeCPUTopology) Consumer {
	out := Consumer{
		Name:              c.Name,
		Namespace:         c.Namespace,
		Cluster:           owningClusterName(c),
		Role:              string(c.Spec.Mode),
		MarkedForDeletion: c.IsMarkedForDeletion(),
	}
	cpu := capacityplanner.CPURequestCores(&c.Spec, topo)
	switch c.Spec.Mode {
	case weka.WekaContainerModeDrive:
		if !c.UsesDriveSharing() {
			// auto-full-drives: charged from spec, same as aggregateContainerResources — TlcGiB/QlcGiB stay
			// 0 since its capacity comes via the node's own-drive split, not this container's spec.
			out.Cores = cpu
			out.HugepagesMiB, out.MemoryMiB = specHugepagesAndMemoryMiB(c, cons)
			return out
		}
		t, q := DriveContainerCapacities(c)
		hp, mem := capacityplanner.RequiredDriveResources(t, q, c.Spec.NumDrives, cons)
		out.TlcGiB, out.QlcGiB, out.Cores, out.HugepagesMiB, out.MemoryMiB = t, q, cpu, hp, mem
		out.NilRatio = c.Spec.DriveCapacity <= 0 && c.Spec.DriveTypesRatio == nil
	case weka.WekaContainerModeCompute:
		out.Cores = cpu
		out.HugepagesMiB, out.MemoryMiB = specHugepagesAndMemoryMiB(c, cons)
	default:
		out.Cores = cpu
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
	// spec.resources may name hugepages-2Mi outright, in which case that — not spec.Hugepages — is
	// what the pod requests (resources/pod.go applyResourcesOverride writes it under its own name,
	// on 1Gi-paged containers too), so it is what has to be charged against the node.
	if named, ok := c.Spec.NamedHugepages2MiMiB(); ok {
		return named
	}
	if c.Spec.Hugepages <= 0 || c.Spec.HugepagesSize == "1Gi" {
		return 0
	}
	return c.Spec.Hugepages
}

// specHugepagesAndMemoryMiB returns the hugepages (2Mi, MiB) and memory (MiB) footprint charged FROM SPEC
// rather than derived from realized drive capacity — the model shared by auto-full-drives drive
// containers and compute containers, both in aggregateContainerResources and consumerFrom.
func specHugepagesAndMemoryMiB(c *weka.WekaContainer, cons *capacityplanner.CapacityConstraints) (hugepagesMiB, memoryMiB int) {
	return spec2MiHugepages(c), capacityplanner.ComputeMemoryFootprintMiB(c.Spec.NumCores, cons)
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

// sumFullDriveCapacity sums the capacity of full (non-shared, non-proxy) drives — every full drive is TLC
// by design (see FullDrivesInventory). A thin wrapper around fullDriveCapacities so the call site reads
// as "the total", independent of how DriveCapacitiesGiB is built.
func sumFullDriveCapacity(drives []domain.DriveEntry) (tlcGiB int) {
	for _, gib := range fullDriveCapacities(drives) {
		tlcGiB += gib
	}
	return tlcGiB
}

// fullDriveCapacities returns the per-drive capacity (GiB) of each full drive, in the same order as
// drives — the exact per-drive sizes PlanAutoFullDrives needs to drop drives precisely (largest-first) rather
// than approximating with a uniform average. Populates NodeCapacity.DriveCapacitiesGiB.
func fullDriveCapacities(drives []domain.DriveEntry) []int {
	if len(drives) == 0 {
		return nil
	}
	out := make([]int, len(drives))
	for i, d := range drives {
		out[i] = d.CapacityGiB
	}
	return out
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

// mergeRoleNodes unions the drive-candidate and compute-candidate node sets into one planner inventory
// and the compute-eligibility map. A node present in both keeps its drive entry; a compute-only node is
// appended with zero drive capacity. computeNodes[name] is true only when the node is also currently
// schedulable (nc.IneligibleReason == ""); an ineligible node still enters inventory but is barred from new-placement candidate lists.
func mergeRoleNodes(driveInv, computeInv []capacityplanner.NodeCapacity) (inventory []capacityplanner.NodeCapacity, computeNodes map[string]bool) {
	inventory = append([]capacityplanner.NodeCapacity(nil), driveInv...)
	index := make(map[string]struct{}, len(inventory))
	for i := range inventory {
		index[inventory[i].NodeName] = struct{}{}
	}
	computeNodes = make(map[string]bool, len(computeInv))
	for i := range computeInv {
		nc := &computeInv[i]
		computeNodes[nc.NodeName] = nc.IneligibleReason == ""
		if _, ok := index[nc.NodeName]; !ok {
			index[nc.NodeName] = struct{}{}
			inventory = append(inventory, *nc)
		}
	}
	return inventory, computeNodes
}

// listNodesForSelector lists every node matching a role node selector, unfiltered by scheduling
// eligibility — every caller narrates or plans over the full matching set and applies its own eligibility
// handling on top (planner callers via NodeCapacity.IneligibleReason, ExploreNodes via NodeDetail's field).
// An empty selector matches every node in the cluster (standard Kubernetes label-selector semantics).
//
// UnsafeDisableDeepCopy (contract in chargeForeignPods): nodes carry the KB-scale discovery.json and drive
// annotations, so skipping their copy saves the most of any List in the pass. Every consumer only reads
// maps or unmarshals annotation strings into fresh structs, and no *corev1.Node outlives its caller.
func listNodesForSelector(ctx context.Context, c client.Client, selector map[string]string) ([]corev1.Node, error) {
	listOpts := []client.ListOption{client.UnsafeDisableDeepCopy}
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
