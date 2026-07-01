package allocator

import (
	"fmt"
	"sort"
	"strings"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/pkg/util"
)

// capacity_planner.go is the clusterCapacity planner. It is a PURE function (no k8s client): given a
// desired per-pool target, the protection scheme, this cluster's existing drive containers and the
// per-node remaining headroom, it returns the set of existing containers to GROW in place and the new
// containers to CREATE so that capacity is spread as evenly as possible across at least minFdNum
// failure domains and is guaranteed to land on its target nodes.
//
// The operator does NOT manage failure-domain identity — Weka does. FD identity is the per-node
// FDValue (the resolved label value in label-based mode, or the node name in AUTO mode = FD per host).
// TLC and QLC are planned as two INDEPENDENT pools; same-node results are merged into mixed containers.

// ProtectionScheme is the cluster's data+parity+hot-spare layout.
type ProtectionScheme struct {
	StripeWidth     int
	RedundancyLevel int
	HotSpare        int
}

// MinFdNum is the minimum number of failure domains drive capacity must spread across.
func (p ProtectionScheme) MinFdNum() int { return p.StripeWidth + p.RedundancyLevel + p.HotSpare }

// MinProtectionFloor returns the minimum accepted data(stripeWidth)/parity(redundancyLevel)/hotSpare
// for a clusterCapacity cluster. Production requires stripeWidth>=3 and redundancyLevel>=2
// (parity>=2 is the durability guarantee); hotSpare is optional (>=0). When the operator-level
// AllowSingleParity flag is set, the floor drops to single-parity 2+1+0 so QA/test schemes such as
// 2+1 (minFdNum=3) are accepted. QA/test only — a single parity chunk leaves a stripe unprotected
// during rebuild. The same flag drives the allow_1_parity weka override at formation.
func MinProtectionFloor() (stripeWidth, redundancyLevel, hotSpare int) {
	if globalconfig.Config.DriveSharing.AllowSingleParity {
		return 2, 1, 0
	}
	return 3, 2, 0
}

// DesiredCapacity is the per-pool RAW target (usable already inflated by RawCapacityGiB and split by
// driveTypesRatio via weka.GetTlcQlcCapacity).
type DesiredCapacity struct {
	TlcRawGiB int
	QlcRawGiB int
	// ComputeContainers / ComputeCores carry the cluster spec's explicit compute sizing (0 == unset,
	// auto-derive). The planner sizes compute from the TLC drive cores bounded by per-node headroom.
	ComputeContainers int
	ComputeCores      int
	// DriveContainers / DriveCores carry the cluster spec's explicit DRIVE sizing (0 == unset,
	// auto-derive). When set the planner honors them exactly and fails fast (plan.Infeasible) if they
	// violate a constraint, instead of silently ignoring them:
	//   - DriveContainers is the EXACT total number of drive containers (in AUTO FD mode, container ==
	//     FD). For mixed TLC+QLC it is the combined total, split between pools by raw-capacity ratio.
	//   - DriveCores is the FIXED per-container core count; a container whose capacity needs more cores
	//     than this is infeasible.
	DriveContainers int
	DriveCores      int
}

// CapacityConstraints bundles the immutable sizing knobs. Per-core capacity caps come from config; the
// hugepages/memory coefficients mirror the drive-pod sizing formulas (conservative — a rare shortfall
// self-heals on a later reconcile). The ≤N-virtual-drives-per-core limit is NOT enforced here: it
// applies at drive-allocation time in the container allocator, below the capacity-planning altitude.
type CapacityConstraints struct {
	TlcCapacityPerCoreGiB int
	QlcCapacityPerCoreGiB int
	MinChunkSizeGiB       int
	// ImbalanceFactor gates the heterogeneous "balanced fresh" growth fallback (detectImbalance): when a
	// fresh per-FD chunk would be >= ImbalanceFactor × the existing per-FD average, the dwarfed existing FDs
	// are abandoned and a fresh uniform set is laid out instead (the old containers are flagged deletable).
	// 8.0 == 8.0x (default); <= 0 disables the fallback (planner then tiles uniformly or reports infeasible).
	ImbalanceFactor float64
	// Drive-pod resource coefficients (MiB) for the per-node resource headroom gate.
	HugepagesPerCoreMiB int
	MemoryBaseMiB       int
	MemoryPerCoreMiB    int
	// DPDK base hugepages added per core to the ACTUAL drive/compute pod hugepages request
	// (GetContainerHugepages adds dpdkBaseMemoryMb × cores to both hugepages and offset). The planner's
	// node-fit gate MUST include it or it under-reserves hugepages and co-locates pools that the
	// scheduler then rejects (Insufficient hugepages-2Mi). 0 keeps the pure config defaults; the
	// cluster-level caller sets these from the cluster spec's per-role DPDK base memory.
	DriveDpdkPerCoreMiB   int
	ComputeDpdkPerCoreMiB int
	// Compute sizing knobs. MaxComputeCoresPerNode is the policy cap on per-container compute cores
	// (0 disables it; real per-node headroom still binds). The ratios/cap mirror
	// ComputeCapacityBasedHugepages so the planner's compute hugepage gate agrees with the container
	// controller's authoritative sizing.
	MaxComputeCoresPerNode   int
	ComputeHugepagesTlcRatio int
	ComputeHugepagesQlcRatio int
	ComputeMaxHugepagesMiB   int
	// AllowInPlaceGrowth permits extending EXISTING drive/compute containers in place. When false
	// (enableDynamicDriveScalingForSharedDrives=false) the planner never grows an existing container —
	// all growth is satisfied by NEW containers only (spread as evenly as possible), and is reported
	// infeasible if none can be placed. This mirrors the container-level NeedsDrivesToAllocate() gate,
	// which when the flag is off refuses same-type (in-place) virtual-drive growth but still permits
	// ADDING a brand-new pool/type to a container (e.g. a QLC-only container gaining its first TLC
	// virtual drive).
	AllowInPlaceGrowth bool
	// MinGrowthFraction is the minimum relative per-container grow (target-cur)/cur to grow an existing
	// drive FD in place; below it the grow is skipped. 0 means treat as the 0.2 default at use sites —
	// but prefer it always set.
	MinGrowthFraction float64
	// MaxOverProvisionFraction is the max fraction a pool's create-new may overshoot desiredRaw.
	MaxOverProvisionFraction float64
	// CapacityDeadbandFraction is the relative shortfall (desired-current)/desired below which pool
	// growth is ignored (see CapacityShort). 0 disables the deadband (strict current < desired).
	CapacityDeadbandFraction float64
}

// Drive-pod resource coefficients (MiB). They mirror the drive-container sizing in resources/pod.go
// conservatively; an occasional over/under-estimate just makes the capacity gates slightly cautious —
// a misjudged placement self-heals on a later reconcile. Shared by the cluster-level planner's node-fit
// gate and the container-level pre-add feasibility check so both agree on what a drive container needs.
const (
	HugepagesPerCoreMiB = 1600
	MemoryBaseMiB       = 8000
	MemoryPerCoreMiB    = 3000
)

// ExistingContainer is the planner's view of one of THIS cluster's healthy drive containers.
// TlcGiB/QlcGiB come from the SPEC (what we already asked for), not realized allocation, so a
// container whose drive-add is still in flight is not re-grown.
type ExistingContainer struct {
	Name        string
	Node        string // GetNodeAffinity(); "" when unknown
	FDValue     string
	TlcGiB      int
	QlcGiB      int
	NumCores    int
	Unscheduled bool // pod not yet scheduled — counted as committed capacity but not grown
}

// ExistingComputeContainer is the planner's view of one of THIS cluster's healthy compute
// containers. Compute carries no drive capacity or failure domain; only its node pin and resource
// footprint matter (for charging node headroom and re-validating in-place growth).
type ExistingComputeContainer struct {
	Name         string
	Node         string // GetNodeAffinity(); "" when unknown
	NumCores     int
	HugepagesMiB int
	Unscheduled  bool
}

// ContainerGrowth is an existing container to edit in place (capacity only ever increases).
type ContainerGrowth struct {
	Name      string
	NewTlcGiB int
	NewQlcGiB int
	NewCores  int
}

// NewContainer is a drive container to create, pinned to Node, in failure domain FDValue.
type NewContainer struct {
	Node     string
	FDValue  string
	TlcGiB   int
	QlcGiB   int
	NumCores int
	Ratio    *weka.DriveTypesRatio
	Type     string // tlc / qlc / mixed
}

// CapacityPlan is the planner output.
type CapacityPlan struct {
	Grow               []ContainerGrowth
	Create             []NewContainer
	TotalTlcDriveCores int
	// ComputeContainers / ComputeCores are the node-core-aware compute sizing the planner derived from
	// the TLC drive cores (1:1) bounded by per-node headroom. Zero when not in clusterCapacity mode.
	ComputeContainers int
	ComputeCores      int
	// ComputeNodes are the specific nodes the planner reserved compute on (post-drive headroom, best-fit
	// first), len == ComputeContainers. The caller pins compute containers to these so they never land on
	// a drive-pinned node that lacks the post-drive hugepages to host both. Empty on the no-op/steady-state
	// fast paths (no compute to create).
	ComputeNodes []string
	// ComputeLayout is the PER-CONTAINER compute layout: one entry per compute container in the desired
	// final state, each carrying its own node, cores and hugepages. It is HETEROGENEOUS when an existing
	// pinned compute cannot grow to the uniform target on its node — that compute is FROZEN at its current
	// size (no pod disruption) and the resulting core deficit is covered by extra compensating containers
	// distributed across free fitting nodes. When every compute fits the uniform target, every entry holds
	// the same cores/hugepages (behaviorally identical to the uniform ComputeCores/ComputeNodes fields).
	// Downstream MUST prefer ComputeLayout when non-empty; the uniform fields above remain set for legacy
	// (non-clusterCapacity) reads and as a dominant-value summary. Empty on no-op/steady-state fast paths.
	ComputeLayout []ComputeContainerSpec
	Warnings      []string
	ShrinkEvents  []string
	// OverProvisions carries per-pool over-provision advisories: the create-new-before-grow path covered an
	// increase with whole uniform-T failure domains that overshoot the pool's desiredRaw (within
	// MaxOverProvisionFraction) to avoid resizing existing containers. Emitted under their own
	// ClusterCapacityOverProvisioned (Normal) event reason, separate from the growth Warnings and shrink
	// advisories above.
	OverProvisions []string
	Infeasible     string
}

// ComputeContainerSpec is one compute container in the planner's per-container layout: the node it is
// pinned to, its core count and its hugepages (MiB). Frozen existing computes carry their CURRENT
// cores/hugepages; grown/new/compensating containers carry the planner-derived values.
type ComputeContainerSpec struct {
	Node         string
	NumCores     int
	HugepagesMiB int
}

// poolKind identifies which independent pool is being planned.
type poolKind int

const (
	poolTLC poolKind = iota
	poolQLC
)

func (p poolKind) String() string {
	if p == poolQLC {
		return "QLC"
	}
	return "TLC"
}

// recomputeCores derives the drive-container core count from its per-pool capacity:
// ceil(tlc/tlcPerCore) + ceil(qlc/qlcPerCore), at least 1.
func recomputeCores(tlcGiB, qlcGiB int, cons *CapacityConstraints) int {
	cores := 0
	if tlcGiB > 0 && cons.TlcCapacityPerCoreGiB > 0 {
		cores += util.CeilDiv(tlcGiB, cons.TlcCapacityPerCoreGiB)
	}
	if qlcGiB > 0 && cons.QlcCapacityPerCoreGiB > 0 {
		cores += util.CeilDiv(qlcGiB, cons.QlcCapacityPerCoreGiB)
	}
	return max(1, cores)
}

// RequiredDriveResources returns the cores, hugepages (MiB) and memory (MiB) a drive container needs to
// host the given per-pool capacity, using the same per-core model the cluster planner uses. The
// container controller calls this before adding virtual drives so the pod-level feasibility gate agrees
// with the cluster-level node-fit gate.
func RequiredDriveResources(tlcGiB, qlcGiB int, cons *CapacityConstraints) (cores, hugepagesMiB, memoryMiB int) {
	cores = recomputeCores(tlcGiB, qlcGiB, cons)
	hugepagesMiB = cores * cons.driveHugepagesPerCoreMiB()
	memoryMiB = ComputeMemoryFootprintMiB(cores, cons)
	return cores, hugepagesMiB, memoryMiB
}

// ComputeMemoryFootprintMiB returns the memory (MiB) a container of the given core count reserves, using
// the shared base+per-core model. Single source of truth for both the drive sizing in
// RequiredDriveResources and the per-node compute footprint charged when building the node inventory.
func ComputeMemoryFootprintMiB(cores int, cons *CapacityConstraints) int {
	return cons.MemoryBaseMiB + cores*cons.MemoryPerCoreMiB
}

// driveHugepagesPerCoreMiB is the per-core hugepages a drive POD actually requests: the base
// coefficient plus the DPDK base memory GetContainerHugepages adds per core. Using this everywhere the
// planner reserves/headrooms drive hugepages keeps the node-fit gate consistent with the scheduler.
func (cons *CapacityConstraints) driveHugepagesPerCoreMiB() int {
	return cons.HugepagesPerCoreMiB + cons.DriveDpdkPerCoreMiB
}

