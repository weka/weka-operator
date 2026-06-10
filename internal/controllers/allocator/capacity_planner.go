package allocator

import (
	"fmt"
	"sort"

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
// for a clusterCapacity cluster. Production durability requires 3+2+1 (parity>=2). When the
// operator-level AllowSingleParity flag is set, the floor drops to single-parity 2+1+0 so QA/test
// schemes such as 2+1 (minFdNum=3) are accepted. QA/test only — a single parity chunk leaves a stripe
// unprotected during rebuild. The same flag drives the allow_1_parity weka override at formation.
func MinProtectionFloor() (stripeWidth, redundancyLevel, hotSpare int) {
	if globalconfig.Config.DriveSharing.AllowSingleParity {
		return 2, 1, 0
	}
	return 3, 2, 1
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
	// ImbalanceFactor gates the heterogeneous "balanced fresh" fallback: 8.0 == 8.0x (default).
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
	// infeasible if none can be placed. This keeps the cluster-level planner consistent with the
	// container-level NeedsDrivesToAllocate() gate, which already refuses live virtual-drive allocation
	// when the flag is off.
	AllowInPlaceGrowth bool
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
	// Imbalances carries per-FD imbalance advisories ("usable capacity gated by the smallest FD"). These
	// are NOT growth — they can fire even on a pure create — so the controller emits them under their own
	// ClusterCapacityImbalance event reason, separate from the growth Warnings above.
	Imbalances   []string
	ShrinkEvents []string
	Infeasible   string
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
	perCap := perCoreCap(p, cons)
	if perCap <= 0 {
		return 0
	}
	headroom := ns.poolFree(p)
	if coreCap := ns.coresFree * perCap; coreCap < headroom {
		headroom = coreCap
	}
	if hpPerCore := cons.driveHugepagesPerCoreMiB(); hpPerCore > 0 {
		if hpCap := (ns.hugepagesMiB / hpPerCore) * perCap; hpCap < headroom {
			headroom = hpCap
		}
	}
	if cons.MemoryPerCoreMiB > 0 {
		mem := ns.memoryMiB
		if includeBase {
			mem -= cons.MemoryBaseMiB
		}
		if memCap := (mem / cons.MemoryPerCoreMiB) * perCap; memCap < headroom {
			headroom = memCap
		}
	}
	if headroom < 0 {
		return 0
	}
	return headroom
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

// detectImbalance reports whether laying out new containers of size newPerFD alongside existing
// containers of average size existingAvg would be too skewed — true when newPerFD >= factor ×
// existingAvg (factor from ImbalanceFactor). Returns false when there are no existing
// containers to compare against.
func detectImbalance(newPerFD, existingAvg int, cons *CapacityConstraints) bool {
	if existingAvg <= 0 || cons.ImbalanceFactor <= 0 {
		return false
	}
	return float64(newPerFD) >= float64(existingAvg)*cons.ImbalanceFactor
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
			nc:           nc,
			tlcFree:      nc.TlcGiB,
			qlcFree:      nc.QlcGiB,
			coresFree:    nc.AllocatableCPU,
			hugepagesMiB: nc.AvailableHugepagesMiB,
			memoryMiB:    nc.AvailableMemoryMiB,
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

	// Per-FD imbalance advisory (non-fatal): usable capacity is gated by the smallest FD. Emitted under
	// the ClusterCapacityImbalance reason — it is not a growth and can fire even on a pure create.
	if plan.Infeasible == "" {
		if w := imbalanceWarning(poolTLC, existingDrives, growth, newByNode); w != "" {
			plan.Imbalances = append(plan.Imbalances, w)
		}
		if w := imbalanceWarning(poolQLC, existingDrives, growth, newByNode); w != "" {
			plan.Imbalances = append(plan.Imbalances, w)
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

// imbalanceWarning sums realized per-FD capacity of a pool across the final state (existing as grown +
// new) and returns a non-fatal warning when the smallest and largest FD differ by more than 10%.
func imbalanceWarning(
	p poolKind,
	existingDrives []ExistingContainer,
	growth map[string]*ContainerGrowth,
	newByNode map[string]*NewContainer,
) string {
	perFD := finalPerFD(p, existingDrives, growth, newByNode)
	if len(perFD) < 2 {
		return ""
	}
	lo, hi := 0, 0
	for _, v := range perFD {
		if lo == 0 || v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	if imbalanceExceeds(lo, hi) {
		return fmt.Sprintf("%s failure-domain imbalance: smallest FD holds %d GiB, largest %d GiB; usable capacity is gated by the smallest", p, lo, hi)
	}
	return ""
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
	if desiredRaw <= 0 {
		return
	}

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

	current := poolCurrent(existingDrives, p)
	delta := desiredRaw - current
	if delta < 0 {
		plan.ShrinkEvents = append(plan.ShrinkEvents, shrinkMsg(p, desiredRaw, current))
		return
	}
	if delta == 0 {
		return
	}

	// Heterogeneous fallback: when a fresh per-FD chunk would dwarf the existing tiny FDs, lay out a fresh
	// balanced set instead and mark the old containers deletable (preserves usable capacity). Skipped with
	// pinned driveContainers or in-place growth off; falls through to the incremental water-fill when a
	// fresh balanced set is not feasible.
	if cons.AllowInPlaceGrowth && targetFds == 0 &&
		detectImbalance(growChunk(delta, minFd, cons), poolAvg(existingDrives, p), cons) {
		if balancedFresh(p, desiredRaw, minFd, existingDrives, states, cons, newByNode, newFor, plan) {
			return
		}
	}

	// Build the failure-domain model (existing FDs grow in place; fresh FDs create) and place delta with a
	// single water-fill over the combined set. freeze (in-place growth off) pins existing ceilings to cap,
	// so the whole delta lands on fresh FDs. Balance is per FAILURE DOMAIN, not per container.
	existing := existingFdFills(p, existingDrives, states, cons, !cons.AllowInPlaceGrowth)
	fresh := freshFdFills(p, existing, states, poolNodeUsed(existingDrives, p), cons)

	exactNewFds, reason := resolveExactNewFds(p, targetFds, existingDrives, delta, cons)
	if reason != "" {
		plan.Infeasible = reason
		return
	}

	remaining := waterFill(p, desiredRaw, minFd, exactNewFds, existing, fresh, cons, growFor, newByNode, newFor)

	if reason := poolFeasibility(p, minFd, remaining, existingDrives, growth, newByNode, cons); reason != "" {
		plan.Infeasible = reason
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

// poolAvg is the mean per-container capacity for pool p over the containers that carry it (0 if none).
// It is the baseline the heterogeneous-fallback trigger compares a fresh grow chunk against.
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

// growChunk is the per-FD capacity a grow would add if delta were spread evenly across minFd FDs, floored
// at MinChunk — the magnitude the heterogeneous-fallback trigger weighs against the existing average.
func growChunk(delta, minFd int, cons *CapacityConstraints) int {
	return max(cons.MinChunkSizeGiB, util.CeilDiv(delta, minFd))
}

// shrinkMsg is the ClusterCapacityShrink advisory for an over-provisioned pool (never auto-applied).
func shrinkMsg(p poolKind, desiredRaw, current int) string {
	return fmt.Sprintf(
		"%s capacity is over-provisioned by %d GiB (desired %d, current %d); delete WekaContainers manually to shrink — the operator never auto-shrinks",
		p, current-desiredRaw, desiredRaw, current)
}

// resolveExactNewFds maps an explicit driveContainers count (targetFds) onto the EXACT number of fresh FDs
// this pool must add, or -1 when driveContainers is unset (auto — waterFill selects N via the even rule).
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

// balancedFresh is the heterogeneous-fallback POLICY: lay out a fresh, internally-balanced set of new
// containers holding the FULL desiredRaw across the capable FRESH failure domains (those without an
// existing this-pool container), ignoring the existing containers entirely (they are presumed deletable
// once data migrates). It is a thin wrapper over the unified waterFill — it builds the fresh-only
// participating set and water-fills desiredRaw over it with NO existing FDs — plus the delete-old advisory
// warning. Returns true when the full desiredRaw was placed across at least minFd FDs.
func balancedFresh(
	p poolKind,
	desiredRaw, minFd int,
	existingDrives []ExistingContainer,
	states map[string]*nodeState,
	cons *CapacityConstraints,
	newByNode map[string]*NewContainer,
	newFor func(node, fd string) *NewContainer,
	plan *CapacityPlan,
) bool {
	// FDs that already host an existing this-pool container are NOT fresh — the fresh balanced set stands
	// on its own across NEW failure domains. poolNodeUsed (keyed by node) likewise keeps freshFdFills from
	// re-using a node that already carries this pool.
	poolNodeUsed := map[string]struct{}{}
	existingFD := map[string]struct{}{}
	for _, c := range existingDrives {
		if poolCap(&c, p) > 0 {
			if c.Node != "" {
				poolNodeUsed[c.Node] = struct{}{}
			}
			if c.FDValue != "" {
				existingFD[c.FDValue] = struct{}{}
			}
		}
	}

	// Fresh-only participating set: every genuinely-fresh FD with placeable headroom, excluding the FDs the
	// existing containers occupy. (freshFdFills already drops FDs present in its `existing` arg; pass an
	// existing-FD stub set so a fresh slot never describes a held FD.)
	excludeExisting := make([]*fdFill, 0, len(existingFD))
	for fd := range existingFD {
		excludeExisting = append(excludeExisting, &fdFill{fd: fd})
	}
	fresh := freshFdFills(p, excludeExisting, states, poolNodeUsed, cons)
	if len(fresh) < minFd {
		return false // cannot form a fresh balanced set; fall back to the incremental path
	}

	// All-or-nothing pre-check (no mutation): the fresh set's aggregate ceiling must hold the FULL
	// desiredRaw, otherwise fall through to the incremental path with states untouched. waterFill only
	// mutates as it places, so this necessary-condition guard keeps a partial fresh layout from corrupting
	// the fallback. (The per-FD MinChunk floor is satisfied because the heterogeneous trigger implies
	// desiredRaw/minFd >= MinChunk.)
	totalCeiling := 0
	for _, f := range fresh {
		totalCeiling += f.ceiling
	}
	if totalCeiling < desiredRaw {
		return false
	}

	// Water-fill the FULL desiredRaw over the fresh-only set (no existing FDs participate). remaining ==
	// desiredRaw because every fresh FD starts at cap 0.
	left := waterFill(p, desiredRaw, minFd, -1 /*auto N*/, nil /*no existing*/, fresh, cons, nil /*never grows*/, newByNode, newFor)
	if left > 0 {
		return false
	}

	// Count distinct fresh FDs that actually received capacity, for the advisory.
	placedFds := map[string]struct{}{}
	for _, f := range fresh {
		for _, ns := range f.hosts {
			if n, ok := newByNode[ns.nc.NodeName]; ok && poolCapNew(n, p) > 0 {
				placedFds[f.fd] = struct{}{}
			}
		}
	}
	n := len(placedFds)
	if n < minFd {
		return false
	}
	plan.Warnings = append(plan.Warnings, fmt.Sprintf(
		"%s capacity grew heterogeneously: created a fresh balanced set of ~%d GiB across %d failure domains. "+
			"The older, smaller drive containers can be deleted manually once data has migrated.",
		p, util.CeilDiv(desiredRaw, n), n))
	return true
}

// fdGroup is a failure domain's candidate nodes for new-container placement: its member node states
// (headroom desc) and aggregate headroom (sum over members) for the fit check. Built by orderFreshFdGroups.
type fdGroup struct {
	nodes    []*nodeState // nodes in this FD, headroom desc (inherited from cands' order)
	headroom int          // aggregate FD headroom (sum over its candidate nodes), for the fit check
}

// orderFreshFdGroups returns the failure domains with placeable headroom for pool p — candidate nodes not
// already hosting a this-pool container (poolNodeUsed), each with >= MinChunk headroom — grouped by FDValue
// and ordered by best-node headroom desc (first-seen). Shared by freshFdFills (which feeds the unified
// waterFill) so selection and fill agree on the candidate FD set and its order. In AUTO mode (FDValue ==
// node name) each group holds one node, so the order is identical to the plain node-headroom-desc sort.
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
	}
	return order
}

// fdFill is one failure domain the planner fills for a pool: its candidate hosts in fill order, the
// running this-pool capacity (seeded with the FD's current capacity for an existing FD, 0 for a fresh
// one), the ceiling it can be filled to (current + Σ host headroom), and — for an existing FD — the
// containers to grow, index-aligned with hosts. A nil grow slice marks a fresh FD (create new containers).
type fdFill struct {
	fd      string
	cap     int
	ceiling int
	hosts   []*nodeState
	grow    []ExistingContainer
}

// existingFdFills builds one fdFill per failure domain that this cluster already occupies — every
// scheduled container grouped by FDValue. Each FD's hosts and grow targets are its containers' nodes
// ordered smallest-this-pool-capacity first, cap = Σ current this-pool capacity, ceiling = cap + Σ host
// headroom; FDs are returned smallest-cap first so the grow phase tops up the smallest FDs first, leveling
// pre-existing skew. A TLC-only container appears here for the QLC pool with cap 0, so growing its QLC
// converts it to mixed in place (and vice-versa).
//
// When freezeExisting is set (enableDynamicDriveScalingForSharedDrives=false), each FD's ceiling is
// pinned to its current cap — no host headroom is added — so waterFill still counts the FD toward the
// per-FD target denominator (keeping new FDs sized to the common even target) but can never grow it: with
// ceiling == cap the FD's water-fill increment is forced to zero, so the whole delta lands on fresh FDs.
func existingFdFills(p poolKind, existingDrives []ExistingContainer, states map[string]*nodeState, cons *CapacityConstraints, freezeExisting bool) []*fdFill {
	growable := make([]ExistingContainer, 0, len(existingDrives))
	for _, c := range existingDrives {
		if c.Unscheduled || c.Node == "" || states[c.Node] == nil {
			continue
		}
		growable = append(growable, c)
	}
	sort.Slice(growable, func(i, j int) bool {
		ci, cj := poolCap(&growable[i], p), poolCap(&growable[j], p)
		if ci != cj {
			return ci < cj // smallest this-pool capacity first
		}
		if growable[i].FDValue != growable[j].FDValue {
			return growable[i].FDValue < growable[j].FDValue
		}
		if growable[i].Node != growable[j].Node {
			return growable[i].Node < growable[j].Node
		}
		return growable[i].Name < growable[j].Name
	})
	byFd := map[string]*fdFill{}
	order := make([]*fdFill, 0, len(growable))
	for _, c := range growable {
		f := byFd[c.FDValue]
		if f == nil {
			f = &fdFill{fd: c.FDValue}
			byFd[c.FDValue] = f
			order = append(order, f)
		}
		ns := states[c.Node]
		capc := poolCap(&c, p)
		f.hosts = append(f.hosts, ns)
		f.grow = append(f.grow, c)
		f.cap += capc
		f.ceiling += capc
		if !freezeExisting {
			f.ceiling += ns.nodeHeadroom(p, cons, false) // headroom this FD could grow into
		}
	}
	// Smallest FD first (then FDValue) so the grow phase levels the smallest FDs up toward the target.
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].cap != order[j].cap {
			return order[i].cap < order[j].cap
		}
		return order[i].fd < order[j].fd
	})
	return order
}

// freshFdFills returns the genuinely-fresh failure domains for pool p — those not already represented in
// `existing` — as single fill slots (cap 0, ceiling = aggregate host headroom), in orderFreshFdGroups
// order (best-node headroom desc). Excluding the existing FDs keeps a fresh slot and an existing slot from
// ever describing the same FD (a TLC-only node's FD, fresh for QLC but already grown there).
func freshFdFills(p poolKind, existing []*fdFill, states map[string]*nodeState, poolNodeUsed map[string]struct{}, cons *CapacityConstraints) []*fdFill {
	existingFd := make(map[string]struct{}, len(existing))
	for _, f := range existing {
		existingFd[f.fd] = struct{}{}
	}
	var out []*fdFill
	for _, g := range orderFreshFdGroups(p, states, poolNodeUsed, cons) {
		fd := g.nodes[0].nc.FDValue
		if _, dup := existingFd[fd]; dup {
			continue
		}
		out = append(out, &fdFill{fd: fd, ceiling: g.headroom, hosts: g.nodes})
	}
	return out
}

// waterFill is the UNIFIED drive-pool water-fill: one pass over the COMBINED existing + fresh failure-domain
// set that selects the FD count, levels every chosen FD toward one common per-FD target, redistributes any
// ceiling-forced overflow across all FDs with headroom, and places each FD's increment across its hosts
// (growing existing containers / creating new ones). It subsumes the former selectFdTarget +
// growExistingToTarget + distributeFreshEven trio.
//
// exactNewFds: -1 == auto (select N via the even rule below); >= 0 == pin the fresh-FD count exactly
// (explicit driveContainers — existing FDs always all participate, plus exactly exactNewFds fresh FDs).
// growFor may be nil only when there are no existing FDs (the fresh-only balancedFresh policy). `remaining`
// is the GiB still to place (delta for a grow, desiredRaw for a fresh-only set). Returns the GiB it could
// not place; the caller's poolFeasibility flags any genuine shortfall.
func waterFill(
	p poolKind,
	desiredRaw, minFd, exactNewFds int,
	existing, fresh []*fdFill,
	cons *CapacityConstraints,
	growFor func(ExistingContainer) *ContainerGrowth,
	newByNode map[string]*NewContainer,
	newFor func(node, fd string) *NewContainer,
) int {
	if desiredRaw <= 0 {
		return 0
	}
	nExisting := len(existing)

	// reach sums the per-FD fill at target T over the existing FDs and the first nFresh fresh FDs: each FD
	// fills to min(T, ceiling) but never below its current cap (existing never shrink; fresh cap == 0).
	reach := func(nFresh, T int) int {
		sum := 0
		fill := func(cur, ceil int) {
			c := max(min(T, ceil), cur)
			sum += c
		}
		for _, f := range existing {
			fill(f.cap, f.ceiling)
		}
		for i := 0; i < nFresh && i < len(fresh); i++ {
			fill(0, fresh[i].ceiling)
		}
		return sum
	}
	// anyChosenBelow reports whether any chosen FD (existing or first nFresh fresh) has a ceiling strictly
	// below T — i.e. it would cap below the even share, the trigger for opening one more fresh FD.
	anyChosenBelow := func(nFresh, T int) bool {
		for _, f := range existing {
			if f.ceiling < T && f.ceiling > f.cap { // a growable existing FD that can't reach the share
				return true
			}
		}
		for i := 0; i < nFresh && i < len(fresh); i++ {
			if fresh[i].ceiling < T {
				return true
			}
		}
		return false
	}

	// --- Step 1: select the FD count N and the common per-FD target T. ---
	var nFresh int
	if exactNewFds >= 0 {
		nFresh = min(exactNewFds, len(fresh)) // pinned count (explicit driveContainers)
	} else {
		// Auto: smallest N >= max(minFd, nExisting) such that the chosen set, filled to min(T, ceiling)
		// (existing floored at cap), reaches desiredRaw — AND the unified EVEN rule: prefer opening another
		// fresh FD over capping a chosen FD below the even share T, but only while the next fresh FD can
		// hold the lowered share (its ceiling >= the lowered T). When fresh FDs run out, stop.
		startN := max(minFd, nExisting)
		startN = max(startN, 1)
		nFresh = min(startN-nExisting, len(fresh))
		for {
			n := nExisting + nFresh
			T := util.CeilDiv(desiredRaw, max(1, n))
			if nFresh >= len(fresh) {
				break // fresh pool exhausted — place what fits, poolFeasibility flags any shortfall
			}
			if reach(nFresh, T) < desiredRaw {
				nFresh++ // can't hold the capacity yet — must extend
				continue
			}
			// Capacity is satisfied. Open one more fresh FD only if it keeps the layout even: a chosen FD
			// currently caps below the share AND the next fresh FD can hold the (lowered) share.
			nextT := util.CeilDiv(desiredRaw, n+1)
			if anyChosenBelow(nFresh, T) && fresh[nFresh].ceiling >= nextT {
				nFresh++
				continue
			}
			break
		}
	}
	// --- Step 2 & 3: per-FD allocation by EVEN water-fill to a common LEVEL. Each chosen FD ends at
	// clamp(level, cap, ceiling): FDs below the level are raised toward it (so a smaller pre-existing FD
	// catches up first), FDs at/above their cap never shrink, and FDs that hit their ceiling hand their
	// deficit to the rest — symmetric forced overflow across existing AND fresh. The water level is the
	// largest L for which Σ clamp(L, cap, ceiling) <= desiredRaw; the residual (not absorbed at L because of
	// integer rounding) is then handed out round-robin to FDs still under ceiling. Smallest-cap FDs come
	// first in `chosen` (existing smallest-first, fresh after), so the residual lands on them first. ---
	chosen := make([]*fdFill, 0, nExisting+nFresh)
	chosen = append(chosen, existing...)
	chosen = append(chosen, fresh[:nFresh]...)

	// budget = the NEW capacity to place: the desired total minus what the chosen FDs already hold (existing
	// caps; fresh start at 0). want is simply desiredRaw. An empty chosen set leaves budget unplaced, which
	// poolFeasibility then reports.
	curTotal := 0
	for _, f := range chosen {
		curTotal += f.cap
	}
	budget := desiredRaw - curTotal
	want := desiredRaw

	// fillTo(L) = Σ clamp(L, cap, ceiling) over chosen FDs.
	fillTo := func(L int) int {
		sum := 0
		for _, f := range chosen {
			v := min(L, f.ceiling)
			if v < f.cap {
				v = f.cap
			}
			sum += v
		}
		return sum
	}
	// Binary-search the largest level L with fillTo(L) <= want. Upper bound = the largest ceiling.
	hiL := 0
	for _, f := range chosen {
		if f.ceiling > hiL {
			hiL = f.ceiling
		}
	}
	level := 0
	for lo, hi := 0, hiL; lo <= hi; {
		mid := (lo + hi) / 2
		if fillTo(mid) <= want {
			level = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	alloc := make([]int, len(chosen)) // target this-pool capacity per chosen FD (>= cap)
	for i, f := range chosen {
		alloc[i] = min(level, f.ceiling)
		if alloc[i] < f.cap {
			alloc[i] = f.cap
		}
	}
	// Hand out the residual (want - fillTo(level)) round-robin to FDs still below their ceiling, smallest
	// chosen first, so the totals stay as even as the ceilings allow without overshooting `want`.
	residual := want - fillTo(level)
	for residual > 0 {
		progressed := false
		for i, f := range chosen {
			if residual <= 0 {
				break
			}
			if alloc[i] >= f.ceiling {
				continue
			}
			alloc[i]++
			residual--
			progressed = true
		}
		if !progressed {
			break
		}
	}

	// --- Step 4: place each FD's increment across its hosts. Existing FDs grow their index-aligned
	// containers (no base memory); fresh FDs create new containers (base charged once per node, brand-new
	// floored at MinChunk). The per-FD increment is split across the FD's live hosts, preserving the
	// per-FD (not per-container) balance. Returns the GiB still unplaced. ---
	left := budget
	// existedNode: the node already carries SOME new container (any pool) from an earlier pass — its
	// per-container base memory is charged already, so a merged add here must not charge it again.
	existedNode := func(node string) bool { _, ok := newByNode[node]; return ok }
	// hasNew: the node already carries a THIS-POOL new container — a follow-up top-up to it may be below
	// MinChunk (the brand-new floor only applies to the first this-pool drive on the node).
	hasNew := func(node string) bool {
		nn, ok := newByNode[node]
		return ok && poolCapNew(nn, p) > 0
	}
	for i, f := range chosen {
		inc := alloc[i] - f.cap // this FD's planned increment
		if inc <= 0 {
			continue
		}
		if f.grow != nil {
			// Existing FD: grow its containers (smallest first; hosts are pre-ordered), capped at headroom.
			for hi, ns := range f.hosts {
				if inc <= 0 {
					break
				}
				room := ns.nodeHeadroom(p, cons, false)
				if room <= 0 {
					continue
				}
				add := min3(inc, left, room)
				if add <= 0 {
					continue
				}
				addPoolGrowth(growFor(f.grow[hi]), p, add)
				ns.consume(p, add, cons, false)
				inc -= add
				left -= add
			}
			continue
		}
		// Fresh FD: create new containers, splitting `inc` evenly across the FD's live hosts. A multi-host
		// FD receives the same per-FD capacity as a single-host one, just in more drives.
		fdRound := min(inc, left)
		for {
			live := 0
			for _, ns := range f.hosts {
				if ns.nodeHeadroom(p, cons, !existedNode(ns.nc.NodeName)) > 0 {
					live++
				}
			}
			if live == 0 || fdRound <= 0 {
				break
			}
			perNode := max(cons.MinChunkSizeGiB, util.CeilDiv(fdRound, live))
			progressed := false
			for _, ns := range f.hosts {
				if fdRound <= 0 {
					break
				}
				includeBase := !existedNode(ns.nc.NodeName)
				hr := ns.nodeHeadroom(p, cons, includeBase)
				if hr <= 0 {
					continue
				}
				minAdd := cons.MinChunkSizeGiB
				if hasNew(ns.nc.NodeName) {
					minAdd = 1 // top-up to an already-created this-pool container may be small
				}
				add := min3(min(perNode, fdRound), left, hr)
				if add < minAdd {
					continue
				}
				addPoolNew(newFor(ns.nc.NodeName, ns.nc.FDValue), p, add)
				ns.consume(p, add, cons, includeBase)
				fdRound -= add
				left -= add
				progressed = true
			}
			if !progressed {
				break
			}
		}
	}

	// Fold a sub-MinChunk tail into the highest-headroom node that already carries a new this-pool
	// container, instead of opening another FD. Only for the auto path (explicit driveContainers pins the
	// FD set exactly). Mirrors the former distributeFreshEven foldTail.
	if exactNewFds < 0 && left > 0 && left < cons.MinChunkSizeGiB {
		var best *nodeState
		for _, f := range chosen {
			if f.grow != nil {
				continue
			}
			for _, ns := range f.hosts {
				if !hasNew(ns.nc.NodeName) {
					continue
				}
				if best == nil || ns.nodeHeadroom(p, cons, false) > best.nodeHeadroom(p, cons, false) {
					best = ns
				}
			}
		}
		if best != nil && best.nodeHeadroom(p, cons, false) >= left {
			addPoolNew(newFor(best.nc.NodeName, best.nc.FDValue), p, left)
			best.consume(p, left, cons, false)
			left = 0
		}
	}
	return left
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

func min3(a, b, c int) int { return min(min(a, b), c) }

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
