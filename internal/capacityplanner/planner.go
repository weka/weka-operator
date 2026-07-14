package capacityplanner

import (
	"fmt"
	"sort"
	"strings"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

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
// during rebuild. The same flag drives the allow_1_parity weka override at formation. The flag is
// passed in explicitly (CapacityConstraints.AllowSingleParity) so this package stays free of the
// globalconfig singleton; the allocator keeps a no-arg shim that reads it from config.
func MinProtectionFloor(allowSingleParity bool) (stripeWidth, redundancyLevel, hotSpare int) {
	if allowSingleParity {
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
	// AllowInPlaceGrowth permits extending or converting EXISTING drive/compute containers in place.
	// When false (enableDynamicDriveScalingForSharedDrives=false) the planner neither GROWS an existing
	// container nor CONVERTS one to a new type (e.g. adding TLC to a QLC-only container to make it
	// mixed): fresh placement excludes every node already hosting any drive container (freshExclusion),
	// so new capacity may only land on EMPTY nodes as brand-new containers, and the pool is reported
	// infeasible if no empty node can host it. This mirrors the container-level NeedsDrivesToAllocate()
	// gate, which when the flag is off blanket-refuses dynamic drive allocation for a drive-sharing
	// container — so no in-place expansion or cross-pool conversion happens on any path.
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
	// AllowSingleParity relaxes the protection floor to single-parity 2+1+0 (see MinProtectionFloor).
	// QA/test only. Sourced from the operator-level flag by the caller so this package stays free of
	// the globalconfig singleton.
	AllowSingleParity bool
	// CpuPolicy is the cluster's target cpuPolicy for FRESH containers (all roles inherit the single
	// cluster.Spec.CpuPolicy — see container_factory.go), used (with each node's IsHt/FullPcpusOnly) to
	// project their physical CPU reservation when charging node CPU headroom — see cpuModel in cpu.go.
	// Empty is treated as CpuPolicyAuto (the operator default), which resolves to dedicated_ht on HT
	// nodes (a data core then costs 2 physical CPUs).
	CpuPolicy weka.CpuPolicy
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
	// OverProvisions carries per-pool over-provision advisories: the pool was realized with uniformly-sized
	// failure domains (whether by adding new FDs, growing existing ones, or both), and ceiling that uniform
	// size lands slightly above the pool's desiredRaw (within MaxOverProvisionFraction). Each message states
	// which placement happened (grown existing vs. new FDs). Emitted under their own
	// ClusterCapacityOverProvisioned (Normal) event reason, separate from the growth Warnings and shrink
	// advisories above.
	OverProvisions []string
	Infeasible     string
	// Infeasibility is the structured form of Infeasible: the binding cause, per-node rejection
	// breakdown and ordered fix tips. nil when the plan is feasible; when set, Infeasibility.Reason is
	// byte-identical to Infeasible. Populated via setInfeasible at every failing site.
	Infeasibility *InfeasibilityReport
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

// RequiredDriveResources returns the hugepages (MiB) and memory (MiB) a drive container needs to host the
// given per-pool capacity, using the same per-core model the cluster planner uses. The container
// controller calls this before adding virtual drives so the pod-level feasibility gate agrees with the
// cluster-level node-fit gate.
func RequiredDriveResources(tlcGiB, qlcGiB int, cons *CapacityConstraints) (hugepagesMiB, memoryMiB int) {
	cores := recomputeCores(tlcGiB, qlcGiB, cons)
	hugepagesMiB = cores * cons.driveHugepagesPerCoreMiB()
	memoryMiB = ComputeMemoryFootprintMiB(cores, cons)
	return hugepagesMiB, memoryMiB
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
	nc      NodeCapacity
	tlcFree int
	qlcFree int
	// coresFree is the remaining PHYSICAL CPU on the node (seeded from NodeCapacity.AllocatableCPU). Data
	// cores are converted to physical CPU when charged via cpuCost / dataCoresFit — a data core
	// costs 2 physical CPUs on an HT node under dedicated_ht, plus 1 per container. See cpu.go.
	coresFree    int
	hugepagesMiB int
	memoryMiB    int
	// hasDeletingDriveContainer mirrors NodeCapacity.HasDeletingDriveContainer: the node still hosts a
	// this-cluster drive container being deleted. Used only to deprioritize the node in
	// orderFreshFdGroups so a replacement FD prefers a node with no deleting container.
	hasDeletingDriveContainer bool
}

// topo returns the node's CPU topology for the cpu.go conversion helpers.
func (ns *nodeState) topo() NodeCPUTopology {
	return NodeCPUTopology{IsHt: ns.nc.IsHt, FullPcpusOnly: ns.nc.FullPcpusOnly}
}

// cpuCost returns the PHYSICAL CPU a container of dataCores weka cores reserves on this node under the
// given cpuPolicy (cons.CpuPolicy). includeBase adds the per-container management core (charged once per
// NEW container, mirroring the memory base).
func (ns *nodeState) cpuCost(policy weka.CpuPolicy, dataCores int, includeBase bool) int {
	perCore, base := cpuModel(policy, ns.topo())
	c := perCore * dataCores
	if includeBase {
		c += base
	}
	return c
}

// dataCoresFit returns how many DATA cores still fit in the node's physical CPU headroom under the given
// role cpuPolicy. includeBase reserves the per-container management core (for a NEW container).
func (ns *nodeState) dataCoresFit(policy weka.CpuPolicy, includeBase bool) int {
	return ns.dataCoresCapacity(policy, 0, includeBase)
}

// dataCoresCapacity returns how many DATA cores a single container could hold on this node if it also
// reclaimed extraCPU physical CPU already charged against coresFree by a container it will keep hosting
// (a frozen/grown existing compute). extraCPU=0 reduces to dataCoresFit — plain remaining headroom.
// includeBase reserves the per-container management core (for a NEW container).
func (ns *nodeState) dataCoresCapacity(policy weka.CpuPolicy, extraCPU int, includeBase bool) int {
	perCore, base := cpuModel(policy, ns.topo())
	avail := ns.coresFree + extraCPU
	if includeBase {
		avail -= base
	}
	if avail < 0 || perCore <= 0 {
		return 0
	}
	return avail / perCore
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
	// coresFree is physical CPU; convert it to the drive DATA cores that fit (accounting for the HT
	// multiplier and per-container base) before turning that into pool capacity.
	if coreCap := ns.dataCoresFit(cons.CpuPolicy, includeBase) * perCap; coreCap < headroom {
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
	ns.coresFree -= ns.cpuCost(cons.CpuPolicy, cores, includeBase)
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
	ns.coresFree += ns.cpuCost(cons.CpuPolicy, cores, includeBase)
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
	// The surplus is extra DRIVE data cores on top of an already-placed container, so charge its physical
	// CPU without the per-container base (already charged when the container was consumed).
	cpuNeed := ns.cpuCost(cons.CpuPolicy, cores, false)
	if ns.coresFree < cpuNeed ||
		(hpPerCore > 0 && ns.hugepagesMiB < cores*hpPerCore) ||
		(cons.MemoryPerCoreMiB > 0 && ns.memoryMiB < cores*cons.MemoryPerCoreMiB) {
		return false
	}
	ns.coresFree -= cpuNeed
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

	minSW, minRL, minHS := MinProtectionFloor(cons.AllowSingleParity)
	if scheme.StripeWidth < minSW || scheme.RedundancyLevel < minRL || scheme.HotSpare < minHS {
		setInfeasible(&plan, &InfeasibilityReport{
			Reason: fmt.Sprintf("clusterCapacity requires stripeWidth>=%d, redundancyLevel>=%d, hotSpare>=%d (got sw=%d rl=%d hs=%d)",
				minSW, minRL, minHS, scheme.StripeWidth, scheme.RedundancyLevel, scheme.HotSpare),
			Binding: "protection",
			Fixes:   fixesProtection(minSW, minRL, minHS),
		})
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
			setInfeasible(&plan, &InfeasibilityReport{Reason: reason, Binding: "driveContainers", Fixes: fixesDriveContainers(0)})
			return plan
		}
	}

	// Working per-node headroom, sorted deterministically by FD then node.
	states := make(map[string]*nodeState, len(inventory))
	for _, nc := range inventory {
		states[nc.NodeName] = &nodeState{
			nc:                        nc,
			tlcFree:                   nc.TlcGiB,
			qlcFree:                   nc.QlcGiB,
			coresFree:                 nc.AllocatableCPU, // physical CPU remaining
			hugepagesMiB:              nc.AvailableHugepagesMiB,
			memoryMiB:                 nc.AvailableMemoryMiB,
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

	// Plan the more spatially-CONSTRAINED pool first (fewer nodes can physically host its drive type), so
	// the other, more flexible pool can then co-locate onto the same nodes as a mixed (TLC+QLC) container
	// via the fresh-placement co-location bias. Co-location only works in this direction: the constrained
	// pool cannot bend onto nodes lacking its drive type, but the flexible pool can bend toward it. A tie
	// (or only one pool requested) keeps TLC first for determinism.
	type poolPlan struct {
		p        poolKind
		desired  int
		targetFd int
	}
	pools := []poolPlan{
		{poolTLC, desired.TlcRawGiB, tlcTargetFds},
		{poolQLC, desired.QlcRawGiB, qlcTargetFds},
	}
	if desired.TlcRawGiB > 0 && desired.QlcRawGiB > 0 &&
		countPoolCapableNodes(states, poolQLC) < countPoolCapableNodes(states, poolTLC) {
		pools[0], pools[1] = pools[1], pools[0]
	}
	for _, pp := range pools {
		planPool(pp.p, pp.desired, minFd, pp.targetFd, existingDrives, states, cons, growth, newByNode, &plan)
	}

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
			setInfeasible(&plan, &InfeasibilityReport{Reason: reason, Binding: "driveCores", Fixes: fixesDriveCores(recomputeCores(g.NewTlcGiB, g.NewQlcGiB, cons))})
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
			setInfeasible(&plan, &InfeasibilityReport{Reason: reason, Binding: "driveCores", Fixes: fixesDriveCores(derived)})
			return plan
		}
		// When driveCores is pinned ABOVE the capacity-derived count, the extra cores (and their
		// hugepages/memory) must still fit the node — placement only reserved the derived amount. Charge
		// and verify the surplus so an over-pinned container fails fast instead of landing unschedulable.
		if ns := states[node]; ns != nil && !ns.reserveCores(cores-derived, cons) {
			setInfeasible(&plan, &InfeasibilityReport{
				Reason: fmt.Sprintf(
					"node %s cannot host driveCores=%d for its %d GiB drive container (insufficient cores/hugepages/memory for the pinned core count)",
					node, cores, n.TlcGiB+n.QlcGiB),
				Binding: "cores",
				Fixes:   []string{fmt.Sprintf("lower driveCores (<=%d), or free cores/hugepages/memory on node %s", derived, node)},
			})
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
			setInfeasible(&plan, &InfeasibilityReport{
				Reason: fmt.Sprintf(
					"driveContainers=%d cannot be honored: the plan resolves to %d drive containers across the available failure domains",
					desired.DriveContainers, got),
				Binding: "driveContainers",
				Fixes:   fixesDriveContainers(got),
			})
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
		setInfeasible(plan, &InfeasibilityReport{Reason: "internal: compute node set not provided", Pool: "compute"})
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

	// A node already hosting an existing compute keeps hosting one (frozen or grown in place), so its
	// container-hosting CAPACITY is its residual free CPU PLUS the physical CPU that compute occupies —
	// which the inventory already netted out of coresFree at build time. Reclaim it per node so the
	// capacity below reflects the node's full container size, not the sliver left after the existing pod.
	// Without this, an occupied node's small residual drags hmin down in deriveComputeLayout and inflates
	// the container count, recreating computes on fresh nodes across passes (OP-348).
	existingComputeCPU := make(map[string]int, len(existingCompute))
	for i := range existingCompute {
		ec := &existingCompute[i]
		if ec.Node == "" {
			continue
		}
		if ns := states[ec.Node]; ns != nil {
			existingComputeCPU[ec.Node] += ns.cpuCost(cons.CpuPolicy, ec.NumCores, true)
		}
	}

	// Per-node compute-core headroom (cores left after drives). `nodes` already excludes any compute
	// node without headroom info (states[node] == nil), so each entry maps to a real per-node budget.
	// coresFree is physical CPU; deriveComputeLayout reasons in compute DATA cores, so convert per node,
	// adding back any existing compute's footprint (see existingComputeCPU above).
	coreHeadroom := make([]int, len(nodes))
	for i, node := range nodes {
		if ns := states[node]; ns != nil {
			coreHeadroom[i] = ns.dataCoresCapacity(cons.CpuPolicy, existingComputeCPU[node], true)
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
		setInfeasible(plan, &InfeasibilityReport{Reason: "compute: " + infeasible, Pool: "compute", Fixes: fixesCompute()})
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

	// Freeze/grow the existing computes (mutates each grown node's reserved headroom in states). Whether
	// growth is allowed follows cons.AllowInPlaceGrowth, applied inside layOutExistingCompute.
	existing, pinned, existingCores := layOutExistingCompute(existingCompute, states, cores, perContainerHP, cons)

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
	fitNodes := orderFitNodesByFreshFD(nodes, states, pinned, coveredFDs, cores, perContainerHP, fdOf, cons)

	// Balanced fill: cover the shortfall with the fewest uniform-capped (≤ `cores`) new containers, each on
	// the next best-fitting free node, splitting the cores as evenly as possible (the first `rem` get one
	// extra). nNew ≤ shortfall and base+1 ≤ cores by construction, so no explicit per-container cap is
	// needed. A shortfall that no free node set can cover is the sole remaining compute infeasibility.
	nNew := 0
	if shortfall > 0 {
		nNew = util.CeilDiv(shortfall, cores)
	}
	if nNew > len(fitNodes) {
		setInfeasible(plan, &InfeasibilityReport{
			Reason: fmt.Sprintf(
				"compute: cannot place %d new compute container(s) to cover the %d-core shortfall — only %d free fitting compute node(s) (each holds up to %d cores + %d MiB hugepages)",
				nNew, shortfall, len(fitNodes), cores, perContainerHP),
			Pool:    "compute",
			Binding: "cores",
			Fixes:   fixesCompute(),
		})
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
		setInfeasible(plan, &InfeasibilityReport{Reason: "compute: " + reason, Pool: "compute", Binding: "failure domains", Fixes: fixesCompute()})
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
			cCPU := ns.cpuCost(cons.CpuPolicy, cCores, true) // physical CPU for a NEW compute container
			if ns.coresFree < cCPU || ns.hugepagesMiB < cHP {
				// A new container is ≤ the uniform footprint this node already passed, so this is not
				// expected; treat as infeasible rather than over-claim.
				setInfeasible(plan, &InfeasibilityReport{
					Reason: fmt.Sprintf(
						"compute: free compute node %s cannot host a %d-core compute container (%d physical CPU + %d MiB hugepages free)",
						node, cCores, ns.coresFree, ns.hugepagesMiB),
					Pool:    "compute",
					Binding: "hugepages",
					Fixes:   fixesCompute(),
				})
				return
			}
			ns.coresFree -= cCPU
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

// layOutExistingCompute resolves each existing compute with a resolved node against the uniform target
// (cores + perContainerHP). A container whose node has headroom for the growth delta is GROWN in place
// (and the delta is reserved in states so the balanced fill does not double-claim that node); one whose
// node lacks the headroom, or whose pod is still Pending (ec.Unscheduled), is FROZEN at its current size
// (no pod disruption) and counted as committed capacity — so a just-created-but-Pending compute is not
// recreated as a duplicate on a fresh node. Only a compute with no resolved node is skipped.
// The prerequisite (Step 1b in
// PlanCapacity) already charged each existing compute's CURRENT footprint against states, so
// states[node].coresFree/hugepagesMiB is the remaining headroom after it. Returns the laid-out containers
// (in input order), the set of pinned nodes, and the cores they contribute toward the count*cores target.
func layOutExistingCompute(
	existingCompute []ExistingComputeContainer,
	states map[string]*nodeState,
	cores, perContainerHP int,
	cons *CapacityConstraints,
) (existing []laidOut, pinned map[string]struct{}, existingCores int) {
	// With in-place growth disabled every existing compute is frozen at its current size (its deficit is
	// covered by new containers only).
	freezeExisting := !cons.AllowInPlaceGrowth
	pinned = make(map[string]struct{}, len(existingCompute))
	existing = make([]laidOut, 0, len(existingCompute))
	for i := range existingCompute {
		ec := &existingCompute[i]
		if ec.Node == "" {
			continue
		}
		ns := states[ec.Node]
		if ns == nil {
			continue
		}
		pinned[ec.Node] = struct{}{}
		coresDelta := cores - ec.NumCores
		hpDelta := perContainerHP - ec.HugepagesMiB
		// The existing compute's current footprint is already netted from coresFree at inventory build, so
		// the growth delta charges physical CPU WITHOUT the per-container base.
		coresDeltaCPU := ns.cpuCost(cons.CpuPolicy, coresDelta, false)
		if ec.Unscheduled || freezeExisting ||
			(coresDelta > 0 && ns.coresFree < coresDeltaCPU) ||
			(hpDelta > 0 && ns.hugepagesMiB < hpDelta) {
			// Frozen at the current size (no pod disruption): the pod is still Pending (ec.Unscheduled — no
			// pod to resize, so count it as committed capacity but never grow/recreate it), or in-place
			// growth is disabled (freezeExisting), or the node lacks headroom for the growth delta. The
			// shortfall it leaves is covered by the balanced fill. Its current footprint was already charged
			// against states (Step 1b of PlanCapacity), so this branch reserves nothing — no double-charge.
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
			ns.coresFree -= coresDeltaCPU
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
	cons *CapacityConstraints,
) []string {
	// coresFree is physical CPU; it is a monotonic best-fit sort key, so ordering by it is unchanged.
	headroomOf := func(node string) int { return states[node].coresFree }
	var freshFit, coveredFit []string
	for _, node := range nodes {
		if _, ok := pinned[node]; ok {
			continue // already carries an existing pinned compute (grown or frozen)
		}
		ns := states[node]
		if ns == nil || ns.coresFree < ns.cpuCost(cons.CpuPolicy, cores, true) || ns.hugepagesMiB < perContainerHP {
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
	shortfall := max(0, CapacityCoverTarget(desiredRaw, cons)-placed)
	if reason := poolFeasibility(p, minFd, shortfall, existingDrives, growth, newByNode, cons); reason != "" {
		// poolFeasibility reports a capacity shortfall when remaining>0, else an FD-count shortfall.
		binding, fixes := "drive capacity", fixesCapacity(p, cons.AllowInPlaceGrowth)
		if shortfall == 0 {
			binding, fixes = "failure domains", fixesFailureDomains(p, minFd)
		}
		setInfeasible(plan, &InfeasibilityReport{Reason: reason, Pool: p.tag(), Binding: binding, ShortfallGiB: shortfall, Fixes: fixes})
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
	// Co-location bias: prefer nodes that already carry a freshly-planned OTHER-pool container so both
	// pools land on the same node (a mixed drive container) when it can hold both, splitting only when no
	// co-located node can hold this pool's even share. Only the two-pool create case populates this;
	// otherwise preferNodes is empty and selection is the plain headroom-desc top-N.
	preferNodes := otherPoolPreferNodes(p, newByNode)
	candidates := orderFreshFdGroups(p, states, freshExclusion(existingDrives, p, cons), cons)
	chosen, T, ok := selectUniform(desiredRaw, minFd, candidates, preferNodes, cons)
	if !ok {
		if !isFallback {
			poolUsed := freshExclusion(existingDrives, p, cons)
			msg, binding, shortfall := uniformInfeasibleMsg(p, desiredRaw, minFd, candidates, states, poolUsed, cons)
			fixes := fixesCapacity(p, cons.AllowInPlaceGrowth)
			if binding == "failure domains" {
				fixes = fixesFailureDomains(p, minFd)
			}
			setInfeasible(plan, &InfeasibilityReport{
				Reason:        msg,
				Pool:          p.tag(),
				Binding:       binding,
				ShortfallGiB:  shortfall,
				RejectedNodes: rejectedNodes(p, states, poolUsed, cons),
				Fixes:         fixes,
			})
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
		setInfeasible(plan, &InfeasibilityReport{Reason: reason, Pool: p.tag(), Binding: "driveContainers", Fixes: fixesDriveContainers(0)})
		return
	}

	T := util.CeilDiv(desiredRaw, targetFds)

	// INVARIANT: never grow/convert an existing container in place unless dynamic drive scaling is enabled.
	// The pinned-driveContainers path reaches the uniform per-FD level T by growing existing below-T FDs;
	// if any existing pool-p FD is below T (would need growing) while AllowInPlaceGrowth is off, report
	// infeasible instead of growing — consistent with planPoolUniformIncrease and the fresh paths (which
	// freshExclusion already bars from touching occupied nodes when the flag is off).
	if !cons.AllowInPlaceGrowth {
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
		for fd, capGiB := range perFd {
			if capGiB < T {
				setInfeasible(plan, &InfeasibilityReport{
					Reason: fmt.Sprintf(
						"%s: driveContainers=%d pins %d GiB per failure domain, but failure domain %q holds only %d GiB and growing it in place is disabled (enableDynamicDriveScalingForSharedDrives=false) — enable it or unset driveContainers",
						p, targetFds, T, fd, capGiB),
					Pool:         p.tag(),
					Binding:      "driveContainers",
					ShortfallGiB: T - capGiB,
					Fixes:        []string{"enable enableDynamicDriveScalingForSharedDrives to grow the failure domain in place", "or unset driveContainers"},
				})
				return
			}
		}
	}

	// Assemble the exactly-targetFds chosen FDs: every existing pool-p FD (as a grow target) plus exactly
	// exactNewFds fresh FDs at the front of the headroom-desc candidate list. placeUniform grows the
	// existing FDs below T up to T and creates the fresh FDs at T. (When the flag is off, the guard above
	// has already ensured no existing FD is below T, so no growth occurs here.)
	chosen := existingFdsAsChosen(p, existingDrives, states, cons)
	fresh := orderFreshFdGroups(p, states, freshExclusion(existingDrives, p, cons), cons)
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
	// When in-place growth is off, freshExclusion bars EVERY occupied node (not just this pool's), so a
	// different-pool node can no longer be converted to mixed and only empty nodes remain as candidates.
	freshGroups := orderFreshFdGroups(p, states, freshExclusion(existingDrives, p, cons), cons)
	// Co-location bias (increase path): float FDs whose nodes already carry a freshly-planned other-pool
	// container to the front so takeFreshAtLevel draws them first — both pools land on the same node (a
	// mixed drive container) when it can hold this pool's share. Order-only; freshCountAtLeast is a count
	// and takeFreshAtLevel still filters by level, so an under-capacity co-located node is skipped and
	// placement falls back to a split. Empty preferNodes (this pool planned first) → no-op.
	freshGroups = colocatedFirst(freshGroups, otherPoolPreferNodes(p, newByNode))
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
	// overProvisionMsg describes an intentional overshoot: the pool is realized with uniformly-sized
	// failure domains, and ceiling that uniform size to a whole GiB (or to the smallest size the fresh
	// nodes support) lands slightly above the exact target. `grown` is how many EXISTING FDs were resized
	// in place and `kFresh` how many NEW FDs were added — the two placement paths read very differently, so
	// the message states which actually happened rather than assuming "new FDs, existing untouched".
	overProvisionMsg := func(grown, kFresh, level, total int) string {
		var placement string
		switch {
		case grown > 0 && kFresh > 0:
			placement = fmt.Sprintf("growing %d existing and adding %d new failure domain(s)", grown, kFresh)
		case grown > 0:
			placement = fmt.Sprintf("growing %d existing failure domain(s)", grown)
		default:
			placement = fmt.Sprintf("adding %d new failure domain(s)", kFresh)
		}
		return fmt.Sprintf(
			"%s: +%d GiB covered by %s, each sized to a uniform %d GiB; this over-provisions the target by %d GiB (within maxOverProvisionFraction=%.2f) — intentional rounding to keep failure domains uniformly sized, not reclaimable excess (no manual shrink needed)",
			p, delta, placement, level, total-desiredRaw, cons.MaxOverProvisionFraction)
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
	// the shortfall itself (CeilDiv(delta, k)) rather than cloning an existing FD size. Sizing to delta/k
	// makes the new FDs sum to ~delta, so the pool reaches desiredRaw without over-provisioning. Prefer the
	// FEWEST new containers: iterate the count k ASCENDING from kMin (the fewest FDs that keep per-FD <=
	// maxPerFdCap = desiredRaw/minFd) and place at the first feasible k, so a delta is covered by as few,
	// as-large FDs as possible instead of many small ones (deleting a few FDs recreates a few, not a fresh
	// swarm that ratchets the pool finer each time). Two bounds shape the search:
	//   - kMax = CeilDiv(delta, T0) caps the FD COUNT at what T0-cloning (the smallest existing FD) would
	//     use, so we never fragment into MORE FDs than that. Note this bounds the count, not the per-FD size:
	//     at k=kMax the per-FD can dip mildly below T0 (ceiling rounding) — that's intended, so a delta whose
	//     fresh nodes are individually a little smaller than T0 is still covered here rather than pushed to
	//     the grow phase. A delta that truly needs sub-T0 fragments beyond this count is left to grow/infeasible.
	//   - detectImbalance keeps a single fresh FD from dwarfing tiny existing FDs (it then tries more,
	//     smaller FDs). Node scarcity (freshCountAtLeast) likewise falls to more, smaller FDs.
	maxPerFdCap := 0
	if minFd > 0 {
		maxPerFdCap = desiredRaw / minFd
	}
	if maxPerFdCap > 0 {
		existingAvg := poolAvg(existingDrives, p)
		kMin := max(1, util.CeilDiv(delta, maxPerFdCap))       // fewest FDs (largest per-FD within the ceiling)
		kMax := min(util.CeilDiv(delta, T0), len(freshGroups)) // count cap: no more FDs than T0-cloning would use
		for k := kMin; k <= kMax; k++ {
			perFd := util.CeilDiv(delta, k)
			if perFd < cons.MinChunkSizeGiB {
				perFd = cons.MinChunkSizeGiB
			}
			if perFd > maxPerFdCap {
				continue // still above the per-FD ceiling — need more (smaller) FDs
			}
			if detectImbalance(perFd, existingAvg, cons) {
				continue // this size would dwarf the existing FDs — try more (smaller) FDs
			}
			if freshCountAtLeast(perFd) < k {
				continue // not enough spare nodes for k FDs this size; try more (smaller) FDs
			}
			total := current + k*perFd
			if total-desiredRaw > overshootCap {
				continue
			}
			placeUniform(p, perFd, freshChosen(k, perFd), existingDrives, states, cons, growFor, newByNode, newFor)
			finalizeFeasibility()
			if plan.Infeasible == "" && total > desiredRaw {
				// Step 4 even-split: k NEW fresh FDs cover the delta; existing FDs are untouched.
				plan.OverProvisions = append(plan.OverProvisions, overProvisionMsg(0, k, perFd, total))
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
		setInfeasible(plan, &InfeasibilityReport{
			Reason: fmt.Sprintf(
				"%s: cannot satisfy clusterCapacity (+%d GiB) at the uniform per-failure-domain size of %d GiB. Even after growing the %d existing failure domain(s) to their nodes' limits and adding failure domains on all %d candidate node(s) (nodes not already running a %s drive container, with enough free capacity/cores/hugepages/memory), the target is still out of reach. Add more nodes (or nodes with more free resources), or lower clusterCapacity.",
				p, delta, T0, numExisting, len(freshGroups), p),
			Pool:         p.tag(),
			Binding:      "drive capacity",
			ShortfallGiB: delta,
			Fixes:        fixesAddCapacity(p),
		})
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
			// L==T0 defensive create-at-T0: kFresh NEW FDs at T0; existing FDs are untouched.
			plan.OverProvisions = append(plan.OverProvisions, overProvisionMsg(0, kFresh, T0, best.total))
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
			setInfeasible(plan, &InfeasibilityReport{
				Reason: fmt.Sprintf(
					"%s: cannot satisfy clusterCapacity (+%d GiB). The %d existing failure domain(s) are frozen at %d GiB each and cannot grow because dynamic drive scaling for shared drives is disabled, so new capacity can only be added as more %d GiB failure domains — one per node not already running a %s drive container and with %d GiB of free capacity/cores/hugepages/memory. This needs %d such node(s) but only %d is/are available, so %d more node(s) are required. Either add %d more node(s), or enable enableDynamicDriveScalingForSharedDrives to grow the existing containers in place instead (aggregate free capacity elsewhere does not help — capacity on a node already hosting this pool's FD cannot be reused while growth is disabled). The maximum capacity a single failure domain may hold is %d GiB (clusterCapacity raw ÷ (stripeWidth+redundancy+hotSpare) = %d ÷ %d).",
					p, delta, numExisting, T0, T0, p, T0, kNeeded, kAvail, shortfall, shortfall, maxPerFdCap, desiredRaw, minFd),
				Pool:         p.tag(),
				Binding:      "failure domains",
				ShortfallGiB: delta,
				Fixes:        fixesGrowthDisabledFDs(shortfall),
			})
			return
		}
		// Enough candidate nodes exist, but covering the delta with only T0-sized FDs would over-provision
		// beyond maxOverProvisionFraction; the balanced plan therefore needs to grow existing FDs, which is
		// disabled. Either allow growth or align the request to a whole number of T0 chunks.
		setInfeasible(plan, &InfeasibilityReport{
			Reason: fmt.Sprintf(
				"%s: cannot satisfy clusterCapacity (+%d GiB) without growing the %d existing failure domain(s) beyond their current %d GiB each, but dynamic drive scaling for shared drives is disabled. Enable enableDynamicDriveScalingForSharedDrives, or set clusterCapacity to a value that the %d GiB failure-domain size divides evenly. The maximum capacity a single failure domain may hold is %d GiB (clusterCapacity raw ÷ (stripeWidth+redundancy+hotSpare) = %d ÷ %d).",
				p, delta, numExisting, T0, T0, maxPerFdCap, desiredRaw, minFd),
			Pool:         p.tag(),
			Binding:      "drive capacity",
			ShortfallGiB: delta,
			Fixes:        fixesGrowthDisabledOverProvision(T0),
		})
		return
	}
	if float64(best.L-T0) < cons.MinGrowthFraction*float64(T0) {
		// Grow is allowed but too small (below minGrowthFraction); the T0-clone framing (kNeeded T0-sized
		// FDs across kAvail spare nodes) explains the create-new alternative that also fell short.
		kNeeded := util.CeilDiv(delta, T0)
		kAvail := freshCountAtLeast(T0)
		pct := int((100*float64(best.L-T0))/float64(T0) + 0.5)
		setInfeasible(plan, &InfeasibilityReport{
			Reason: fmt.Sprintf(
				"%s: cannot satisfy clusterCapacity — need +%d GiB. Adding failure domains requires %d node(s) not already running a %s drive container with >=%d GiB free each (the uniform per-FD size), but only %d is/are available. The alternative — growing existing containers in place — would raise each by only %d%% (below minGrowthFraction=%.2f), so it is skipped. Resolve by: adding %d more node(s), or raising clusterCapacity by at least one %d GiB failure-domain chunk, or lowering minGrowthFraction.",
				p, delta, kNeeded, p, T0, kAvail, pct, cons.MinGrowthFraction, kNeeded-kAvail, T0),
			Pool:         p.tag(),
			Binding:      "failure domains",
			ShortfallGiB: delta,
			Fixes:        fixesGrowTooSmall(kNeeded-kAvail, T0, cons.MinGrowthFraction),
		})
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
		// Grow path: the numExisting existing FDs are grown in place to best.L, plus kFresh NEW FDs at best.L.
		plan.OverProvisions = append(plan.OverProvisions, overProvisionMsg(numExisting, kFresh, best.L, best.total))
	}
}

// selectUniform free-selects the best uniform (N, T) for a greenfield pool: the smallest N >= minFd such
// that the N highest-headroom candidate FDs each have aggregate headroom >= T = ceil(desiredRaw/N). It
// grows N (which lowers T) until either the top-N candidates all clear T (returns them + T) or candidates
// run out (ok=false -> caller reports infeasible). candidates are headroom-desc (orderFreshFdGroups), so
// the front N are always the highest-headroom N FDs.
// preferNodes (may be nil) are nodes already carrying a freshly-planned OTHER-pool container. When both
// pools need fresh FDs, selection biases toward co-locating pool p onto those nodes (a mixed drive
// container) so both pools share a node when it can still hold both — see pickPreferringColocated.
func selectUniform(desiredRaw, minFd int, candidates []*fdGroup, preferNodes map[string]struct{}, cons *CapacityConstraints) (chosen []*fdGroup, target int, ok bool) {
	for N := max(minFd, 1); N <= len(candidates); N++ {
		target = max(cons.MinChunkSizeGiB, util.CeilDiv(desiredRaw, N))
		fits := true
		for i := 0; i < N; i++ {
			if candidates[i].headroom < target {
				fits = false
				break
			}
		}
		if !fits {
			continue
		}
		// N and target are fixed by the headroom-desc fit above and stay unchanged (FD count and per-FD
		// size are identical to a pure top-N pick). Only WHICH N failure domains get filled flips toward
		// co-located ones, so both pools land on the same node whenever it can still hold its even share.
		return pickPreferringColocated(candidates, N, target, preferNodes), target, true
	}
	return nil, 0, false
}

// pickPreferringColocated selects N failure domains (each with aggregate headroom >= target) from the
// headroom-desc candidate list, taking CO-LOCATED FDs first (any member node in preferNodes) then the
// rest, preserving headroom-desc order within each tier. selectUniform's fit check guarantees at least N
// candidates clear target, and only such candidates are picked, so exactly N are returned and each still
// holds its even share. When preferNodes is empty, or no co-located FD clears target (e.g. disjoint
// TLC-only/QLC-only nodes), this reduces to the plain headroom-desc top-N — i.e. a split.
func pickPreferringColocated(candidates []*fdGroup, n, target int, preferNodes map[string]struct{}) []*fdGroup {
	colocated := func(g *fdGroup) bool {
		for _, ns := range g.nodes {
			if _, ok := preferNodes[ns.nc.NodeName]; ok {
				return true
			}
		}
		return false
	}
	out := make([]*fdGroup, 0, n)
	for _, wantColocated := range []bool{true, false} {
		for _, g := range candidates {
			if len(out) >= n {
				return out
			}
			if g.headroom >= target && colocated(g) == wantColocated {
				out = append(out, g)
			}
		}
	}
	return out
}

// uniformInfeasibleMsg explains why no uniform tiling fits: the smallest usable FD caps below the per-FD
// share. It reports the per-FD share at the largest feasible N (the most forgiving tiling) and the smallest
// candidate FD headroom that falls short.
func uniformInfeasibleMsg(p poolKind, desiredRaw, minFd int, candidates []*fdGroup, states map[string]*nodeState, poolUsed map[string]struct{}, cons *CapacityConstraints) (msg, binding string, shortfallGiB int) {
	if len(candidates) < minFd {
		msg = fmt.Sprintf(
			"%s: only %d of %d required failure domains have capacity (need at least stripeWidth+redundancyLevel+hotSpare)",
			p, len(candidates), minFd)
		if breakdown := rejectedNodesBreakdown(p, states, poolUsed, cons); breakdown != "" {
			msg += " — " + breakdown
		}
		return msg, "failure domains", 0
	}
	// At the largest N (all candidates) the per-FD share is smallest; the smallest candidate still caps below
	// it, so no N can tile uniformly.
	N := len(candidates)
	T := max(cons.MinChunkSizeGiB, util.CeilDiv(desiredRaw, N))
	smallest := candidates[N-1].headroom
	msg = fmt.Sprintf(
		"%s: cannot place %d GiB uniformly across %d failure domains — the smallest usable FD holds %d GiB, below the %d GiB per-FD share; add capacity or lower clusterCapacity",
		p, desiredRaw, N, smallest, T)
	return msg, "drive capacity", max(0, T-smallest)
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

	type reasonGroup struct {
		nodes []string // member names (sorted, capped at maxNamesPerReason)
		total int      // total nodes with this reason (may exceed len(nodes))
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

	// rejectedNodes() sorts by name and applies the same candidate classification; format each
	// structured rejection into its human reason bucket (behavior-identical to the former inline loop).
	for _, rj := range rejectedNodes(p, states, poolUsed, cons) {
		switch {
		case strings.HasPrefix(rj.Binding, "already hosts"):
			add(rj.Node, rj.Binding)
		case rj.Binding == "drive capacity" && rj.FreeGiB == 0:
			add(rj.Node, fmt.Sprintf("no %s drive capacity", p))
		default:
			add(rj.Node, fmt.Sprintf("%s limits usable %s to %d GiB (below the %d GiB minimum chunk)", rj.Binding, p, rj.FreeGiB, rj.NeededGiB))
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

// allDriveNodes is the set of nodes hosting ANY existing drive container (any pool). Used to bar fresh
// placement from every occupied node when in-place growth is disabled (see freshExclusion).
func allDriveNodes(existingDrives []ExistingContainer) map[string]struct{} {
	used := map[string]struct{}{}
	for i := range existingDrives {
		c := &existingDrives[i]
		if c.Node == "" {
			continue
		}
		if c.TlcGiB > 0 || c.QlcGiB > 0 {
			used[c.Node] = struct{}{}
		}
	}
	return used
}

// freshExclusion returns the node set that fresh (new-container) placement must avoid.
// Normally only nodes already hosting THIS pool are excluded (a different-pool node can be converted
// to mixed via placeUniform's grow path). But when in-place growth is disabled
// (enableDynamicDriveScalingForSharedDrives=false) we must not grow OR convert any existing
// container, so exclude every node hosting any drive container — new capacity may land only on
// empty nodes; if none, the pool is reported infeasible.
func freshExclusion(existingDrives []ExistingContainer, p poolKind, cons *CapacityConstraints) map[string]struct{} {
	if cons.AllowInPlaceGrowth {
		return poolNodeUsed(existingDrives, p)
	}
	return allDriveNodes(existingDrives)
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
	// colocated is set by colocatedFirst when a member node already carries the OTHER pool's pending
	// container: placing this pool there yields a mixed container. takeFreshAtLevel treats it as the PRIMARY
	// preference (above the not-deleting tier) so a co-location target is chosen even if its node still hosts
	// a terminating container — the just-freed mixed node is exactly where both pools should co-locate.
	colocated bool
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
	// Co-location is the PRIMARY key, not-deleting the SECONDARY: a co-located FD (its node carries the
	// other pool's pending container) is preferred even when its node still hosts a terminating container,
	// because that just-freed mixed node is exactly where both pools should land as one mixed container.
	// Tier order: colocated+notDeleting, colocated+deleting, notColocated+notDeleting, notColocated+deleting.
	for _, wantColocated := range []bool{true, false} {
		for _, wantDeleting := range []bool{false, true} {
			for _, g := range fresh {
				if len(out) >= k {
					return out
				}
				if g.headroom >= level && g.colocated == wantColocated && g.hasDeletingDriveContainer == wantDeleting {
					out = append(out, g)
				}
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

// otherPool returns the pool that is NOT p (there are exactly two).
func otherPool(p poolKind) poolKind {
	if p == poolTLC {
		return poolQLC
	}
	return poolTLC
}

// otherPoolPreferNodes is the set of nodes whose pending NEW container (placed earlier in THIS plan, by
// the constrained pool planned first) already carries the OTHER pool's capacity and NOT pool p. Placing
// pool p on such a node yields a single FRESH mixed (TLC+QLC) drive container, so fresh placement biases
// toward them (pickPreferringColocated on the greenfield path, colocatedFirst on the increase path).
//
// It deliberately does NOT bias toward EXISTING single-pool containers: co-locating there would mean
// adding the other pool to an already-running container (an in-place conversion/grow), which is not
// wanted — co-location is only ever realized by creating one fresh mixed container on an EMPTY node when
// both pools are short in the same plan. Empty when the other pool has not been placed yet → no-op.
func otherPoolPreferNodes(p poolKind, newByNode map[string]*NewContainer) map[string]struct{} {
	other := otherPool(p)
	out := map[string]struct{}{}
	for node, n := range newByNode {
		if poolCapNew(n, other) > 0 && poolCapNew(n, p) == 0 {
			out[node] = struct{}{}
		}
	}
	return out
}

// colocatedFirst stable-partitions fresh FD groups so those with any member node in preferNodes come
// first (co-location candidates), preserving input order within each partition. Used on the uniform-
// increase fresh candidate list so takeFreshAtLevel draws co-located FDs first (still level-filtered, so
// an under-capacity co-located node is skipped and placement falls back to a split). No-op when
// preferNodes is empty.
func colocatedFirst(groups []*fdGroup, preferNodes map[string]struct{}) []*fdGroup {
	if len(preferNodes) == 0 {
		return groups
	}
	isColocated := func(g *fdGroup) bool {
		for _, ns := range g.nodes {
			if _, ok := preferNodes[ns.nc.NodeName]; ok {
				return true
			}
		}
		return false
	}
	front := make([]*fdGroup, 0, len(groups))
	back := make([]*fdGroup, 0, len(groups))
	for _, g := range groups {
		if isColocated(g) {
			g.colocated = true // primary tier in takeFreshAtLevel (above not-deleting)
			front = append(front, g)
		} else {
			back = append(back, g)
		}
	}
	return append(front, back...)
}

// countPoolCapableNodes counts inventory nodes that can PHYSICALLY host pool p (have any of its drive
// type). It measures spatial constraint, not current free space, so PlanCapacity can plan the more
// constrained pool first and let the flexible pool co-locate onto it.
func countPoolCapableNodes(states map[string]*nodeState, p poolKind) int {
	n := 0
	for _, ns := range states {
		capacity := ns.nc.TlcGiB
		if p == poolQLC {
			capacity = ns.nc.QlcGiB
		}
		if capacity > 0 {
			n++
		}
	}
	return n
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