// TlcDriveCores returns the TLC drive-core count for a container holding tlcGiB of TLC capacity:
// ceil(tlcGiB / TlcCapacityPerCoreGiB), at least 1, or 0 when there is no TLC capacity (or no per-core
// cap configured). Shared by the planner's totalTlcDriveCores and the controller's existing-container
// summary so both derive the compute 1:1 basis identically.
func TlcDriveCores(tlcGiB int, cons *CapacityConstraints) int {
	if tlcGiB <= 0 || cons.TlcCapacityPerCoreGiB <= 0 {
		return 0
	}
	return max(1, util.CeilDiv(tlcGiB, cons.TlcCapacityPerCoreGiB))
}

// perCoreCap returns the per-core capacity ceiling for a pool.
func perCoreCap(p poolKind, cons *CapacityConstraints) int {
	if p == poolTLC {
		return cons.TlcCapacityPerCoreGiB
	}
	return cons.QlcCapacityPerCoreGiB
}

// nodeState tracks a node's remaining headroom as the planner consumes it across both pools.
type nodeState struct {
	nc           NodeCapacity
	tlcFree      int
	qlcFree      int
	coresFree    int
	hugepagesMiB int
	memoryMiB    int
	// hasDeletingDriveContainer mirrors NodeCapacity.HasDeletingDriveContainer: the node still hosts a
	// this-cluster drive container being deleted. Used only to deprioritize the node in
	// orderFreshFdGroups so a replacement FD prefers a node with no deleting container.
	hasDeletingDriveContainer bool
}

func (ns *nodeState) poolFree(p poolKind) int {
	if p == poolTLC {
		return ns.tlcFree
	}
	return ns.qlcFree
}

// nodeHeadroom returns the maximum capacity of pool p the node can still host, as the minimum of its
// drive, core, hugepages and memory budgets converted to pool capacity. includeBase reserves the
// per-container base memory (true for a NEW container, false for growing an existing one whose base
// memory is already accounted for in the node's available figure).
func (ns *nodeState) nodeHeadroom(p poolKind, cons *CapacityConstraints, includeBase bool) int {
	h, _ := ns.nodeHeadroomBinding(p, cons, includeBase)
	return h
}

// nodeHeadroomBinding is nodeHeadroom plus the name of the binding (tightest) dimension that determines
// the result — "drive capacity", "cores", "hugepages" or "memory". It mirrors nodeHeadroom's
// min-of-budgets logic exactly, so the dimension it reports is the one that caps the headroom. Used to
// explain WHY a node was rejected as a failure-domain candidate (headroom below the minimum chunk).
func (ns *nodeState) nodeHeadroomBinding(p poolKind, cons *CapacityConstraints, includeBase bool) (headroom int, binding string) {
	perCap := perCoreCap(p, cons)
	if perCap <= 0 {
		return 0, "pool disabled"
	}
	headroom, binding = ns.poolFree(p), "drive capacity"
	if coreCap := ns.coresFree * perCap; coreCap < headroom {
		headroom, binding = coreCap, "cores"
	}
	if hpPerCore := cons.driveHugepagesPerCoreMiB(); hpPerCore > 0 {
		if hpCap := (ns.hugepagesMiB / hpPerCore) * perCap; hpCap < headroom {
			headroom, binding = hpCap, "hugepages"
		}
	}
	if cons.MemoryPerCoreMiB > 0 {
		mem := ns.memoryMiB
		if includeBase {
			mem -= cons.MemoryBaseMiB
		}
		if memCap := (mem / cons.MemoryPerCoreMiB) * perCap; memCap < headroom {
			headroom, binding = memCap, "memory"
		}
	}
	if headroom < 0 {
		headroom = 0
	}
	return headroom, binding
}

// consume decrements the node's budgets for placing gGiB of pool p. includeBase charges the
// per-container base memory once (for a newly created container).
func (ns *nodeState) consume(p poolKind, gGiB int, cons *CapacityConstraints, includeBase bool) {
	cores := util.CeilDiv(gGiB, perCoreCap(p, cons))
	if p == poolTLC {
		ns.tlcFree -= gGiB
	} else {
		ns.qlcFree -= gGiB
	}
	ns.coresFree -= cores
	ns.hugepagesMiB -= cores * cons.driveHugepagesPerCoreMiB()
	ns.memoryMiB -= cores * cons.MemoryPerCoreMiB
	if includeBase {
		ns.memoryMiB -= cons.MemoryBaseMiB
	}
}

// unconsume reverses a consume of gGiB of pool p (used to roll back a fresh-FD placement that could not
// reach the uniform level). It must mirror consume exactly, including the per-container base memory when
// includeBase was charged.
func (ns *nodeState) unconsume(p poolKind, gGiB int, cons *CapacityConstraints, includeBase bool) {
	cores := util.CeilDiv(gGiB, perCoreCap(p, cons))
	if p == poolTLC {
		ns.tlcFree += gGiB
	} else {
		ns.qlcFree += gGiB
	}
	ns.coresFree += cores
	ns.hugepagesMiB += cores * cons.driveHugepagesPerCoreMiB()
	ns.memoryMiB += cores * cons.MemoryPerCoreMiB
	if includeBase {
		ns.memoryMiB += cons.MemoryBaseMiB
	}
}

// reserveCores charges `cores` extra cores (and their hugepages/memory) against the node, returning
// false without mutating anything when they do not fit. Used to account a pinned driveCores that
// exceeds the capacity-derived count, on top of what placement already consumed.
func (ns *nodeState) reserveCores(cores int, cons *CapacityConstraints) bool {
	if cores <= 0 {
		return true
	}
	hpPerCore := cons.driveHugepagesPerCoreMiB()
	if ns.coresFree < cores ||
		(hpPerCore > 0 && ns.hugepagesMiB < cores*hpPerCore) ||
		(cons.MemoryPerCoreMiB > 0 && ns.memoryMiB < cores*cons.MemoryPerCoreMiB) {
		return false
	}
	ns.coresFree -= cores
	ns.hugepagesMiB -= cores * hpPerCore
	ns.memoryMiB -= cores * cons.MemoryPerCoreMiB
	return true
}

// PlanCapacity computes the grow/create plan for both pools. It never shrinks or deletes; a pool whose
// desired is below current yields a ShrinkEvents message only.
func PlanCapacity(
	desired DesiredCapacity,
	scheme ProtectionScheme,
	existingDrives []ExistingContainer,
	existingCompute []ExistingComputeContainer,
	inventory []NodeCapacity,
	computeNodes map[string]bool,
	cons *CapacityConstraints,
) CapacityPlan {
	plan := CapacityPlan{}

	minSW, minRL, minHS := MinProtectionFloor()
	if scheme.StripeWidth < minSW || scheme.RedundancyLevel < minRL || scheme.HotSpare < minHS {
		plan.Infeasible = fmt.Sprintf("clusterCapacity requires stripeWidth>=%d, redundancyLevel>=%d, hotSpare>=%d (got sw=%d rl=%d hs=%d)",
			minSW, minRL, minHS, scheme.StripeWidth, scheme.RedundancyLevel, scheme.HotSpare)
		return plan
	}
	minFd := scheme.MinFdNum()

	// Explicit driveContainers (0 == auto): the EXACT total drive-container/FD count, split between the
	// TLC and QLC pools by raw-capacity ratio. A share below minFd (or a total below minFd) is a hard
	// constraint violation → fail fast before placing anything.
	tlcTargetFds, qlcTargetFds := 0, 0
	if desired.DriveContainers > 0 {
		var reason string
		tlcTargetFds, qlcTargetFds, reason = splitDriveContainers(desired, minFd)
		if reason != "" {
			plan.Infeasible = reason
			return plan
		}
	}

	// Working per-node headroom, sorted deterministically by FD then node.
	states := make(map[string]*nodeState, len(inventory))
	for _, nc := range inventory {
		states[nc.NodeName] = &nodeState{
			nc:                   nc,
			tlcFree:              nc.TlcGiB,
			qlcFree:              nc.QlcGiB,
			coresFree:            nc.AllocatableCPU,
			hugepagesMiB:         nc.AvailableHugepagesMiB,
			memoryMiB:            nc.AvailableMemoryMiB,
			hasDeletingDriveContainer: nc.HasDeletingDriveContainer,
		}
	}

	// Inventory headroom is already net of EVERY weka container on each node — other clusters' AND this
	// cluster's own, across all modes including compute (charged once when the node inventory is built;
	// see consumedNodeResources / aggregateContainerResources). So the per-node states here are net of
	// both drive and compute already present, which naturally steers new drive FDs away from
	// compute-saturated nodes without an explicit exclusion list — no separate compute charge needed.
	// This cluster's own compute is re-represented in existingCompute and re-validated/grown in
	// planCompute (layOutExistingCompute charges only the growth delta), mirroring how drives net out in
	// the inventory and re-charge only their growth increment.

	// Accumulators merged across pools.
	growth := map[string]*ContainerGrowth{} // by container name
	newByNode := map[string]*NewContainer{} // by node name

	planPool(poolTLC, desired.TlcRawGiB, minFd, tlcTargetFds, existingDrives, states, cons, growth, newByNode, &plan)
	planPool(poolQLC, desired.QlcRawGiB, minFd, qlcTargetFds, existingDrives, states, cons, growth, newByNode, &plan)

	// driveCores (0 == auto): a FIXED per-container core count. A container whose capacity needs more
	// cores than this cannot serve it → fail fast. Higher-than-needed is allowed (the node-fit check
	// below verifies the pinned cores actually fit). When unset, cores follow capacity (recomputeCores).
	// pinCores returns the per-container core count and the capacity-derived count it is based on. With
	// driveCores set the pinned value is honored (and a container needing more than that fails fast);
	// otherwise cores follow capacity. Returning derived lets the caller size the pinned surplus without
	// recomputing it.
	pinCores := func(tlcGiB, qlcGiB int) (cores, derived int, reason string) {
		derived = recomputeCores(tlcGiB, qlcGiB, cons)
		if desired.DriveCores <= 0 {
			return derived, derived, ""
		}
		if desired.DriveCores < derived {
			return 0, derived, fmt.Sprintf(
				"driveCores=%d is too small for a drive container of %d GiB (TLC %d + QLC %d): it needs %d cores",
				desired.DriveCores, tlcGiB+qlcGiB, tlcGiB, qlcGiB, derived)
		}
		return desired.DriveCores, derived, ""
	}

	// Emit growth (only where capacity actually increased).
	growNames := make([]string, 0, len(growth))
	for name := range growth {
		growNames = append(growNames, name)
	}
	sort.Strings(growNames)
	for _, name := range growNames {
		g := growth[name]
		cores, _, reason := pinCores(g.NewTlcGiB, g.NewQlcGiB)
		if reason != "" {
			plan.Infeasible = reason
			return plan
		}
		g.NewCores = cores
		plan.Grow = append(plan.Grow, *g)
	}

	// Emit new containers (merged TLC+QLC per node → mixed).
	newNodes := make([]string, 0, len(newByNode))
	for node := range newByNode {
		newNodes = append(newNodes, node)
	}
	sort.Strings(newNodes)
	for _, node := range newNodes {
		n := newByNode[node]
		cores, derived, reason := pinCores(n.TlcGiB, n.QlcGiB)
		if reason != "" {
			plan.Infeasible = reason
			return plan
		}
		// When driveCores is pinned ABOVE the capacity-derived count, the extra cores (and their
		// hugepages/memory) must still fit the node — placement only reserved the derived amount. Charge
		// and verify the surplus so an over-pinned container fails fast instead of landing unschedulable.
		if ns := states[node]; ns != nil && !ns.reserveCores(cores-derived, cons) {
			plan.Infeasible = fmt.Sprintf(
				"node %s cannot host driveCores=%d for its %d GiB drive container (insufficient cores/hugepages/memory for the pinned core count)",
				node, cores, n.TlcGiB+n.QlcGiB)
			return plan
		}
		n.NumCores = cores
		n.Ratio = RatioFromCaps(n.TlcGiB, n.QlcGiB)
		n.Type = ratioTypeFromCaps(n.TlcGiB, n.QlcGiB)
		plan.Create = append(plan.Create, *n)
	}

	// Explicit driveContainers: the realized distinct drive-container/FD count (existing grown + new)
	// must match exactly. The per-pool targets already enforce this on a greenfield create; this guards
	// grow/merge topologies (e.g. TLC+QLC co-locating) against silently diverging from the request.
	if plan.Infeasible == "" && desired.DriveContainers > 0 {
		if got := distinctDriveFds(existingDrives, newByNode); got != desired.DriveContainers {
			plan.Infeasible = fmt.Sprintf(
				"driveContainers=%d cannot be honored: the plan resolves to %d drive containers across the available failure domains",
				desired.DriveContainers, got)
			return plan
		}
	}

	plan.TotalTlcDriveCores = totalTlcDriveCores(existingDrives, growth, newByNode, cons)

	// Size compute from the post-drive per-node headroom (1:1 with TLC drive cores, bounded by real
	// per-node cores/hugepages). Skipped when drives are already infeasible — the caller retries.
	if plan.Infeasible == "" {
		planCompute(desired, scheme, existingCompute, computeNodes, states, cons, &plan)
	}
	return plan
}

// planCompute sizes the clusterCapacity compute containers from the POST-drive per-node headroom and
// records the result on the plan. It runs after drive placement so each node's coresFree/hugepagesMiB
// already reflect the drives this plan places and grows (Bug 2: compute is accounted against the same
// node headroom). Compute pods spread one-per-node across the COMPUTE nodes — the nodes matching the
// cluster's compute role selector (computeNodes), which may include diskless nodes outside the drive
// inventory and may overlap drive nodes. A node shared with drives draws compute from the same post-drive
// headroom; a diskless compute node contributes its full headroom. Per-container cores must fit the
// smallest compute node and the count cannot exceed the number of compute nodes. A 1:1 ratio that cannot
// fit sets plan.Infeasible so the caller retries BEFORE any drive container is created or grown.
func planCompute(
	desired DesiredCapacity,
	scheme ProtectionScheme,
	existingCompute []ExistingComputeContainer,
	computeNodes map[string]bool,
	states map[string]*nodeState,
	cons *CapacityConstraints,
	plan *CapacityPlan,
) {
	// computeNodes is always supplied by the caller (planClusterCapacity). A nil map is a bug — surface
	// it loudly rather than silently sizing compute over an empty/unintended node set.
	if computeNodes == nil {
		plan.Infeasible = "internal: compute node set not provided"
		return
	}

	// Compute-eligible nodes for THIS cluster that the planner has headroom info for. A diskless compute
	// node carries 0 drive capacity, so drive placement skipped it and states[node] holds its full
	// headroom; an overlapping node holds its post-drive remainder.
	nodes := make([]string, 0, len(computeNodes))
	for node, eligible := range computeNodes {
		if eligible && states[node] != nil {
			nodes = append(nodes, node)
		}
	}
	sort.Strings(nodes)

	// Per-node compute-core headroom (cores left after drives). `nodes` already excludes any compute
	// node without headroom info (states[node] == nil), so each entry maps to a real per-node budget.
	coreHeadroom := make([]int, len(nodes))
	for i, node := range nodes {
		if ns := states[node]; ns != nil {
			coreHeadroom[i] = max(0, ns.coresFree)
		}
	}

	// MinFdNum() is SW+RL+HS — one FD above Weka's strict SW+RL init minimum, leaving headroom to
	// delete/recreate a single compute pod (e.g. to apply grown cores/hugepages) without the layout
	// dropping below Weka's minimum. floor bounds how MANY compute containers exist.
	floor := scheme.MinFdNum()
	// minComputeFds bounds how many DISTINCT failure domains the layout must span (same MinFdNum value;
	// in AUTO mode FD == node so the two coincide). An inventory with exactly SW+RL compute FDs is
	// rejected here as infeasible even though Weka alone would accept it — deliberate, for consistency
	// with the drive minFdNum and the recreation-headroom guarantee.
	minComputeFds := scheme.MinFdNum()
	count, cores, infeasible, warnings := deriveComputeLayout(
		desired.ComputeContainers, desired.ComputeCores, plan.TotalTlcDriveCores,
		floor, cons.MaxComputeCoresPerNode, coreHeadroom,
	)
	plan.Warnings = append(plan.Warnings, warnings...)
	if infeasible != "" {
		plan.Infeasible = "compute: " + infeasible
		return
	}

	// Hugepage feasibility: a compute container of `cores` cores needs this many hugepages.
	// Require at least `count` compute nodes that can host one (both cores AND hugepages).
	perContainerHP := computeContainerHugepagesMiB(desired.TlcRawGiB, desired.QlcRawGiB, count, cores, cons)

	// Per-container layout. An existing pinned compute that can reach the uniform target on its node is
	// GROWN there; one whose node lacks the headroom is FROZEN at its current size (no pod disruption).
	// Whatever the existing computes don't supply toward the count*cores target — the SHORTFALL — is then
	// placed on free fitting compute nodes as uniformly balanced new containers (each ≤ `cores`). The only
	// remaining compute infeasibility is "not enough free fitting nodes to cover the shortfall"; it is
	// clean and pre-mutation, never stranding a Pending pod.
	fdOf := func(node string) string { return states[node].nc.FDValue }

	// Freeze/grow the existing computes (mutates each grown node's reserved headroom in states). With
	// in-place growth disabled (enableDynamicDriveScalingForSharedDrives=false) every existing compute is
	// frozen at its current size and the resulting deficit is covered by new containers only.
	existing, pinned, existingCores := layOutExistingCompute(existingCompute, states, cores, perContainerHP, !cons.AllowInPlaceGrowth)

	// Shortfall: the target cores the existing computes don't already supply, placed as new containers.
	shortfall := max(count*cores-existingCores, 0)

	// Failure domains already carrying compute via a pinned (grown or frozen) existing container. New
	// compute nodes in these FDs add no distinct-FD coverage, so the fit-node ordering below steers the
	// FD-diversity selection toward FRESH FDs first.
	coveredFDs := map[string]struct{}{}
	for _, lo := range existing {
		coveredFDs[fdOf(lo.spec.Node)] = struct{}{}
	}

	// Free fitting nodes (not pinned, full uniform footprint of cores + perContainerHP), ordered so a
	// prefix maximizes distinct-FD coverage: fresh-FD nodes first, each partition FD-spread round-robin.
	// In AUTO mode (FD == node) this reduces to the plain cores-desc best-fit order — byte-for-byte
	// unchanged. See orderFitNodesByFreshFD / orderNodesByFDSpread.
	fitNodes := orderFitNodesByFreshFD(nodes, states, pinned, coveredFDs, cores, perContainerHP, fdOf)

	// Balanced fill: cover the shortfall with the fewest uniform-capped (≤ `cores`) new containers, each on
	// the next best-fitting free node, splitting the cores as evenly as possible (the first `rem` get one
	// extra). nNew ≤ shortfall and base+1 ≤ cores by construction, so no explicit per-container cap is
	// needed. A shortfall that no free node set can cover is the sole remaining compute infeasibility.
	nNew := 0
	if shortfall > 0 {
		nNew = util.CeilDiv(shortfall, cores)
	}
	if nNew > len(fitNodes) {
		plan.Infeasible = fmt.Sprintf(
			"compute: cannot place %d new compute container(s) to cover the %d-core shortfall — only %d free fitting compute node(s) (each holds up to %d cores + %d MiB hugepages)",
			nNew, shortfall, len(fitNodes), cores, perContainerHP)
		return
	}

	// Distinct-FD requirement: the layout must span at least minComputeFds FDs (pinned existing computes
	// plus the nNew nodes chosen). The core/count math may pick an nNew short of that — e.g. a few fat FDs
	// cover the 1:1 cores in fewer containers. fitNodes is fresh-FD-first, so EXTEND nNew one node at a
	// time (each a new distinct FD) until the span is met, never past len(fitNodes); computeFDFeasibility
	// then fails fast if even all fit nodes fall short, mirroring the drive-side distinct-FD gate.
	spanFDs := map[string]struct{}{}
	for fd := range coveredFDs {
		spanFDs[fd] = struct{}{}
	}
	for _, n := range fitNodes[:nNew] {
		spanFDs[fdOf(n)] = struct{}{}
	}
	for nNew < len(fitNodes) && len(spanFDs) < minComputeFds {
		spanFDs[fdOf(fitNodes[nNew])] = struct{}{}
		nNew++
	}
	if reason := computeFDFeasibility(minComputeFds, coveredFDs, fitNodes[:nNew], fdOf); reason != "" {
		plan.Infeasible = "compute: " + reason
		return
	}
	// Extending nNew for FD coverage can outrun the cores the shortfall would split into (one-per-node,
	// so we may now have more new containers than shortfall cores). Floor the split at one core per new
	// container — over-provisioning compute slightly beyond the 1:1 target is acceptable; a 0-core
	// container is not. When nNew was not extended this is a no-op (shortfall >= nNew already).
	splitCores := max(shortfall, nNew)

	// totalCount fixes the capacity-based hugepages share (clusterMiB / totalCount) used to size every
	// non-frozen container, so it must be known before sizing any of them.
	totalCount := len(existing) + nNew
	newContainers := make([]ComputeContainerSpec, 0, nNew)
	if nNew > 0 {
		base := splitCores / nNew
		rem := splitCores % nNew
		for i := 0; i < nNew; i++ {
			cCores := base
			if i < rem {
				cCores++
			}
			node := fitNodes[i]
			ns := states[node]
			cHP := computeContainerHugepagesMiB(desired.TlcRawGiB, desired.QlcRawGiB, totalCount, cCores, cons)
			if ns.coresFree < cCores || ns.hugepagesMiB < cHP {
				// A new container is ≤ the uniform footprint this node already passed, so this is not
				// expected; treat as infeasible rather than over-claim.
				plan.Infeasible = fmt.Sprintf(
					"compute: free compute node %s cannot host a %d-core compute container (%d cores + %d MiB hugepages free)",
					node, cCores, ns.coresFree, ns.hugepagesMiB)
				return
			}
			ns.coresFree -= cCores
			ns.hugepagesMiB -= cHP
			newContainers = append(newContainers, ComputeContainerSpec{Node: node, NumCores: cCores, HugepagesMiB: cHP})
		}
	}

	// Assemble: re-derive every NON-frozen container's hugepages at the final totalCount (frozen ones keep
	// their current hugepages — they are not being resized). New containers were already sized at
	// totalCount above.
	layout := make([]ComputeContainerSpec, 0, totalCount)
	for _, lo := range existing {
		spec := lo.spec
		if !lo.frozen {
			spec.HugepagesMiB = computeContainerHugepagesMiB(desired.TlcRawGiB, desired.QlcRawGiB, totalCount, spec.NumCores, cons)
		}
		layout = append(layout, spec)
	}
	layout = append(layout, newContainers...)
	sort.Slice(layout, func(i, j int) bool { return layout[i].Node < layout[j].Node })

	chosen := make([]string, 0, totalCount)
	for _, l := range layout {
		chosen = append(chosen, l.Node)
	}

	plan.ComputeContainers = totalCount
	plan.ComputeCores = cores // the uniform/dominant target (legacy summary)
	plan.ComputeNodes = chosen
	plan.ComputeLayout = layout
}

// laidOut is one existing compute container resolved against the uniform target: either grown to the
// target or frozen at its current size.
type laidOut struct {
	spec   ComputeContainerSpec
	frozen bool // kept at current size (not resized) — its hugepages must not be re-derived
}

// layOutExistingCompute resolves each scheduled existing compute against the uniform target (cores +
// perContainerHP). A container whose node has headroom for the growth delta is GROWN in place (and the
// delta is reserved in states so the balanced fill does not double-claim that node); one whose node lacks
// the headroom is FROZEN at its current size (no pod disruption). The prerequisite (Step 1b in
// PlanCapacity) already charged each existing compute's CURRENT footprint against states, so
// states[node].coresFree/hugepagesMiB is the remaining headroom after it. Returns the laid-out containers
// (in input order), the set of pinned nodes, and the cores they contribute toward the count*cores target.
func layOutExistingCompute(
	existingCompute []ExistingComputeContainer,
	states map[string]*nodeState,
	cores, perContainerHP int,
	freezeExisting bool,
) (existing []laidOut, pinned map[string]struct{}, existingCores int) {
	pinned = make(map[string]struct{}, len(existingCompute))
	existing = make([]laidOut, 0, len(existingCompute))
	for i := range existingCompute {
		ec := &existingCompute[i]
		if ec.Node == "" || ec.Unscheduled {
			continue
		}
		ns := states[ec.Node]
		if ns == nil {
			continue
		}
		pinned[ec.Node] = struct{}{}
		coresDelta := cores - ec.NumCores
		hpDelta := perContainerHP - ec.HugepagesMiB
		if freezeExisting ||
			(coresDelta > 0 && ns.coresFree < coresDelta) ||
			(hpDelta > 0 && ns.hugepagesMiB < hpDelta) {
			// Frozen at the current size — either because in-place growth is disabled (freezeExisting:
			// enableDynamicDriveScalingForSharedDrives=false) or this node lacks headroom for the growth
			// delta. No pod disruption; the shortfall it leaves is covered by the balanced fill (new
			// containers).
			existing = append(existing, laidOut{
				spec:   ComputeContainerSpec{Node: ec.Node, NumCores: ec.NumCores, HugepagesMiB: ec.HugepagesMiB},
				frozen: true,
			})
			existingCores += ec.NumCores
			continue
		}
		// Delta fits: reserve the growth increment (so the balanced fill does not double-claim this node)
		// and keep it in place at the uniform target.
		if coresDelta > 0 {
			ns.coresFree -= coresDelta
		}
		if hpDelta > 0 {
			ns.hugepagesMiB -= hpDelta
		}
		existing = append(existing, laidOut{spec: ComputeContainerSpec{Node: ec.Node, NumCores: cores, HugepagesMiB: perContainerHP}})
		existingCores += cores
	}
	return existing, pinned, existingCores
}

// orderFitNodesByFreshFD returns the free fitting compute nodes (not pinned, with full uniform-footprint
// headroom of cores + perContainerHP) ordered so a prefix maximizes distinct-FD coverage. Without this,
// best-fit-by-cores can pile the first picks onto a few high-headroom FDs (e.g. two hosts of one rack),
// leaving compute on fewer than the required failure domains and making Weka refuse to initialize (#11).
// Nodes in FRESH FDs (not yet covered by a pinned existing compute) are FD-spread and emitted first, then
// the covered-FD nodes; each partition is round-robined by orderNodesByFDSpread. In AUTO mode (FD == node)
// every node is its own FD, so each partition holds one node per FD and the round-robin reduces to the
// plain cores-desc best-fit sort — byte-for-byte unchanged.
func orderFitNodesByFreshFD(
	nodes []string,
	states map[string]*nodeState,
	pinned, coveredFDs map[string]struct{},
	cores, perContainerHP int,
	fdOf func(node string) string,
) []string {
	headroomOf := func(node string) int { return states[node].coresFree }
	var freshFit, coveredFit []string
	for _, node := range nodes {
		if _, ok := pinned[node]; ok {
			continue // already carries an existing pinned compute (grown or frozen)
		}
		ns := states[node]
		if ns == nil || ns.coresFree < cores || ns.hugepagesMiB < perContainerHP {
			continue
		}
		if _, ok := coveredFDs[fdOf(node)]; ok {
			coveredFit = append(coveredFit, node)
		} else {
			freshFit = append(freshFit, node)
		}
	}
	return append(
		orderNodesByFDSpread(freshFit, headroomOf, fdOf),
		orderNodesByFDSpread(coveredFit, headroomOf, fdOf)...,
	)
}

// deriveComputeLayout sizes the clusterCapacity compute containers from the TLC drive cores
// (compute:drive 1:1), bounded by REAL per-node core headroom. Compute pods spread one-per-node across
// the compute nodes, so per-container cores must fit the smallest such node (nodeHeadroom) and
// the container count cannot exceed the number of compute nodes. specCount/specCores of 0 mean
// "unset" (auto-derive); maxCoresPerNode of 0 disables the policy cap (real headroom still binds).
// nodeHeadroom is the per-compute-node compute-core headroom. It returns the count, per-container
// cores, a non-empty infeasible reason when 1:1 cannot be honored one-per-node within node limits, and
// advisory warnings.
//
// Invariants when feasible: count >= floor; count <= len(nodeHeadroom); per-container cores <=
// min(maxCoresPerNode, min(nodeHeadroom)) for auto-derived cores; count*cores >= totalTlcDriveCores
// whenever the count or cores is auto-derived.
func deriveComputeLayout(specCount, specCores, totalTlcDriveCores, floor, maxCoresPerNode int, nodeHeadroom []int) (count, cores int, infeasible string, warnings []string) {
	d := len(nodeHeadroom)
	hmin := 0
	for i, h := range nodeHeadroom {
		if i == 0 || h < hmin {
			hmin = h
		}
	}
	// perContainerCap: the largest per-container core count that fits every compute node and the
	// policy cap. maxCoresPerNode == 0 disables the policy cap; the real headroom hmin still binds.
	perContainerCap := hmin
	if maxCoresPerNode > 0 && maxCoresPerNode < perContainerCap {
		perContainerCap = maxCoresPerNode
	}
	t := totalTlcDriveCores

	switch {
	case specCount != 0:
		// Explicit count: honor it (one-per-node, so it cannot exceed the compute node count),
		// derive cores to meet 1:1 when unset, and warn on cap/ratio violations.
		count = specCount
		if count > d {
			return 0, 0, fmt.Sprintf(
				"computeContainers=%d exceeds the %d compute nodes; compute spreads one-per-node", count, d), nil
		}
		cores = specCores
		if cores == 0 {
			cores = max(1, util.CeilDiv(t, count))
		}
		// Explicit values are honored exactly and fail fast (no clamp) when they violate a constraint.
		if perContainerCap > 0 && cores > perContainerCap {
			return 0, 0, fmt.Sprintf(
				"computeCores=%d exceeds the per-node compute core headroom (%d) after drive placement",
				cores, perContainerCap), nil
		}
		if count*cores < t {
			return 0, 0, fmt.Sprintf(
				"compute:drive 1:1 core ratio not met: %d compute containers × %d cores = %d compute cores < %d TLC drive cores; "+
					"increase computeContainers or computeCores, or remove them to enable auto-derivation",
				count, cores, count*cores, t), nil
		}
		return count, cores, "", warnings

	case specCores != 0:
		// Cores set, count unset: honor the cores exactly (fail fast if they exceed the real per-node
		// headroom or policy cap), then derive the count to satisfy 1:1 with the SW+RL floor.
		cores = specCores
		if perContainerCap > 0 && cores > perContainerCap {
			return 0, 0, fmt.Sprintf(
				"computeCores=%d exceeds the per-node compute core headroom (%d) after drive placement",
				cores, perContainerCap), nil
		}
		if cores <= 0 {
			return 0, 0, "no compute core headroom on the compute nodes after drive placement", nil
		}
		count = max(floor, util.CeilDiv(t, cores))
		if count > d {
			return 0, 0, fmt.Sprintf(
				"cannot satisfy compute:drive 1:1: need %d compute containers of %d cores but only %d compute nodes",
				count, cores, d), nil
		}
		return count, cores, "", warnings

	default:
		// Neither set: minimize the container count subject to one-per-node fit and 1:1, bounded by the
		// real per-node core headroom (NOT a blind cap).
		if perContainerCap <= 0 {
			return 0, 0, "no compute core headroom on the compute nodes after drive placement", nil
		}
		count = max(floor, util.CeilDiv(t, perContainerCap))
		if count > d {
			return 0, 0, fmt.Sprintf(
				"cannot satisfy compute:drive 1:1: need %d compute containers but only %d compute nodes (max %d compute cores < %d TLC drive cores)",
				count, d, d*perContainerCap, t), nil
		}
		cores = max(1, util.CeilDiv(t, count))
		return count, cores, "", warnings
	}
}

// computeFDFeasibility returns a non-empty reason when the planned compute layout would span fewer than
// minFds distinct failure domains. coveredFDs are the FDs of pinned existing computes; newNodes are the
// nodes chosen for new compute containers (their FDValue resolved via fdOf). Weka refuses to initialize a
// cluster whose compute nodes do not cover at least stripeWidth+redundancyLevel FDs, so the planner fails
// fast here with a clear message instead of letting Weka reject init after containers are created.
func computeFDFeasibility(minFds int, coveredFDs map[string]struct{}, newNodes []string, fdOf func(node string) string) string {
	fds := map[string]struct{}{}
	for fd := range coveredFDs {
		fds[fd] = struct{}{}
	}
	for _, n := range newNodes {
		fds[fdOf(n)] = struct{}{}
	}
	if len(fds) < minFds {
		return fmt.Sprintf(
			"compute spans only %d of %d required failure domains (minFdNum = stripeWidth+redundancyLevel+hotSpare); "+
				"add compute-eligible nodes in more distinct failure domains",
			len(fds), minFds)
	}
	return ""
}

// unboundedComputeHeadroom is a per-node compute-core headroom large enough to never bind, used by
// ComputeLayoutWouldGrow to derive the compute target under the policy cap alone (real per-node
// headroom can only ever shrink that target, never grow it).
const unboundedComputeHeadroom = 1 << 30

// ComputeLayoutWouldGrow reports whether the clusterCapacity compute derivation could require MORE
// compute than the cluster's current healthy compute set (curCount containers, curMinCores cores
// each), assuming UNBOUNDED per-node core headroom (only the maxCoresPerNode policy cap binds). It is
// the steady-state skip gate: when it returns false, the current compute set already covers the maximum
// the planner could ask for, so node inventory need not be rebuilt to size compute; real (finite) node
// headroom can only lower that target. It returns true (must re-plan) when there is no current compute,
// when the derivation is infeasible at the current count, or when it needs more containers or more
// per-container cores than the current set already has.
func ComputeLayoutWouldGrow(specCount, specCores, totalTlcDriveCores, floor, maxCoresPerNode, curCount, curMinCores, curTotalCores int) bool {
	if curCount <= 0 {
		return true
	}
	headroom := make([]int, curCount)
	for i := range headroom {
		headroom[i] = unboundedComputeHeadroom
	}
	count, cores, infeasible, _ := deriveComputeLayout(specCount, specCores, totalTlcDriveCores, floor, maxCoresPerNode, headroom)
	if infeasible != "" {
		return true
	}
	if count > curCount {
		return true // need more containers than exist
	}
	// A uniform per-container target above the smallest existing compute normally means grow. But with a
	// HETEROGENEOUS layout (a frozen compute kept below target, its deficit covered by extra/compensating
	// containers) the smallest compute legitimately stays below `cores` forever — that is steady state, not
	// pending growth, AS LONG AS the current TOTAL compute cores already cover the compute:drive 1:1
	// requirement (>= totalTlcDriveCores). This freeze/compensate path only applies when cores are
	// AUTO-derived (specCores == 0); an explicitly pinned specCores must be reached by every container, so
	// a below-target smallest compute always means grow there.
	if cores > curMinCores {
		if specCores == 0 {
			return curTotalCores < totalTlcDriveCores
		}
		return true
	}
	return false
}

// computeContainerHugepagesMiB estimates one compute container's hugepages (MiB) for the planner's
// node-fit gate, mirroring allocator.ComputeCapacityBasedHugepages: a capacity-based cluster-wide
// component split across the containers, plus a per-core component, floored at a per-core minimum and
// optionally capped. Used only to gate placement; the container controller computes the authoritative
// value when it builds the pod.
func computeContainerHugepagesMiB(tlcRawGiB, qlcRawGiB, count, cores int, cons *CapacityConstraints) int {
	capacityBased := 0
	if count > 0 {
		clusterMiB := 0
		if cons.ComputeHugepagesTlcRatio > 0 {
			clusterMiB += tlcRawGiB * 1024 / cons.ComputeHugepagesTlcRatio
		}
		if cons.ComputeHugepagesQlcRatio > 0 {
			clusterMiB += qlcRawGiB * 1024 / cons.ComputeHugepagesQlcRatio
		}
		capacityBased = clusterMiB / count
	}
	hp := max(capacityBased+1700*cores, 3000*cores)
	if hp%2 != 0 {
		hp++
	}
	if cons.ComputeMaxHugepagesMiB > 0 && hp > cons.ComputeMaxHugepagesMiB {
		hp = cons.ComputeMaxHugepagesMiB
	}
	// Mirror GetContainerHugepages: the compute POD adds DPDK base memory (× cores) on top of the
	// even-rounded, capped base. Include it so the planner's compute hugepage fit-gate matches the
	// scheduler's actual request and never co-locates compute onto a node that cannot hold it.
	hp += cons.ComputeDpdkPerCoreMiB * cores
	return hp
}

// finalPoolCap returns an existing container's post-growth capacity for pool p (its planned
// NewTlcGiB/NewQlcGiB when a growth record exists, else its current capacity).
func finalPoolCap(c *ExistingContainer, growth map[string]*ContainerGrowth, p poolKind) int {
	if g, ok := growth[c.Name]; ok {
		if p == poolTLC {
			return g.NewTlcGiB
		}
		return g.NewQlcGiB
	}
	return poolCap(c, p)
}

// finalPerFD sums final-state pool-p capacity per failure domain across existing containers (as
// grown) and newly created containers. Only positive contributions on a known FD are counted.
func finalPerFD(
	p poolKind,
	existingDrives []ExistingContainer,
	growth map[string]*ContainerGrowth,
	newByNode map[string]*NewContainer,
) map[string]int {
	perFD := map[string]int{}
	for i := range existingDrives {
		if v := finalPoolCap(&existingDrives[i], growth, p); v > 0 && existingDrives[i].FDValue != "" {
			perFD[existingDrives[i].FDValue] += v
		}
	}
	for _, n := range newByNode {
		if v := poolCapNew(n, p); v > 0 && n.FDValue != "" {
			perFD[n.FDValue] += v
		}
	}
	return perFD
}

// ratioTypeFromCaps classifies a (tlc,qlc) capacity pair.
func ratioTypeFromCaps(tlcGiB, qlcGiB int) string {
	switch {
	case tlcGiB > 0 && qlcGiB > 0:
		return DriveTypeMixed
	case qlcGiB > 0:
		return DriveTypeQLC
	default:
		return DriveTypeTLC
	}
}

// poolCap returns a container's capacity for the given pool.
func poolCap(c *ExistingContainer, p poolKind) int {
	if p == poolTLC {
		return c.TlcGiB
	}
	return c.QlcGiB
}

// addPoolGrowth bumps a growth record's capacity for the given pool.
func addPoolGrowth(g *ContainerGrowth, p poolKind, add int) {
	if p == poolTLC {
		g.NewTlcGiB += add
	} else {
		g.NewQlcGiB += add
	}
}

// addPoolNew bumps a new container's capacity for the given pool.
func addPoolNew(n *NewContainer, p poolKind, add int) {
	if p == poolTLC {
		n.TlcGiB += add
	} else {
		n.QlcGiB += add
	}
}

// poolAvg returns the average per-container pool-p capacity over this cluster's existing pool-p containers
// (0 when none). Used as the existing baseline for the heterogeneous-growth fallback trigger (detectImbalance).
func poolAvg(existingDrives []ExistingContainer, p poolKind) int {
	sum, n := 0, 0
	for i := range existingDrives {
		if v := poolCap(&existingDrives[i], p); v > 0 {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

// growChunk is the even per-FD chunk a delta would add across minFd failure domains, floored at MinChunk.
// It is the "fresh chunk" compared against the existing per-FD average in the heterogeneous trigger.
func growChunk(delta, minFd int, cons *CapacityConstraints) int {
	return max(cons.MinChunkSizeGiB, util.CeilDiv(delta, minFd))
}

// detectImbalance reports whether laying a fresh per-FD chunk of newPerFD alongside existing FDs of
// average size existingAvg would be too skewed — true when newPerFD >= ImbalanceFactor × existingAvg.
// False when there is no existing baseline (existingAvg <= 0) or the factor is disabled (<= 0). This gates
// the heterogeneous fallback (planPoolFreshUniform): a fresh chunk that dwarfs the tiny existing FDs means growing them into a
// uniform set is either infeasible (their low ceiling caps the uniform level) or would gate the pool's
// usable capacity, so a fresh uniform set is laid out instead and the small FDs are flagged deletable.
func detectImbalance(newPerFD, existingAvg int, cons *CapacityConstraints) bool {
	if existingAvg <= 0 || cons.ImbalanceFactor <= 0 {
		return false
	}
	return float64(newPerFD) >= float64(existingAvg)*cons.ImbalanceFactor
}

func planPool(
	p poolKind,
	desiredRaw int,
	minFd int,
	targetFds int, // 0 == auto (minFd-first, extend to fit); >0 == EXACT total FD count for this pool
	existingDrives []ExistingContainer,
	states map[string]*nodeState,
	cons *CapacityConstraints,
	growth map[string]*ContainerGrowth,
	newByNode map[string]*NewContainer,
	plan *CapacityPlan,
) {
	// Step 1: nothing to do when no capacity requested.
	if desiredRaw <= 0 {
		return
	}

	// Step 2: closures that create-or-fetch the mutable records for existing-grow and new-container paths.
	growFor := func(c ExistingContainer) *ContainerGrowth {
		g, ok := growth[c.Name]
		if !ok {
			g = &ContainerGrowth{Name: c.Name, NewTlcGiB: c.TlcGiB, NewQlcGiB: c.QlcGiB}
			growth[c.Name] = g
		}
		return g
	}
	newFor := func(node, fd string) *NewContainer {
		n, ok := newByNode[node]
		if !ok {
			n = &NewContainer{Node: node, FDValue: fd}
			newByNode[node] = n
		}
		return n
	}

	// Step 3: compute the signed delta; shrink and no-change are terminal.
	current := poolCurrent(existingDrives, p)
	delta := desiredRaw - current
	if delta < 0 {
		// Over-provisioned: advise a manual shrink only when the overage exceeds the intentional
		// over-provision cap (an in-cap overage is our own create-new rounding — stay silent).
		if current-desiredRaw > OverProvisionCapGiB(desiredRaw, cons) {
			plan.ShrinkEvents = append(plan.ShrinkEvents, shrinkMsg(p, desiredRaw, current))
		}
		return
	}
	if !CapacityShort(current, desiredRaw, cons) {
		return // within the relative deadband (or exactly met) — treat as no change
	}

	// Step 3.5: Heterogeneous-growth fallback. When the FD count is not pinned and a fresh per-FD chunk
	// would DWARF the existing tiny FDs (detectImbalance: chunk >= ImbalanceFactor × existing per-FD
	// average), the existing FDs are too small to grow into a uniform set without gating the pool's usable
	// capacity or forcing an infeasible uniform level. Lay out a fresh UNIFORM set on nodes not already
	// hosting this pool and flag the small containers deletable, instead of letting them veto the plan. This
	// is a CREATE-on-fresh-nodes operation (it abandons the dwarfed FDs, never grows them), so it is NOT
	// gated on AllowInPlaceGrowth — consistent with the default config covering increases by creating new
	// FDs. Falls through to the incremental uniform-increase path (state untouched) when no fresh uniform
	// set reaches the target.
	if targetFds == 0 &&
		detectImbalance(growChunk(delta, minFd, cons), poolAvg(existingDrives, p), cons) {
		if planPoolFreshUniform(p, desiredRaw, minFd, existingDrives, states, cons, growFor, growth, newByNode, newFor, plan, true /*isFallback*/) {
			return
		}
	}

	// Step 4: Explicit driveContainers — caller pinned the exact total FD count for this pool. Place
	// exactly targetFds FDs at the even per-FD chunk T=ceil(desiredRaw/targetFds) via placeUniform (grow
	// existing below-T FDs, create the rest), then check feasibility.
	if targetFds > 0 {
		planPoolExplicit(p, desiredRaw, minFd, targetFds, existingDrives, states, cons, growth, growFor, newByNode, newFor, plan)
		return
	}

	// Step 5: Uniform-FD increase — auto FD count, genuine increase, pool already has existing FDs. The
	// existing per-FD chunk T is well-defined; replicate it rather than recomputing a greenfield level.
	if delta > 0 && poolExistingFds(p, existingDrives) > 0 {
		planPoolUniformIncrease(p, desiredRaw, minFd, current, existingDrives, states, cons, growFor, growth, newByNode, newFor, plan)
		return
	}

	// Step 6: greenfield (this pool has no FD yet — though other-pool containers may exist on shared nodes).
	// Free-select the best uniform (N, T) and place it; cross-pool conversion happens via placeUniform's
	// grow path when a chosen FD already hosts an other-pool container. Infeasible if no uniform tiling fits.
	planPoolFreshUniform(p, desiredRaw, minFd, existingDrives, states, cons, growFor, growth, newByNode, newFor, plan, false /*isFallback*/)
}

// finalizePoolFeasibility verifies that placement actually realized desiredRaw for pool p and that the
// final state carries >= minFd failure domains, setting plan.Infeasible on a shortfall. placeUniform may
// roll back an FD whose hosts can't hold their even share (the (N,T) scan reasons over aggregate FD
// headroom), so a post-placement recheck is required on every placement branch. Realized pool-p capacity =
// existing as grown (finalPoolCap) + new (poolCapNew). When excludePoolPExisting is set (the greenfield /
// balanced-fresh fresh-only paths) the existing pool-p FDs are NOT counted — they are being abandoned, or
// none exist — so only fresh placements (new containers + other-pool→mixed conversions) count toward coverage.
func finalizePoolFeasibility(
	p poolKind,
	desiredRaw, minFd int,
	existingDrives []ExistingContainer,
	growth map[string]*ContainerGrowth,
	newByNode map[string]*NewContainer,
	cons *CapacityConstraints,
	plan *CapacityPlan,
	excludePoolPExisting bool,
) {
	placed := 0
	for _, n := range newByNode {
		placed += poolCapNew(n, p)
	}
	for i := range existingDrives {
		c := &existingDrives[i]
		if excludePoolPExisting && poolExistingFds(p, []ExistingContainer{*c}) > 0 {
			continue // pool-p FD being abandoned (heterogeneous fallback) — does not count toward coverage
		}
		placed += finalPoolCap(c, growth, p)
	}
	// remaining shortfall is measured to the deadband floor (CapacityCoverTarget), so a within-deadband gap isn't reported infeasible.
	if reason := poolFeasibility(p, minFd, max(0, CapacityCoverTarget(desiredRaw, cons)-placed), existingDrives, growth, newByNode, cons); reason != "" {
		plan.Infeasible = reason
	}
}

// planPoolFreshUniform lays a fresh, internally-UNIFORM set of failure domains for pool p across nodes NOT
// already hosting pool p (a node carrying an other-pool container is still a candidate — placeUniform
// converts it to mixed via its grow path). Coverage is measured over the fresh set ALONE
// (excludePoolPExisting). Two callers, distinguished by isFallback:
//   - greenfield (false): the pool has no FD yet. If no uniform (N, T) tiles the candidates, the pool is
//     ClusterCapacityInfeasible. (excludePoolPExisting is a no-op — no pool-p FDs exist.)
//   - heterogeneous fallback (true): a fresh per-FD chunk would dwarf the existing FDs, so they are
//     ABANDONED — left running but flagged deletable via a ClusterCapacityHeterogeneousGrowth Warning. Returns false
//     WITHOUT mutating state when no fresh set reaches the target (selectUniform is non-mutating), so
//     planPool falls through to the uniform-increase path on untouched state.
//
// Returns true when a fresh set was placed (committed; plan.Infeasible may still be set if a per-host even
// split fell short — pathological label FDs).
func planPoolFreshUniform(
	p poolKind,
	desiredRaw, minFd int,
	existingDrives []ExistingContainer,
	states map[string]*nodeState,
	cons *CapacityConstraints,
	growFor func(ExistingContainer) *ContainerGrowth,
	growth map[string]*ContainerGrowth,
	newByNode map[string]*NewContainer,
	newFor func(node, fd string) *NewContainer,
	plan *CapacityPlan,
	isFallback bool,
) bool {
	candidates := orderFreshFdGroups(p, states, poolNodeUsed(existingDrives, p), cons)
	chosen, T, ok := selectUniform(desiredRaw, minFd, candidates, cons)
	if !ok {
		if !isFallback {
			plan.Infeasible = uniformInfeasibleMsg(p, desiredRaw, minFd, candidates, states, poolNodeUsed(existingDrives, p), cons)
		}
		return false // greenfield: infeasible (set above); fallback: fall through on untouched state
	}
	placeUniform(p, T, chosen, existingDrives, states, cons, growFor, newByNode, newFor)
	finalizePoolFeasibility(p, desiredRaw, minFd, existingDrives, growth, newByNode, cons, plan, true)
	if isFallback && plan.Infeasible == "" {
		// Every chosen FD reached T (finalize passed), so len(chosen) is the fresh FD count for the advisory.
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"%s capacity grew heterogeneously: created a fresh balanced set of ~%d GiB across %d failure domain(s). "+
				"The older, smaller drive containers can be deleted manually once data has migrated.",
			p, T, len(chosen)))
	}
	return true
}

// planPoolExplicit places exactly targetFds failure domains for pool p when the user pinned the
// driveContainers count. The per-FD chunk is the uniform T = ceil(desiredRaw / targetFds). placeUniform
// grows existing below-T FDs to T and creates the remaining fresh FDs at T. resolveExactNewFds runs first
// for its fail-fast guards.
func planPoolExplicit(
	p poolKind,
	desiredRaw, minFd, targetFds int,
	existingDrives []ExistingContainer,
	states map[string]*nodeState,
	cons *CapacityConstraints,
	growth map[string]*ContainerGrowth,
	growFor func(ExistingContainer) *ContainerGrowth,
	newByNode map[string]*NewContainer,
	newFor func(node, fd string) *NewContainer,
	plan *CapacityPlan,
) {
	current := poolCurrent(existingDrives, p)
	delta := desiredRaw - current

	// Fail-fast checks: pinned count vs existing count, per-container minimum chunk, etc.
	exactNewFds, reason := resolveExactNewFds(p, targetFds, existingDrives, delta, cons)
	if reason != "" {
		plan.Infeasible = reason
		return
	}

	T := util.CeilDiv(desiredRaw, targetFds)

	// Assemble the exactly-targetFds chosen FDs: every existing pool-p FD (as a grow target) plus exactly
	// exactNewFds fresh FDs at the front of the headroom-desc candidate list. placeUniform grows the
	// existing FDs below T up to T and creates the fresh FDs at T.
	chosen := existingFdsAsChosen(p, existingDrives, states, cons)
	fresh := orderFreshFdGroups(p, states, poolNodeUsed(existingDrives, p), cons)
	chosen = append(chosen, takeFreshAtLevel(fresh, exactNewFds, T)...)

	placeUniform(p, T, chosen, existingDrives, states, cons, growFor, newByNode, newFor)

	// Existing pool-p FDs are grown in place, so they count toward coverage (excludePoolPExisting=false).
	finalizePoolFeasibility(p, desiredRaw, minFd, existingDrives, growth, newByNode, cons, plan, false)
}

// planPoolUniformIncrease realizes the uniform-FD increase policy (§4 of the FEAT plan): on a genuine
// increase for a pool that already has at least one failure domain, it prefers CREATING whole new FDs at
// the existing uniform per-FD chunk T over editing existing container specs, capped at
// MaxOverProvisionFraction. If create-new cannot cover the delta (no spare nodes at T) it raises the
// uniform level T -> Lmin and grows every below-Lmin existing FD up to Lmin while placing the fresh FDs
// AT Lmin (uniformity) — but only if the relative grow clears MinGrowthFraction and in-place growth is
// allowed; otherwise the plan is marked infeasible with a tailored message. New FDs are never sub-T.
//
// Both the create-new fresh FDs and the uniform grow go through placeUniform, so an other-pool container
// on a candidate node is CONVERTED to mixed (its grow-path) just like greenfield — consistent cross-pool
// conversion on the increase path too.
func planPoolUniformIncrease(
	p poolKind,
	desiredRaw, minFd, current int,
	existingDrives []ExistingContainer,
	states map[string]*nodeState,
	cons *CapacityConstraints,
	growFor func(ExistingContainer) *ContainerGrowth,
	growth map[string]*ContainerGrowth,
	newByNode map[string]*NewContainer,
	newFor func(node, fd string) *NewContainer,
	plan *CapacityPlan,
) {
	delta := desiredRaw - current

	// NOTE on Unscheduled: perFd/numExisting/T0 below do NOT filter Unscheduled containers while reach/
	// existingReach/existingFdsAsChosen DO. That asymmetry is harmless because a capacity-bearing
	// unscheduled drive container can never reach this function: planClusterCapacity defers planning
	// upstream while any alive drive container is unscheduled (see firstUnscheduledDriveContainer). The
	// Unscheduled checks here are therefore purely defensive.
	//
	// --- T0: the uniform per-FD chunk (the chunk we replicate). Aggregate existing per-FD pool capacity,
	// then T0 = max(MinChunk, smallest per-FD sum). Over-sized anchors stay above T0; fragments cannot
	// lower it below MinChunk. ---
	perFd := map[string]int{}
	for i := range existingDrives {
		c := &existingDrives[i]
		if c.FDValue == "" {
			continue
		}
		if v := poolCap(c, p); v > 0 {
			perFd[c.FDValue] += v
		}
	}
	numExisting := len(perFd)
	minFdCap := 0
	for _, v := range perFd {
		if minFdCap == 0 || v < minFdCap {
			minFdCap = v
		}
	}
	T0 := max(cons.MinChunkSizeGiB, minFdCap)

	// --- Fresh candidate FDs (FDs not hosting pool p, per-node headroom >= MinChunk), best-headroom first.
	freshGroups := orderFreshFdGroups(p, states, poolNodeUsed(existingDrives, p), cons)
	freshCountAtLeast := func(L int) int {
		n := 0
		for _, g := range freshGroups {
			if g.headroom >= L {
				n++
			}
		}
		return n
	}

	// existingReach sums the per-FD pool capacity reachable at level L over THIS POOL's existing FDs (cap>0):
	// an anchor already >= L contributes its full cap; a growable FD (ceiling >= L) contributes L; an FD that
	// cannot reach L makes level L infeasible (ok=false). Per FD: cap = Σ current pool-p capacity over its
	// containers; ceiling = cap + Σ host headroom (the level it could grow to). Cap-0 FDs (other-pool
	// containers, e.g. TLC-only when planning QLC) are SKIPPED: those nodes do not host pool p, so they are
	// counted as FRESH candidates (freshGroups / kFresh) and CONVERTED to mixed by placeUniform's grow path —
	// counting them here too would double-count against kFresh*L and over-estimate the plan.
	type fdReach struct{ cap, ceiling int }
	reach := map[string]*fdReach{}
	for i := range existingDrives {
		c := &existingDrives[i]
		if c.FDValue == "" || c.Unscheduled || c.Node == "" || states[c.Node] == nil {
			continue
		}
		r := reach[c.FDValue]
		if r == nil {
			r = &fdReach{}
			reach[c.FDValue] = r
		}
		v := poolCap(c, p)
		r.cap += v
		r.ceiling += v + states[c.Node].nodeHeadroom(p, cons, false)
	}
	existingReach := func(L int) (sum int, ok bool) {
		for _, r := range reach {
			if r.cap <= 0 {
				continue // other-pool FD — counted via freshGroups/kFresh, not here (avoid double-count)
			}
			switch {
			case r.cap >= L:
				sum += r.cap
			case r.ceiling >= L:
				sum += L
			default:
				return 0, false
			}
		}
		return sum, true
	}

	overshootCap := int(cons.MaxOverProvisionFraction * float64(desiredRaw))
	overProvisionMsg := func(kFresh, level, total int) string {
		return fmt.Sprintf(
			"%s: covering +%d GiB by %d new failure domain(s) of %d GiB (over-provisioned by %d GiB, within maxOverProvisionFraction=%.2f) to avoid resizing existing containers",
			p, delta, kFresh, level, total-desiredRaw, cons.MaxOverProvisionFraction)
	}

	// freshChosen returns the first k fresh candidate FDs that can host `level` (clean-first then
	// headroom-desc, per orderFreshFdGroups) as a chosen set for placeUniform. The level filter keeps
	// the clean-first ordering from picking a node too small for the uniform chunk over a capable one.
	freshChosen := func(k, level int) []*fdGroup {
		return takeFreshAtLevel(freshGroups, k, level)
	}

	// finalizeFeasibility verifies what placeUniform ACTUALLY placed (it may roll back an FD whose hosts
	// can't hold their even share, or land below minFd) — the scan reasons over aggregate FD headroom, so a
	// post-placement check is needed, same as the other placement branches. Existing pool-p FDs are grown
	// in place here, so they count toward coverage (excludePoolPExisting=false).
	finalizeFeasibility := func() {
		finalizePoolFeasibility(p, desiredRaw, minFd, existingDrives, growth, newByNode, cons, plan, false)
	}

	// --- Step 4: no-grow attempt (preferred) — cover the missing capacity with new failure domains sized to
	// the shortfall itself (CeilDiv(delta, k)) rather than cloning the full existing FD size (T0). Sizing to
	// delta/k makes the new FDs sum to ~delta, so the pool reaches desiredRaw without over-provisioning —
	// neither the count-rounding overshoot of cloning T0 nor the sub-T0 quantization overshoot when
	// delta < T0. The PREFERRED count is kBase = CeilDiv(delta, T0): the same count T0-cloning would use, and
	// its per-FD size CeilDiv(delta, kBase) is always <= T0, so new FDs are <= the existing frozen FDs (never
	// bigger) and clean multiples of T0 stay uniform at exactly T0. Only when there aren't enough spare nodes
	// for kBase FDs may the count drop and per-FD size grow (up to maxPerFdCap = desiredRaw/minFd).
	maxPerFdCap := 0
	if minFd > 0 {
		maxPerFdCap = desiredRaw / minFd
	}
	if maxPerFdCap > 0 {
		existingAvg := poolAvg(existingDrives, p)
		kBase := util.CeilDiv(delta, T0) // count T0-cloning would use — preferred (per-FD comes out <= T0)
		// Prefer the preferred count (most FDs, smallest per-FD <= T0, uniform with existing); only when there
		// aren't enough spare nodes for that many fall to fewer, LARGER new FDs (up to maxPerFdCap). Sizing to
		// delta/k makes the new FDs sum to ~delta, so we hit desiredRaw without over-provisioning.
		for k := min(kBase, len(freshGroups)); k >= 1; k-- {
			perFd := util.CeilDiv(delta, k)
			if perFd < cons.MinChunkSizeGiB {
				perFd = cons.MinChunkSizeGiB
			}
			if perFd > maxPerFdCap {
				break // fewer FDs only make perFd larger — cannot satisfy the cap; leave to grow/infeasible
			}
			if detectImbalance(perFd, existingAvg, cons) {
				break // fewer FDs only make perFd larger — imbalance won't improve
			}
			if freshCountAtLeast(perFd) < k {
				continue // not enough spare nodes for k FDs this size; try fewer (larger) FDs
			}
			total := current + k*perFd
			if total-desiredRaw > overshootCap {
				continue
			}
			placeUniform(p, perFd, freshChosen(k, perFd), existingDrives, states, cons, growFor, newByNode, newFor)
			finalizeFeasibility()
			if plan.Infeasible == "" && total > desiredRaw {
				plan.OverProvisions = append(plan.OverProvisions, overProvisionMsg(k, perFd, total))
			}
			return
		}
	}

	// --- Step 5: grow phase — search the final FD count N for the smallest feasible uniform level L. ---
	type feasN struct {
		N, L, total int
	}
	var best *feasN
	for N := max(minFd, numExisting); N <= numExisting+len(freshGroups); N++ {
		L := max(T0, util.CeilDiv(desiredRaw, N))
		kFresh := N - numExisting
		if freshCountAtLeast(L) < kFresh {
			continue // not enough spare nodes at level L
		}
		sumE, ok := existingReach(L)
		if !ok {
			continue
		}
		total := sumE + kFresh*L
		if total < desiredRaw {
			continue
		}
		if total-desiredRaw > overshootCap {
			continue // would over-provision this pool beyond MaxOverProvisionFraction
		}
		// Smallest L; ties broken by smallest total (least over-provision).
		if best == nil || L < best.L || (L == best.L && total < best.total) {
			best = &feasN{N: N, L: L, total: total}
		}
	}

	if best == nil {
		plan.Infeasible = fmt.Sprintf(
			"%s: cannot satisfy clusterCapacity (+%d GiB) at the uniform per-failure-domain size of %d GiB. Even after growing the %d existing failure domain(s) to their nodes' limits and adding failure domains on all %d candidate node(s) (nodes not already running a %s drive container, with enough free capacity/cores/hugepages/memory), the target is still out of reach. Add more nodes (or nodes with more free resources), or lower clusterCapacity.",
			p, delta, T0, numExisting, len(freshGroups), p)
		return
	}

	if best.L == T0 {
		// Defensive: an L==T0 grow candidate means the delta can be covered by T0-sized fresh FDs, which
		// Step 4's even-split (perFd <= maxPerFdCap, and T0 <= maxPerFdCap) would already have placed and
		// returned on — so this is effectively unreachable. Handle it as a plain create-at-T0 anyway.
		kFresh := best.N - numExisting
		placeUniform(p, T0, freshChosen(kFresh, T0), existingDrives, states, cons, growFor, newByNode, newFor)
		finalizeFeasibility()
		if plan.Infeasible == "" && best.total > desiredRaw {
			plan.OverProvisions = append(plan.OverProvisions, overProvisionMsg(kFresh, T0, best.total))
		}
		return
	}

	// best.L > T0: a uniform grow is required.
	if !cons.AllowInPlaceGrowth {
		// Growth is disabled, so the preferred no-grow cover (Step 4's even-split-to-delta on fresh FDs
		// sized up to maxPerFdCap) has ALREADY been attempted above and found no feasible k — otherwise it
		// would have placed and returned. There is therefore no additional placement to try here: fall
		// straight through to the tailored infeasible message. maxPerFdCap (the per-FD ceiling =
		// desiredRaw/minFd) and the T0-clone framing (kNeeded T0-sized FDs, kAvail available) are recomputed
		// locally to describe WHY the frozen layout cannot reach the target.
		maxPerFdCap := 0
		if minFd > 0 {
			maxPerFdCap = desiredRaw / minFd
		}
		kNeeded := util.CeilDiv(delta, T0)
		kAvail := freshCountAtLeast(T0)
		if shortfall := kNeeded - kAvail; shortfall > 0 {
			// The binding constraint is the NUMBER of failure domains, not bytes-per-node: existing FDs are
			// frozen (growth disabled) and uniform distribution forces every new FD to equal the smallest
			// existing one (T0), so the only way to add capacity is one new T0-sized FD per spare node.
			plan.Infeasible = fmt.Sprintf(
				"%s: cannot satisfy clusterCapacity (+%d GiB). The %d existing failure domain(s) are frozen at %d GiB each and cannot grow because dynamic drive scaling for shared drives is disabled, so new capacity can only be added as more %d GiB failure domains — one per node not already running a %s drive container and with %d GiB of free capacity/cores/hugepages/memory. This needs %d such node(s) but only %d is/are available, so %d more node(s) are required. Either add %d more node(s), or enable enableDynamicDriveScalingForSharedDrives to grow the existing containers in place instead (aggregate free capacity elsewhere does not help — capacity on a node already hosting this pool's FD cannot be reused while growth is disabled). The maximum capacity a single failure domain may hold is %d GiB (clusterCapacity raw ÷ (stripeWidth+redundancy+hotSpare) = %d ÷ %d).",
				p, delta, numExisting, T0, T0, p, T0, kNeeded, kAvail, shortfall, shortfall, maxPerFdCap, desiredRaw, minFd)
			return
		}
		// Enough candidate nodes exist, but covering the delta with only T0-sized FDs would over-provision
		// beyond maxOverProvisionFraction; the balanced plan therefore needs to grow existing FDs, which is
		// disabled. Either allow growth or align the request to a whole number of T0 chunks.
		plan.Infeasible = fmt.Sprintf(
			"%s: cannot satisfy clusterCapacity (+%d GiB) without growing the %d existing failure domain(s) beyond their current %d GiB each, but dynamic drive scaling for shared drives is disabled. Enable enableDynamicDriveScalingForSharedDrives, or set clusterCapacity to a value that the %d GiB failure-domain size divides evenly. The maximum capacity a single failure domain may hold is %d GiB (clusterCapacity raw ÷ (stripeWidth+redundancy+hotSpare) = %d ÷ %d).",
			p, delta, numExisting, T0, T0, maxPerFdCap, desiredRaw, minFd)
		return
	}
	if float64(best.L-T0) < cons.MinGrowthFraction*float64(T0) {
		// Grow is allowed but too small (below minGrowthFraction); the T0-clone framing (kNeeded T0-sized
		// FDs across kAvail spare nodes) explains the create-new alternative that also fell short.
		kNeeded := util.CeilDiv(delta, T0)
		kAvail := freshCountAtLeast(T0)
		pct := int((100*float64(best.L-T0))/float64(T0) + 0.5)
		plan.Infeasible = fmt.Sprintf(
			"%s: cannot satisfy clusterCapacity — need +%d GiB. Adding failure domains requires %d node(s) not already running a %s drive container with >=%d GiB free each (the uniform per-FD size), but only %d is/are available. The alternative — growing existing containers in place — would raise each by only %d%% (below minGrowthFraction=%.2f), so it is skipped. Resolve by: adding %d more node(s), or raising clusterCapacity by at least one %d GiB failure-domain chunk, or lowering minGrowthFraction.",
			p, delta, kNeeded, p, T0, kAvail, pct, cons.MinGrowthFraction, kNeeded-kAvail, T0)
		return
	}

	// Grow allowed: realize at (N, Lmin) via ONE placeUniform over existing FDs (grown to Lmin) + the kFresh
	// fresh FDs (created at Lmin). New FDs are sized at Lmin (uniformity), not T0.
	chosen := existingFdsAsChosen(p, existingDrives, states, cons)
	kFresh := best.N - numExisting
	chosen = append(chosen, freshChosen(kFresh, best.L)...)
	placeUniform(p, best.L, chosen, existingDrives, states, cons, growFor, newByNode, newFor)
	finalizeFeasibility()
	if plan.Infeasible == "" && best.total > desiredRaw {
		plan.OverProvisions = append(plan.OverProvisions, overProvisionMsg(kFresh, best.L, best.total))
	}
}

// selectUniform free-selects the best uniform (N, T) for a greenfield pool: the smallest N >= minFd such
// that the N highest-headroom candidate FDs each have aggregate headroom >= T = ceil(desiredRaw/N). It
// grows N (which lowers T) until either the top-N candidates all clear T (returns them + T) or candidates
// run out (ok=false -> caller reports infeasible). candidates are headroom-desc (orderFreshFdGroups), so
// the front N are always the highest-headroom N FDs.
func selectUniform(desiredRaw, minFd int, candidates []*fdGroup, cons *CapacityConstraints) (chosen []*fdGroup, target int, ok bool) {
	for N := max(minFd, 1); N <= len(candidates); N++ {
		target = max(cons.MinChunkSizeGiB, util.CeilDiv(desiredRaw, N))
		fits := true
		for i := 0; i < N; i++ {
			if candidates[i].headroom < target {
				fits = false
				break
			}
		}
		if fits {
			return candidates[:N], target, true
		}
	}
	return nil, 0, false
}

// uniformInfeasibleMsg explains why no uniform tiling fits: the smallest usable FD caps below the per-FD
// share. It reports the per-FD share at the largest feasible N (the most forgiving tiling) and the smallest
// candidate FD headroom that falls short.
func uniformInfeasibleMsg(p poolKind, desiredRaw, minFd int, candidates []*fdGroup, states map[string]*nodeState, poolUsed map[string]struct{}, cons *CapacityConstraints) string {
	if len(candidates) < minFd {
		msg := fmt.Sprintf(
			"%s: only %d of %d required failure domains have capacity (need at least stripeWidth+redundancyLevel+hotSpare)",
			p, len(candidates), minFd)
		if breakdown := rejectedNodesBreakdown(p, states, poolUsed, cons); breakdown != "" {
			msg += " — " + breakdown
		}
		return msg
	}
	// At the largest N (all candidates) the per-FD share is smallest; the smallest candidate still caps below
	// it, so no N can tile uniformly.
	N := len(candidates)
	T := max(cons.MinChunkSizeGiB, util.CeilDiv(desiredRaw, N))
	smallest := candidates[N-1].headroom
	return fmt.Sprintf(
		"%s: cannot place %d GiB uniformly across %d failure domains — the smallest usable FD holds %d GiB, below the %d GiB per-FD share; add capacity or lower clusterCapacity",
		p, desiredRaw, N, smallest, T)
}

// rejectedNodesBreakdown explains why the candidate failure-domain set fell short: every node that is NOT
// a usable pool-p candidate is bucketed by its binding reason — it already hosts a pool-p container, or the
// dimension (drive capacity / cores / hugepages / memory) that caps its usable headroom below the MinChunk
// floor — and nodes sharing a reason are listed together (e.g. "n4, n5, n6: no QLC drive capacity"). Names
// are sorted for determinism; both the names per reason and the number of distinct reasons are capped with
// "+N more" tails to keep the event readable. Returns "" when nothing was rejected.
func rejectedNodesBreakdown(p poolKind, states map[string]*nodeState, poolUsed map[string]struct{}, cons *CapacityConstraints) string {
	const maxNamesPerReason = 6
	const maxReasons = 8

	names := make([]string, 0, len(states))
	for name := range states {
		names = append(names, name)
	}
	sort.Strings(names)

	type reasonGroup struct {
		nodes []string // member names (sorted, capped at maxNamesPerReason)
		total int       // total nodes with this reason (may exceed len(nodes))
	}
	byReason := map[string]*reasonGroup{}
	order := make([]string, 0) // reason text in first-seen (name-sorted) order
	rejected := 0

	add := func(node, reason string) {
		rejected++
		g := byReason[reason]
		if g == nil {
			g = &reasonGroup{}
			byReason[reason] = g
			order = append(order, reason)
		}
		g.total++
		if len(g.nodes) < maxNamesPerReason {
			g.nodes = append(g.nodes, node)
		}
	}

	for _, name := range names {
		ns := states[name]
		if _, used := poolUsed[name]; used {
			add(name, fmt.Sprintf("already hosts a %s container", p))
			continue
		}
		h, binding := ns.nodeHeadroomBinding(p, cons, true)
		if h >= cons.MinChunkSizeGiB {
			continue // usable candidate — not rejected
		}
		if binding == "drive capacity" && h == 0 {
			add(name, fmt.Sprintf("no %s drive capacity", p))
		} else {
			add(name, fmt.Sprintf("%s limits usable %s to %d GiB (below the %d GiB minimum chunk)", binding, p, h, cons.MinChunkSizeGiB))
		}
	}
	if rejected == 0 {
		return ""
	}

	parts := make([]string, 0, len(order))
	for i, reason := range order {
		if i >= maxReasons {
			parts = append(parts, fmt.Sprintf("(+%d more reason(s))", len(order)-maxReasons))
			break
		}
		g := byReason[reason]
		list := strings.Join(g.nodes, ", ")
		if g.total > len(g.nodes) {
			list += fmt.Sprintf(" (+%d more)", g.total-len(g.nodes))
		}
		parts = append(parts, fmt.Sprintf("%s: %s", list, reason))
	}
	return fmt.Sprintf("%d node(s) cannot host a %s failure domain: %s", rejected, p, strings.Join(parts, "; "))
}

// existingFdsAsChosen builds one *fdGroup per existing pool-bearing failure domain (its member node states,
// headroom desc), so the existing FDs can participate in placeUniform alongside fresh FDs. placeUniform's
// per-host grow-or-create resolves whether each host grows an existing container or creates a new one and
// reads only g.nodes (not g.headroom — left zero here). Cap-0 (other-pool) containers are included so an
// existing TLC-only FD participates in the QLC pool and is converted to mixed.
func existingFdsAsChosen(p poolKind, existingDrives []ExistingContainer, states map[string]*nodeState, cons *CapacityConstraints) []*fdGroup {
	byFd := map[string]*fdGroup{}
	order := make([]*fdGroup, 0)
	seen := map[string]struct{}{}
	for i := range existingDrives {
		c := &existingDrives[i]
		if c.FDValue == "" || c.Unscheduled || c.Node == "" || states[c.Node] == nil {
			continue
		}
		// A pool-p FD is one that already carries pool p anywhere. An other-pool-only FD is NOT pre-existing
		// for this pool — it belongs to the fresh/greenfield candidate path, not here.
		if poolExistingFds(p, []ExistingContainer{*c}) == 0 {
			continue
		}
		g := byFd[c.FDValue]
		if g == nil {
			g = &fdGroup{}
			byFd[c.FDValue] = g
			order = append(order, g)
		}
		if _, dup := seen[c.Node]; dup {
			continue
		}
		seen[c.Node] = struct{}{}
		g.nodes = append(g.nodes, states[c.Node])
	}
	for _, g := range order {
		sortNodesByHeadroomDesc(g.nodes, p, cons)
	}
	return order
}

// sortNodesByHeadroomDesc orders an FD group's member node states by pool-p headroom desc (node name asc to
// tie-break), so an even split that rounds an extra GiB onto the first hosts lands them on the fattest nodes.
func sortNodesByHeadroomDesc(nodes []*nodeState, p poolKind, cons *CapacityConstraints) {
	sort.SliceStable(nodes, func(i, j int) bool {
		hi, hj := nodes[i].nodeHeadroom(p, cons, false), nodes[j].nodeHeadroom(p, cons, false)
		if hi != hj {
			return hi > hj
		}
		return nodes[i].nc.NodeName < nodes[j].nc.NodeName
	})
}

// placeUniform is the ONE placement primitive: make each chosen FD hold exactly `T` of pool p, split EVENLY
// across the FD's member hosts (not greedy — the label-FD per-host balance requirement). Per host: if a
// container already exists on that node it is GROWN (cross-pool conversion TLC-only -> mixed AND same-pool
// top-up); else a new container is CREATED. One grow-or-create path. Brand-new containers honor the MinChunk
// floor and charge per-container base memory once; grows never charge base memory. An FD that cannot reach
// `T` is rolled back and skipped (poolFeasibility then flags the shortfall) rather than left sub-T.
func placeUniform(
	p poolKind,
	target int,
	chosen []*fdGroup,
	existingDrives []ExistingContainer,
	states map[string]*nodeState,
	cons *CapacityConstraints,
	growFor func(ExistingContainer) *ContainerGrowth,
	newByNode map[string]*NewContainer,
	newFor func(node, fd string) *NewContainer,
) {
	// existingOnNode maps a node to the pool-bearing-OR-other-pool existing container it hosts (for the grow
	// path). A node carrying any existing drive container grows in place; one without creates a new container.
	existingOnNode := map[string]ExistingContainer{}
	for i := range existingDrives {
		c := existingDrives[i]
		if c.Node != "" && !c.Unscheduled {
			existingOnNode[c.Node] = c
		}
	}
	existedNode := func(node string) bool { _, ok := newByNode[node]; return ok }
	hasNew := func(node string) bool {
		nn, ok := newByNode[node]
		return ok && poolCapNew(nn, p) > 0
	}

	for _, g := range chosen {
		if len(g.nodes) == 0 {
			continue
		}
		fdValue := g.nodes[0].nc.FDValue

		// Per-host target: the FD's T split as evenly as possible across its member hosts (first `rem` hosts
		// get one extra GiB). Each host's target is the ADDITIONAL pool-p capacity it must end up holding from
		// this placement (its even share); existing same-pool capacity on the node already counts toward the
		// FD total, so subtract it from the host's share.
		nHosts := len(g.nodes)
		base := target / nHosts
		rem := target % nHosts

		// Roll back the whole FD if it cannot reach T (mirror consume/grow exactly).
		type undo struct {
			ns      *nodeState
			add     int
			inc     bool
			grow    bool
			grewFor ExistingContainer
		}
		var moves []undo
		placedFD := 0
		for hi, ns := range g.nodes {
			share := base
			if hi < rem {
				share++
			}
			node := ns.nc.NodeName
			// Existing same-pool capacity on this node already contributes; only the deficit toward `share`.
			ec, hasExisting := existingOnNode[node]
			cur := 0
			if hasExisting {
				cur = poolCap(&ec, p)
			}
			need := share - cur
			if need <= 0 {
				placedFD += min(cur, share)
				continue
			}
			if hasExisting {
				// GROW path (same-pool top-up OR cross-pool conversion of an other-pool container to mixed).
				room := ns.nodeHeadroom(p, cons, false)
				add := min(need, room)
				if add <= 0 {
					continue
				}
				addPoolGrowth(growFor(ec), p, add)
				ns.consume(p, add, cons, false)
				moves = append(moves, undo{ns: ns, add: add, grow: true, grewFor: ec})
				placedFD += cur + add
				continue
			}
			// CREATE path.
			includeBase := !existedNode(node)
			room := ns.nodeHeadroom(p, cons, includeBase)
			if room <= 0 {
				continue
			}
			minAdd := cons.MinChunkSizeGiB
			if hasNew(node) {
				minAdd = 1 // top-up to an already-created this-pool container may be small
			}
			add := min(need, room)
			if add < minAdd {
				continue
			}
			addPoolNew(newFor(node, fdValue), p, add)
			ns.consume(p, add, cons, includeBase)
			moves = append(moves, undo{ns: ns, add: add, inc: includeBase})
			placedFD += add
		}
		if placedFD < target {
			// Could not reach the uniform level on this FD; roll back and skip it.
			for _, m := range moves {
				if m.grow {
					addPoolGrowth(growFor(m.grewFor), p, -m.add)
					ns := m.ns
					ns.unconsume(p, m.add, cons, false)
				} else {
					addPoolNew(newFor(m.ns.nc.NodeName, fdValue), p, -m.add)
					m.ns.unconsume(p, m.add, cons, m.inc)
				}
			}
		}
	}
}

// poolExistingFds counts the distinct failure domains among this cluster's existing containers that
// already carry pool p.
func poolExistingFds(p poolKind, existingDrives []ExistingContainer) int {
	fds := map[string]struct{}{}
	for i := range existingDrives {
		if poolCap(&existingDrives[i], p) > 0 && existingDrives[i].FDValue != "" {
			fds[existingDrives[i].FDValue] = struct{}{}
		}
	}
	return len(fds)
}

// poolCurrent sums this cluster's current capacity for pool p across its existing drive containers.
func poolCurrent(existingDrives []ExistingContainer, p poolKind) int {
	sum := 0
	for i := range existingDrives {
		sum += poolCap(&existingDrives[i], p)
	}
	return sum
}

// poolNodeUsed is the set of nodes already hosting a pool-p container; they are grown in place, never
// given a sibling (one container of a type per host in AUTO mode), so fresh placement skips them.
func poolNodeUsed(existingDrives []ExistingContainer, p poolKind) map[string]struct{} {
	used := map[string]struct{}{}
	for i := range existingDrives {
		if poolCap(&existingDrives[i], p) > 0 && existingDrives[i].Node != "" {
			used[existingDrives[i].Node] = struct{}{}
		}
	}
	return used
}

// OverProvisionCapGiB is the GiB a pool may exceed its desired raw capacity WITHOUT triggering the
// ClusterCapacityShrink advisory. The create-new-before-grow path over-provisions by up to one uniform
// chunk (bounded by MaxOverProvisionFraction) on purpose, so that intentional overage stays silent rather
// than nag the operator to delete containers (it would otherwise contradict the ClusterCapacityOverProvisioned
// event). NOTE: this also suppresses the advisory for a deliberate clusterCapacity DOWNSIZE smaller than
// this fraction — acceptable since such an overage is minor and visible via capacity inspection.
func OverProvisionCapGiB(desiredRaw int, cons *CapacityConstraints) int {
	if desiredRaw <= 0 {
		return 0
	}
	return int(cons.MaxOverProvisionFraction * float64(desiredRaw))
}

// shrinkMsg is the ClusterCapacityShrink advisory for an over-provisioned pool (never auto-applied).
func shrinkMsg(p poolKind, desiredRaw, current int) string {
	return fmt.Sprintf(
		"%s capacity is over-provisioned by %d GiB (desired %d, current %d); delete WekaContainers manually to shrink — the operator never auto-shrinks",
		p, current-desiredRaw, desiredRaw, current)
}

// resolveExactNewFds maps an explicit driveContainers count (targetFds) onto the EXACT number of fresh FDs
// this pool must add, or -1 when driveContainers is unset (auto — selectUniform picks N via the uniform rule).
// It enforces the fail-fast checks: the pinned count cannot be below the FDs already present, cannot require
// placement with no room left, and cannot drive a per-container share below MinChunk. delta is the capacity
// still to place.
func resolveExactNewFds(p poolKind, targetFds int, existingDrives []ExistingContainer, delta int, cons *CapacityConstraints) (newFds int, errMsg string) {
	if targetFds <= 0 {
		return -1, ""
	}
	existingFds := poolExistingFds(p, existingDrives)
	exactNewFds := targetFds - existingFds
	if exactNewFds < 0 {
		return 0, fmt.Sprintf(
			"%s: driveContainers requires %d failure domains for this pool but %d already exist; the operator never removes containers — delete them manually to reduce the count",
			p, targetFds, existingFds)
	}
	if delta > 0 && exactNewFds == 0 {
		return 0, fmt.Sprintf(
			"%s: cannot place %d GiB without new failure domains, but driveContainers caps this pool at %d (already reached)", p, delta, targetFds)
	}
	if exactNewFds > 0 && delta/exactNewFds < cons.MinChunkSizeGiB {
		return 0, fmt.Sprintf(
			"%s: driveContainers=%d makes the per-container share %d GiB, below the %d GiB minimum chunk; reduce driveContainers or raise clusterCapacity",
			p, targetFds, delta/exactNewFds, cons.MinChunkSizeGiB)
	}
	return exactNewFds, ""
}

// fdGroup is a failure domain's candidate nodes for new-container placement: its member node states
// (headroom desc) and aggregate headroom (sum over members) for the fit check. Built by orderFreshFdGroups.
type fdGroup struct {
	nodes    []*nodeState // nodes in this FD, headroom desc (inherited from cands' order)
	headroom int          // aggregate FD headroom (sum over its candidate nodes), for the fit check
	// hasDeletingDriveContainer is true when any member node still hosts a this-cluster drive container being
	// deleted. takeFreshAtLevel deprioritizes such FDs so a replacement is not recreated on the node it
	// was just deleted from while a free FD exists.
	hasDeletingDriveContainer bool
}

// takeFreshAtLevel returns up to k fresh candidate FDs that can host `level`, preferring FDs with no
// deleting container over those that have one (each tier kept in the headroom-desc order from
// orderFreshFdGroups). A node still hosting a this-cluster drive container being deleted re-enters the
// fresh pool the instant that container leaves existingDrives, and once its capacity frees it is the
// emptiest (highest-headroom) FD — so by raw headroom it would win and the replacement FD would be
// recreated on the node it was just deleted from. Taking not-deleting FDs first avoids that, while the
// `level` filter keeps a too-small not-deleting FD from being chosen over a capable deleting one — the
// deleting FD stays eligible as a fallback (e.g. the only node with scarce QLC drives). When no FD
// hosts a deleting container this is simply the front-k of the headroom-desc list.
func takeFreshAtLevel(fresh []*fdGroup, k, level int) []*fdGroup {
	out := make([]*fdGroup, 0, k)
	for _, wantDeleting := range []bool{false, true} {
		for _, g := range fresh {
			if len(out) >= k {
				return out
			}
			if g.headroom >= level && g.hasDeletingDriveContainer == wantDeleting {
				out = append(out, g)
			}
		}
	}
	return out
}

// orderFreshFdGroups returns the failure domains with placeable headroom for pool p — candidate nodes not
// already hosting a this-pool container (poolNodeUsed), each with >= MinChunk headroom — grouped by FDValue
// and ordered by best-node headroom desc (first-seen). Shared by selectUniform / placeUniform (and the
// uniform-increase fresh-candidate scan) so selection and placement agree on the candidate FD set and its
// order. In AUTO mode (FDValue == node name) each group holds one node, so the order is identical to the
// plain node-headroom-desc sort.
func orderFreshFdGroups(p poolKind, states map[string]*nodeState, poolNodeUsed map[string]struct{}, cons *CapacityConstraints) []*fdGroup {
	var cands []*nodeState
	for _, ns := range states {
		if _, used := poolNodeUsed[ns.nc.NodeName]; used {
			continue
		}
		if ns.nodeHeadroom(p, cons, true) >= cons.MinChunkSizeGiB {
			cands = append(cands, ns)
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		hi, hj := cands[i].nodeHeadroom(p, cons, true), cands[j].nodeHeadroom(p, cons, true)
		if hi != hj {
			return hi > hj
		}
		return cands[i].nc.NodeName < cands[j].nc.NodeName
	})
	byFD := map[string]*fdGroup{}
	order := make([]*fdGroup, 0, len(cands)) // FDs in first-seen order == best-node-headroom desc
	for _, ns := range cands {
		g := byFD[ns.nc.FDValue]
		if g == nil {
			g = &fdGroup{}
			byFD[ns.nc.FDValue] = g
			order = append(order, g)
		}
		g.nodes = append(g.nodes, ns)
		g.headroom += ns.nodeHeadroom(p, cons, true)
		if ns.hasDeletingDriveContainer {
			g.hasDeletingDriveContainer = true
		}
	}
	return order
}

// orderNodesByFDSpread reorders `nodes` so that a prefix of the result spans as many distinct failure
// domains as possible before repeating one. It groups the nodes by their FDValue, ranks the FD groups by
// their best (highest-headroom) member node desc (FDValue asc to tie-break), keeps each group's members
// in headroom-desc order, then emits round-robin across the groups: every FD's best node, then every FD's
// second-best node, and so on. headroom returns the per-node ranking score; fdOf returns a node's FDValue.
//
// This is the compute-side analog of distributeFreshEven's per-FD selection (#10): a best-fit-by-node ordering
// can place several of the first picks on nodes that share one FDValue, collapsing the chosen prefix into
// fewer distinct FDs than the cluster requires. Emitting round-robin guarantees the first k picks cover
// min(k, #distinct FDs) failure domains.
//
// AUTO mode (FDValue == node name, one node per FD) is byte-for-byte unchanged: each group has exactly one
// node, the FD ranking equals the node-headroom ranking, and the round-robin emits one node per group in
// that order — identical to the plain headroom-desc node sort.
func orderNodesByFDSpread(nodes []string, headroom func(node string) int, fdOf func(node string) string) []string {
	type fdGroup struct {
		fd    string
		nodes []string // members, headroom desc
	}
	byFD := map[string]*fdGroup{}
	groups := make([]*fdGroup, 0, len(nodes))
	for _, n := range nodes {
		g := byFD[fdOf(n)]
		if g == nil {
			g = &fdGroup{fd: fdOf(n)}
			byFD[fdOf(n)] = g
			groups = append(groups, g)
		}
		g.nodes = append(g.nodes, n)
	}
	for _, g := range groups {
		sort.SliceStable(g.nodes, func(i, j int) bool {
			hi, hj := headroom(g.nodes[i]), headroom(g.nodes[j])
			if hi != hj {
				return hi > hj
			}
			return g.nodes[i] < g.nodes[j]
		})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		hi, hj := headroom(groups[i].nodes[0]), headroom(groups[j].nodes[0])
		if hi != hj {
			return hi > hj
		}
		return groups[i].fd < groups[j].fd
	})
	out := make([]string, 0, len(nodes))
	for round := 0; len(out) < len(nodes); round++ {
		for _, g := range groups {
			if round < len(g.nodes) {
				out = append(out, g.nodes[round])
			}
		}
	}
	return out
}

// poolCapNew returns a new container's capacity for the given pool.
func poolCapNew(n *NewContainer, p poolKind) int {
	if p == poolTLC {
		return n.TlcGiB
	}
	return n.QlcGiB
}

// poolFeasibility returns a non-empty reason when the pool cannot be satisfied: capacity left
// unplaced, or fewer than minFd distinct failure domains carry the pool after planning.
func poolFeasibility(
	p poolKind,
	minFd, remaining int,
	existingDrives []ExistingContainer,
	growth map[string]*ContainerGrowth,
	newByNode map[string]*NewContainer,
	cons *CapacityConstraints,
) string {
	if remaining > 0 {
		if !cons.AllowInPlaceGrowth {
			// In-place growth disabled: the only way to place this would have been to extend existing
			// containers. Name the real cause so operators add nodes/capacity or re-enable the flag.
			return fmt.Sprintf(
				"%s: dynamic drive scaling for shared drives is disabled; cannot place %d GiB of growth on new failure domains and existing containers are not extended — add nodes/capacity or enable enableDynamicDriveScalingForSharedDrives",
				p, remaining)
		}
		return fmt.Sprintf("%s: cannot place %d GiB — not enough node capacity/cores/hugepages/memory across candidate failure domains", p, remaining)
	}
	// Count the FINAL state: a TLC-only container grown into this pool now carries it.
	if fds := finalPerFD(p, existingDrives, growth, newByNode); len(fds) < minFd {
		return fmt.Sprintf("%s: only %d of %d required failure domains have capacity (need at least stripeWidth+redundancyLevel+hotSpare)", p, len(fds), minFd)
	}
	return ""
}

// splitDriveContainers maps an explicit total DriveContainers onto per-pool EXACT FD targets. With both
// pools active it splits the total by raw-capacity ratio (rounded to nearest); a total below minFd, or a
// per-pool share below minFd, is a hard constraint violation (returns a non-empty reason → fail fast).
func splitDriveContainers(d DesiredCapacity, minFd int) (tlcN, qlcN int, reason string) {
	total := d.DriveContainers
	if total < minFd {
		return 0, 0, fmt.Sprintf(
			"driveContainers=%d is below the required %d failure domains (stripeWidth+redundancyLevel+hotSpare)", total, minFd)
	}
	tlcActive, qlcActive := d.TlcRawGiB > 0, d.QlcRawGiB > 0
	switch {
	case tlcActive && qlcActive:
		tlcN = util.RoundDiv(total*d.TlcRawGiB, d.TlcRawGiB+d.QlcRawGiB)
		qlcN = total - tlcN
		if tlcN < minFd || qlcN < minFd {
			return 0, 0, fmt.Sprintf(
				"driveContainers=%d split by ratio (TLC %d, QLC %d) puts a pool below the %d-failure-domain minimum; increase driveContainers or adjust driveTypesRatio",
				total, tlcN, qlcN, minFd)
		}
	case tlcActive:
		tlcN = total
	case qlcActive:
		qlcN = total
	}
	return tlcN, qlcN, ""
}

// distinctDriveFds counts the distinct failure domains that carry drive capacity in the FINAL state
// (existing containers, all of which keep their capacity, plus newly created ones). In AUTO FD mode this
// equals the drive-container count, so it is the realized value compared against an explicit DriveContainers.
// existing is drive-only so the FDValue != "" filter is intentional (compute containers have no FD).
func distinctDriveFds(existingDrives []ExistingContainer, newByNode map[string]*NewContainer) int {
	fds := map[string]struct{}{}
	for i := range existingDrives {
		if existingDrives[i].FDValue != "" {
			fds[existingDrives[i].FDValue] = struct{}{}
		}
	}
	for _, n := range newByNode {
		if n.FDValue != "" {
			fds[n.FDValue] = struct{}{}
		}
	}
	return len(fds)
}

// totalTlcDriveCores sums TLC drive cores across the FINAL state of all TLC-bearing containers
// (unchanged existing + grown + newly created), driving the compute 1:1 ratio downstream.
func totalTlcDriveCores(
	existingDrives []ExistingContainer,
	growth map[string]*ContainerGrowth,
	newByNode map[string]*NewContainer,
	cons *CapacityConstraints,
) int {
	total := 0
	for i := range existingDrives {
		total += TlcDriveCores(finalPoolCap(&existingDrives[i], growth, poolTLC), cons)
	}
	for _, n := range newByNode {
		total += TlcDriveCores(n.TlcGiB, cons)
	}
	return total
}
