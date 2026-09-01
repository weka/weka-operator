package capacityplanner

import (
	"fmt"
	"math"
	"sort"
	"strings"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/pkg/util"
)

// Pure clusterCapacity planner (no k8s client): given a desired per-pool target, protection scheme,
// existing drive containers and per-node headroom, returns the containers to GROW/CREATE so capacity
// spreads evenly across at least minFdNum failure domains. TLC and QLC are planned as INDEPENDENT pools;
// same-node results are merged into mixed containers.

// ProtectionScheme is the cluster's data+parity+hot-spare layout.
type ProtectionScheme struct {
	StripeWidth     int
	RedundancyLevel int
	HotSpare        int
}

// MinFdNum is the minimum number of failure domains drive capacity must spread across.
func (p ProtectionScheme) MinFdNum() int { return p.StripeWidth + p.RedundancyLevel + p.HotSpare }

// MinProtectionFloor is the minimum accepted stripeWidth/redundancyLevel/hotSpare: 3+2+0 in production,
// or 2+1+0 under AllowSingleParity — a single parity chunk leaves a stripe unprotected during rebuild,
// so that floor must stay QA/test only.
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
	// ComputeContainers / ComputeCores: explicit spec sizing (0 == auto-derive from TLC drive cores).
	ComputeContainers int
	ComputeCores      int
	// DriveContainers / DriveCores: explicit spec sizing (0 == auto-derive); when set, honored exactly
	// and infeasible rather than silently ignored. DriveContainers is the exact total container count;
	// DriveCores is the fixed per-container core count.
	DriveContainers int
	DriveCores      int
}

// CapacityConstraints bundles the immutable sizing knobs. Per-core capacity caps come from config; the
// ≤N-virtual-drives-per-core limit is enforced later, at drive-allocation time in the container allocator.
type CapacityConstraints struct {
	TlcCapacityPerCoreGiB int
	QlcCapacityPerCoreGiB int
	MinChunkSizeGiB       int
	// ImbalanceFactor gates the heterogeneous "balanced fresh" growth fallback (detectImbalance): when a
	// fresh per-FD chunk would be >= ImbalanceFactor × the existing per-FD average, abandon the dwarfed
	// existing FDs for a fresh uniform layout instead. <= 0 disables the fallback.
	ImbalanceFactor float64
	// Drive-pod resource coefficients (MiB), mirroring resources/pod.go, for the per-node headroom gate.
	HugepagesPerCoreMiB int
	MemoryBaseMiB       int
	MemoryPerCoreMiB    int
	// DPDK base hugepages added per core; the node-fit gate must include it or it under-reserves and the
	// scheduler rejects co-located pools (Insufficient hugepages-2Mi). 0 keeps pure config defaults.
	DriveDpdkPerCoreMiB   int
	ComputeDpdkPerCoreMiB int
	// MaxCoresPerContainer caps per-container cores for both drive and compute containers in both
	// planners (0 disables; real per-node headroom still binds). Hugepages ratios/cap mirror
	// ComputeCapacityBasedHugepages so the planner agrees with the container controller's sizing.
	MaxCoresPerContainer     int
	ComputeHugepagesTlcRatio int
	ComputeHugepagesQlcRatio int
	ComputeMaxHugepagesMiB   int
	// MinComputeContainers is the form-cluster floor (5 default, 3 under ALLOW_SINGLE_PARITY), distinct
	// from the protection-scheme FD floor. Only auto-full-drives consults it (clusterCapacity's scheme floor
	// already exceeds it); 0 disables it. Populated from config via ConstraintsForClusterSpec.
	MinComputeContainers int
	// Compute:drive core ratios: requiredComputeCores = max(totalDriveCores, ceil(tlcRatio*tlcCores +
	// qlcRatio*qlcCores)) — total drive cores is a HARD 1:1 floor no ratio can undercut. The
	// drive-sharing pair applies to clusterCapacity/containerCapacity; FullDrives... to auto-full-drives.
	ComputeToTlcDriveCoreRatio        float64
	ComputeToQlcDriveCoreRatio        float64
	FullDrivesComputeToDriveCoreRatio float64
	// AllowInPlaceGrowth permits extending/converting EXISTING containers in place. When false, fresh
	// placement excludes every node already hosting any drive container (new capacity only lands on
	// empty nodes); mirrors the container-level NeedsDrivesToAllocate() gate.
	AllowInPlaceGrowth bool
	// MinGrowthFraction: minimum relative per-container grow (target-cur)/cur to grow in place, else
	// skipped. 0 is treated as the 0.2 default at use sites.
	MinGrowthFraction float64
	// MaxOverProvisionFraction is the max fraction a pool's create-new may overshoot desiredRaw.
	MaxOverProvisionFraction float64
	// CapacityDeadbandFraction is the relative shortfall (desired-current)/desired below which pool
	// growth is ignored (see CapacityShort). 0 disables the deadband (strict current < desired).
	CapacityDeadbandFraction float64
	// AllowSingleParity relaxes the protection floor to 2+1+0 (see MinProtectionFloor). QA/test only.
	AllowSingleParity bool
	// CpuPolicy is the target cpuPolicy for FRESH containers, combined with each node's IsHt/
	// FullPcpusOnly to project physical CPU (see cpuModel in cpu.go). Empty resolves to dedicated_ht on
	// HT nodes, where a data core costs 2 physical CPUs.
	CpuPolicy weka.CpuPolicy
}

// ExistingContainer is the planner's view of one of THIS cluster's healthy drive containers.
// TlcGiB/QlcGiB come from the SPEC, not realized allocation, so an in-flight drive-add isn't re-grown.
type ExistingContainer struct {
	Name        string
	Node        string // GetNodeAffinity(); "" when unknown
	FDValue     string
	TlcGiB      int
	QlcGiB      int
	NumCores    int
	Unscheduled bool // pod not yet scheduled — counted as committed capacity but not grown
	// NumDrives: full-drive count, meaningful only for auto-full-drives containers (0 for
	// clusterCapacity/shared-drives). PlanAutoFullDrives diffs it against live count for expand-only growth.
	NumDrives int
}

// ExistingComputeContainer is the planner's view of one of THIS cluster's healthy compute containers;
// only node pin and resource footprint matter (no drive capacity or failure domain).
type ExistingComputeContainer struct {
	Name         string
	Node         string // GetNodeAffinity(); "" when unknown
	NumCores     int
	HugepagesMiB int
	Unscheduled  bool
}

// ContainerGrowth is an existing container to edit in place (capacity only ever increases).
type ContainerGrowth struct {
	Name      string `json:"name"`
	NewTlcGiB int    `json:"newTlcGiB"`
	NewQlcGiB int    `json:"newQlcGiB"`
	NewCores  int    `json:"newCores"`
	// NewNumDrives: new full-drive count for an auto-full-drives container; only set by PlanAutoFullDrives.
	NewNumDrives int `json:"newNumDrives"`
}

// NewContainer is a drive container to create, pinned to Node, in failure domain FDValue.
type NewContainer struct {
	Node     string                `json:"node"`
	FDValue  string                `json:"fdValue"`
	TlcGiB   int                   `json:"tlcGiB"`
	QlcGiB   int                   `json:"qlcGiB"`
	NumCores int                   `json:"numCores"`
	Ratio    *weka.DriveTypesRatio `json:"ratio"`
	Type     string                `json:"type"` // tlc / qlc / mixed
	// NumDrives: full-drive count; only set by PlanAutoFullDrives (0 for shared-drive containers).
	NumDrives int `json:"numDrives"`
}

// WarningKind classifies a planner warning by cause, mapping to a distinct Kubernetes event reason so an
// operator can filter on `reason=` instead of grepping message prose (see each const below).
type WarningKind string

const (
	// WarningKindDrivesStranded: a pinned dynamicTemplate.numDrives leaves signed full drives unused. This
	// is the remaining cause — a node that cannot fit its drives is now a plan-wide infeasibility, not
	// a warning — so it describes an operator choice and maps to a Normal event.
	WarningKindDrivesStranded WarningKind = "DrivesStranded"
	// WarningKindTransient: a condition that clears on its own (e.g. a container still being deleted).
	WarningKindTransient WarningKind = "Transient"
	// WarningKindComputeLayout: compute-sizing advisory, shared by both planners.
	WarningKindComputeLayout WarningKind = "ComputeLayout"
	// WarningKindNodeIneligible: a node with signed full drives and no drive container of its own is cordoned,
	// not ready, or carries an untolerated taint, so it is withheld from Create rather than treated as a plan
	// failure. Maps to a Normal event for the cordoned reason, Warning otherwise (spans both operator
	// actions and conditions outside the operator's control).
	WarningKindNodeIneligible WarningKind = "NodeIneligible"
)

// WarningCause further subdivides a WarningKind so the controller's per-reason event throttle can key on
// more than the reason alone: two Warnings of the same Kind but different Cause get independent throttle
// windows, so one cannot silently suppress the other. Empty is legal — a Kind with exactly one cause today
// (DrivesStranded, ComputeLayout) carries "", which reproduces the old reason-only key for it.
type WarningCause string

const (
	// NodeIneligible has no constants here: it aggregates every ineligible node into one Warning, and its
	// Cause is the sorted, "+"-joined set of the distinct resources.NodeIneligibleReason values actually
	// present, used verbatim. A reason added there therefore becomes its own cause with no change here, and
	// a node going NotReady is never masked by one already cordoned.
	//
	// CausePlacementUnscheduled / CausePlacementDriveDeleting / CausePlacementComputeDeleting are the three
	// PlacementDeferred causes, each rendered as its own Warning instead of merged into one.
	CausePlacementUnscheduled     WarningCause = "unscheduled-pod"
	CausePlacementDriveDeleting   WarningCause = "drive-container-deleting"
	CausePlacementComputeDeleting WarningCause = "compute-container-deleting"
)

// Warning is one classified planner advisory. Every auto-full-drives warning is fleet-wide: a condition
// that can hit several nodes in one pass is reported once, naming every affected node in Message.
type Warning struct {
	Kind    WarningKind
	Cause   WarningCause
	Message string
}

// WarningMessages flattens warnings to their human-readable text, for renderers (the weka-capacity CLI) and
// summaries that only ever showed the prose.
func WarningMessages(warnings []Warning) []string {
	if len(warnings) == 0 {
		return nil
	}
	out := make([]string, 0, len(warnings))
	for _, w := range warnings {
		out = append(out, w.Message)
	}
	return out
}

// fleetWarning builds a classified warning (throttled per reason, not per node).
func fleetWarning(kind WarningKind, format string, args ...any) Warning {
	return fleetWarningWithCause(kind, "", format, args...)
}

// fleetWarningWithCause is fleetWarning plus a Cause, for a Kind whose Warnings need their own throttle key
// per cause rather than sharing the one key the bare Kind/reason gives them.
func fleetWarningWithCause(kind WarningKind, cause WarningCause, format string, args ...any) Warning {
	return Warning{Kind: kind, Cause: cause, Message: fmt.Sprintf(format, args...)}
}

// CapacityPlan is the planner output.
type CapacityPlan struct {
	Grow               []ContainerGrowth
	Create             []NewContainer
	TotalTlcDriveCores int
	// TotalQlcDriveCores: always 0 in auto-full-drives mode (TLC-only by construction).
	TotalQlcDriveCores int
	// RequiredComputeCores: compute-core total this plan must supply. See RequiredComputeCores.
	RequiredComputeCores int
	// ComputeContainers / ComputeCores: node-core-aware compute sizing derived from TLC drive cores
	// (1:1) bounded by per-node headroom. Zero when not in clusterCapacity mode.
	ComputeContainers int
	ComputeCores      int
	// ComputeNodes are the specific nodes reserved for compute, len == ComputeContainers, so callers
	// don't pin compute onto a drive-pinned node lacking post-drive hugepages for both.
	ComputeNodes []string
	// ComputeLayout is the PER-CONTAINER compute layout. HETEROGENEOUS when an existing pinned compute
	// can't grow to the uniform target: it's FROZEN at its current size and the deficit covered by extra
	// containers elsewhere. Downstream MUST prefer this over the uniform fields when non-empty.
	ComputeLayout []ComputeContainerSpec
	// Warnings are advisories CLASSIFIED by Kind so the controller maps each to its own Kubernetes event
	// reason (bare strings under one reason made distinct conditions unfilterable/unalertable).
	Warnings     []Warning
	ShrinkEvents []string
	// OverProvisions: per-pool advisories that a uniform failure-domain size lands slightly above
	// desiredRaw (within MaxOverProvisionFraction). Emitted separately from Warnings.
	OverProvisions []string
	Infeasible     string
	// Infeasibility is the structured form of Infeasible; nil when feasible.
	Infeasibility *InfeasibilityReport
	// DriveSizing is the auto-full-drives planner's sizing rationale; nil for clusterCapacity.
	DriveSizing *DriveSizingRationale
}

// DriveSizingRationale is the auto-full-drives planner's accounting for what it claimed and the compute
// that implies, populated by PlanAutoFullDrives every call. Drive cores follow directly from each node's
// drive count and pins and are never traded down to fit compute (see autofulldrives.go), so there is no
// search outcome to explain.
type DriveSizingRationale struct {
	Reason string `json:"reason"`
	// DrivesTaken/Available and TlcGiBTaken/Available: totals across every drive-having node this pass.
	DrivesTaken     int `json:"drivesTaken"`
	DrivesAvailable int `json:"drivesAvailable"`
	TlcGiBTaken     int `json:"tlcGiBTaken"`
	TlcGiBAvailable int `json:"tlcGiBAvailable"`

	TotalTlcDriveCores int `json:"totalTlcDriveCores"`
	TotalQlcDriveCores int `json:"totalQlcDriveCores"` // always 0 for auto-full-drives; kept for symmetry
	// RequiredComputeCores: the compute-core total this plan must supply (full-drives ratio applied to
	// TotalTlcDriveCores). Nothing in the planner reduces it — a fleet that cannot supply it is infeasible.
	RequiredComputeCores     int `json:"requiredComputeCores"`
	ComputeContainers        int `json:"computeContainers"`
	ComputeCoresPerContainer int `json:"computeCoresPerContainer"`
	ComputeHugepagesMiB      int `json:"computeHugepagesMiB"`
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
	// coresFree is remaining PHYSICAL CPU (seeded from AllocatableCPU); data cores convert to physical
	// CPU via cpuCost/dataCoresFit (e.g. 2 physical CPUs/core on HT under dedicated_ht). See cpu.go.
	coresFree    int
	hugepagesMiB int
	memoryMiB    int
	// hasDeletingDriveContainer mirrors NodeCapacity.HasDeletingDriveContainer; only deprioritizes the
	// node in orderFreshFdGroups.
	hasDeletingDriveContainer bool
}

// topo returns the node's CPU topology for the cpu.go conversion helpers.
func (ns *nodeState) topo() NodeCPUTopology {
	return NodeCPUTopology{IsHt: ns.nc.IsHt, FullPcpusOnly: ns.nc.FullPcpusOnly}
}

// cpuCost returns the PHYSICAL CPU a container of dataCores reserves under cpuPolicy. includeBase adds
// the per-container management core (once per NEW container).
func (ns *nodeState) cpuCost(policy weka.CpuPolicy, dataCores int, includeBase bool) int {
	return cpuCostShared(ns.topo(), policy, dataCores, includeBase)
}

// cpuCostShared is the physical-CPU-cost formula shared by nodeState.cpuCost and autofulldrives.go's
// physicalCPUCost — kept identical so the two never drift apart.
func cpuCostShared(topo NodeCPUTopology, policy weka.CpuPolicy, dataCores int, includeBase bool) int {
	perCore, base := cpuModel(policy, topo)
	c := perCore * dataCores
	if includeBase {
		c += base
	}
	return c
}

// dataCoresFit returns how many DATA cores still fit in remaining physical CPU under policy.
func (ns *nodeState) dataCoresFit(policy weka.CpuPolicy, includeBase bool) int {
	return ns.dataCoresCapacity(policy, 0, includeBase)
}

// dataCoresCapacity is dataCoresFit plus extraCPU physical CPU reclaimed from a container the caller
// will keep hosting (a frozen/grown existing compute); extraCPU=0 reduces to plain headroom.
func (ns *nodeState) dataCoresCapacity(policy weka.CpuPolicy, extraCPU int, includeBase bool) int {
	return dataCoresCapacityShared(ns.topo(), policy, ns.coresFree, extraCPU, includeBase)
}

// dataCoresCapacityShared is shared by nodeState.dataCoresCapacity and autofulldrives.go's
// physicalCPUToDataCores (same split as cpuCostShared).
func dataCoresCapacityShared(topo NodeCPUTopology, policy weka.CpuPolicy, coresFree, extraCPU int, includeBase bool) int {
	perCore, base := cpuModel(policy, topo)
	avail := coresFree + extraCPU
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

// nodeHeadroom returns the max capacity of pool p the node can still host: the min of its drive, core,
// hugepages and memory budgets converted to pool capacity. includeBase reserves per-container base
// memory (true for a NEW container).
func (ns *nodeState) nodeHeadroom(p poolKind, cons *CapacityConstraints, includeBase bool) int {
	h, _ := ns.nodeHeadroomBinding(p, cons, includeBase)
	return h
}

// nodeHeadroomBinding is nodeHeadroom plus the binding (tightest) dimension name, used to explain why a
// node was rejected as an FD candidate.
func (ns *nodeState) nodeHeadroomBinding(p poolKind, cons *CapacityConstraints, includeBase bool) (headroom int, binding string) {
	perCap := perCoreCap(p, cons)
	if perCap <= 0 {
		return 0, "pool disabled"
	}
	headroom, binding = ns.poolFree(p), "drive capacity"
	// coresFree is physical CPU; convert to drive DATA cores before comparing as pool capacity.
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

// unconsume reverses a consume of gGiB of pool p (rolls back a fresh-FD placement that couldn't reach
// the uniform level); must mirror consume exactly.
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
	// No per-container base — already charged when the container was consumed.
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

	// Explicit driveContainers (0 == auto): exact total FD count split TLC/QLC by raw-capacity ratio; a
	// share below minFd fails fast before placing anything.
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

	// Inventory headroom is already net of every weka container (all clusters, all modes, compute
	// included) charged once at inventory build time, so drive FDs already steer away from
	// compute-saturated nodes. This cluster's own compute is re-validated/grown in planCompute, charging
	// only its growth delta.

	// Accumulators merged across pools.
	growth := map[string]*ContainerGrowth{} // by container name
	newByNode := map[string]*NewContainer{} // by node name

	// Plan the more spatially-constrained pool first (fewer capable nodes) so the flexible pool can
	// co-locate onto the same nodes as a mixed container — co-location only works in that direction. A
	// tie keeps TLC first for determinism.
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
		countPoolCapableNodes(states, poolNodeUsed(existingDrives, poolQLC), poolQLC) <
			countPoolCapableNodes(states, poolNodeUsed(existingDrives, poolTLC), poolTLC) {
		pools[0], pools[1] = pools[1], pools[0]
	}
	for _, pp := range pools {
		planPool(pp.p, pp.desired, minFd, pp.targetFd, existingDrives, states, cons, growth, newByNode, &plan)
	}

	// pinCores returns the per-container core count and the capacity-derived count it's based on. driveCores
	// (0 == auto) pins a fixed core count; a container needing more fails fast. cons.MaxCoresPerContainer is
	// a hard per-container limit enforced on both the derived and pinned count, so a target needing more
	// cores than one container may hold must spread across more containers; `fixes` accompanies any reason.
	pinCores := func(tlcGiB, qlcGiB int) (cores, derived int, reason string, fixes []string) {
		derived = RequiredDriveCores(tlcGiB, qlcGiB, cons)
		if cons.MaxCoresPerContainer > 0 && derived > cons.MaxCoresPerContainer {
			reason = fmt.Sprintf(
				"a drive container of %d GiB (TLC %d + QLC %d) needs %d cores, above the %d-core per-container limit: "+
					"raise driveContainers so each container holds less capacity, or lower clusterCapacity",
				tlcGiB+qlcGiB, tlcGiB, qlcGiB, derived, cons.MaxCoresPerContainer)
			return 0, derived, reason, fixesMaxCoresPerContainer(cons.MaxCoresPerContainer)
		}
		if desired.DriveCores <= 0 {
			return derived, derived, "", nil
		}
		if cons.MaxCoresPerContainer > 0 && desired.DriveCores > cons.MaxCoresPerContainer {
			reason = fmt.Sprintf("driveCores=%d is above the %d-core per-container limit",
				desired.DriveCores, cons.MaxCoresPerContainer)
			return 0, derived, reason, fixesMaxCoresPerContainer(cons.MaxCoresPerContainer)
		}
		if desired.DriveCores < derived {
			reason = fmt.Sprintf(
				"driveCores=%d is too small for a drive container of %d GiB (TLC %d + QLC %d): it needs %d cores",
				desired.DriveCores, tlcGiB+qlcGiB, tlcGiB, qlcGiB, derived)
			return 0, derived, reason, fixesDriveCores(derived)
		}
		return desired.DriveCores, derived, "", nil
	}

	// Emit growth (only where capacity actually increased).
	growNames := make([]string, 0, len(growth))
	for name := range growth {
		growNames = append(growNames, name)
	}
	sort.Strings(growNames)
	for _, name := range growNames {
		g := growth[name]
		cores, _, reason, fixes := pinCores(g.NewTlcGiB, g.NewQlcGiB)
		if reason != "" {
			setInfeasible(&plan, &InfeasibilityReport{Reason: reason, Binding: "driveCores", Fixes: fixes})
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
		cores, derived, reason, fixes := pinCores(n.TlcGiB, n.QlcGiB)
		if reason != "" {
			setInfeasible(&plan, &InfeasibilityReport{Reason: reason, Binding: "driveCores", Fixes: fixes})
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
	plan.TotalQlcDriveCores = totalQlcDriveCores(existingDrives, growth, newByNode, cons)
	// fullDrives=false: PlanCapacity is the drive-sharing planner, so the per-pool TLC/QLC ratios apply.
	plan.RequiredComputeCores = RequiredComputeCores(plan.TotalTlcDriveCores, plan.TotalQlcDriveCores, false, cons)

	// Size compute from the post-drive per-node headroom. Skipped when drives are already infeasible.
	if plan.Infeasible == "" {
		planCompute(desired, scheme, existingCompute, computeNodes, states, cons, &plan)
	}
	return plan
}

// planCompute sizes clusterCapacity's compute containers from the POST-drive per-node headroom (this plan's
// own drive placement/growth already netted out). Pods spread one-per-node across computeNodes, which may
// include diskless nodes and may overlap drive nodes (shared nodes draw from the post-drive remainder).
// Infeasibility here fails the whole plan before any drive container is created or grown.
func planCompute(
	desired DesiredCapacity,
	scheme ProtectionScheme,
	existingCompute []ExistingComputeContainer,
	computeNodes map[string]bool,
	states map[string]*nodeState,
	cons *CapacityConstraints,
	plan *CapacityPlan,
) {
	// nil computeNodes is a caller bug, not an empty set — fail loudly instead of sizing over nothing.
	if computeNodes == nil {
		setInfeasible(plan, &InfeasibilityReport{Reason: "internal: compute node set not provided", Pool: "compute"})
		return
	}

	nodes := make([]string, 0, len(computeNodes))
	for node, eligible := range computeNodes {
		if eligible && states[node] != nil {
			nodes = append(nodes, node)
		}
	}
	sort.Strings(nodes)

	// A node already hosting compute keeps hosting one; reclaim its existing CPU footprint (already netted
	// out of coresFree at inventory time) so capacity reflects the full container size, not the residual
	// sliver — else hmin in deriveComputeLayout gets dragged down and computes get recreated on fresh nodes
	// across passes (OP-348). hasExistingCompute also exempts these nodes from the hugepages gate below.
	existingComputeCPU := make(map[string]int, len(existingCompute))
	hasExistingCompute := make(map[string]bool, len(existingCompute))
	for i := range existingCompute {
		ec := &existingCompute[i]
		if ec.Node == "" {
			continue
		}
		if ns := states[ec.Node]; ns != nil {
			existingComputeCPU[ec.Node] += ns.cpuCost(cons.CpuPolicy, ec.NumCores, true)
			hasExistingCompute[ec.Node] = true
		}
	}

	// Post-drive per-node headroom converted to compute data cores (deriveComputeLayout's unit), with any
	// existing compute's footprint added back.
	coreHeadroom := make([]int, len(nodes))
	for i, node := range nodes {
		if ns := states[node]; ns != nil {
			coreHeadroom[i] = ns.dataCoresCapacity(cons.CpuPolicy, existingComputeCPU[node], true)
		}
	}

	// Per-node free hugepages for deriveComputeLayout's aggregate gate. Nodes with existing compute are
	// exempted (MaxInt): freezing them in place is always safe since hugepagesMiB is already net of their
	// current charge; a genuine deficit still surfaces later via the "cannot place" message.
	nodeHugepagesMiB := make([]int, len(nodes))
	for i, node := range nodes {
		if hasExistingCompute[node] {
			nodeHugepagesMiB[i] = math.MaxInt
			continue
		}
		if ns := states[node]; ns != nil {
			nodeHugepagesMiB[i] = ns.hugepagesMiB
		}
	}

	// floor/minComputeFds: SW+RL+HS, one above Weka's strict SW+RL minimum, leaving headroom to
	// delete/recreate one compute pod in place without dropping below Weka's minimum.
	floor := scheme.MinFdNum()
	minComputeFds := scheme.MinFdNum()
	// hugepagesFor mirrors the allocator's cost function so the count/cores decision isn't hugepages-blind:
	// a layout that core-fits but wouldn't fit hugepages prefers more, smaller containers (or fails fast)
	// instead of surfacing the mismatch later as an allocation failure.
	hugepagesFor := func(count, cores int) int {
		return ComputeContainerHugepagesMiB(desired.TlcRawGiB, desired.QlcRawGiB, count, cores, cons)
	}
	count, cores, infeasible, binding, warnings := deriveComputeLayout(
		desired.ComputeContainers, desired.ComputeCores, plan.RequiredComputeCores,
		floor, cons.MaxCoresPerContainer, coreHeadroom, nodeHugepagesMiB, hugepagesFor,
	)
	for _, w := range warnings {
		plan.Warnings = append(plan.Warnings, Warning{Kind: WarningKindComputeLayout, Message: w})
	}
	if infeasible != "" {
		// ShortfallGiB stays 0: the deficit here is in cores or MiB-hugepages, never GiB, and converting
		// either into GiB would invent a number this report never measured.
		setInfeasible(plan, &InfeasibilityReport{Reason: "compute: " + infeasible, Pool: "compute", Binding: binding, Fixes: fixesCompute()})
		return
	}

	// Hugepages needed by a `cores`-core compute container, at the `count`-container split.
	perContainerHP := ComputeContainerHugepagesMiB(desired.TlcRawGiB, desired.QlcRawGiB, count, cores, cons)

	// Existing pinned computes that can reach the uniform target are GROWN in place; ones without headroom
	// are FROZEN. Whatever's left toward count*cores (the shortfall) is placed as new balanced containers.
	fdOf := func(node string) string { return states[node].nc.FDValue }

	existing, pinned, existingCores := layOutExistingCompute(existingCompute, states, cores, perContainerHP, cons)

	shortfall := max(count*cores-existingCores, 0)

	// FDs already covered by a pinned existing container; fit-node ordering below steers toward fresh FDs.
	coveredFDs := map[string]struct{}{}
	for _, lo := range existing {
		coveredFDs[fdOf(lo.spec.Node)] = struct{}{}
	}

	// Free fitting nodes ordered to maximize distinct-FD coverage first (in AUTO mode this is plain
	// cores-desc best-fit; see orderFitNodesByFreshFD).
	fitNodes := orderFitNodesByFreshFD(nodes, states, pinned, coveredFDs, cores, perContainerHP, fdOf, cons)

	// Cover the shortfall with the fewest uniform-capped (<= cores) new containers, split as evenly as
	// possible; nNew <= shortfall and base+1 <= cores by construction.
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

	// The layout must span >= minComputeFds FDs; extend nNew one fresh-FD node at a time until met (never
	// past len(fitNodes)). computeFDFeasibility fails fast if even all fit nodes fall short.
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
	// Floor the split at one core per new container: slight over-provisioning beats a 0-core container.
	splitCores := max(shortfall, nNew)

	// Fixes the capacity-based hugepages share (clusterMiB/totalCount) for every non-frozen container.
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
			cHP := ComputeContainerHugepagesMiB(desired.TlcRawGiB, desired.QlcRawGiB, totalCount, cCores, cons)
			cCPU := ns.cpuCost(cons.CpuPolicy, cCores, true)
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

	// Re-derive non-frozen containers' hugepages at the final totalCount; frozen ones keep theirs.
	layout := make([]ComputeContainerSpec, 0, totalCount)
	for _, lo := range existing {
		spec := lo.spec
		if !lo.frozen {
			spec.HugepagesMiB = ComputeContainerHugepagesMiB(desired.TlcRawGiB, desired.QlcRawGiB, totalCount, spec.NumCores, cons)
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

// laidOut is one existing compute container resolved against the uniform target: grown or frozen in place.
type laidOut struct {
	spec   ComputeContainerSpec
	frozen bool // kept at current size — hugepages must not be re-derived
}

// layOutExistingCompute resolves each existing compute against the uniform target (cores + perContainerHP):
// GROWN in place if its node has headroom for the delta (reserved in states to avoid double-claiming);
// FROZEN at current size if not, or still Pending (avoids recreating a just-created Pending as a duplicate).
// A compute with no resolved node is skipped. Returns laid-out containers, pinned nodes, and count*cores.
func layOutExistingCompute(
	existingCompute []ExistingComputeContainer,
	states map[string]*nodeState,
	cores, perContainerHP int,
	cons *CapacityConstraints,
) (existing []laidOut, pinned map[string]struct{}, existingCores int) {
	// With in-place growth disabled every existing compute is frozen; new containers cover the deficit.
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
			// Frozen: still Pending, growth disabled, or node lacks the delta headroom. Shortfall is
			// covered by the balanced fill; footprint already charged, so nothing to reserve here.
			existing = append(existing, laidOut{
				spec:   ComputeContainerSpec{Node: ec.Node, NumCores: ec.NumCores, HugepagesMiB: ec.HugepagesMiB},
				frozen: true,
			})
			existingCores += ec.NumCores
			continue
		}
		// Delta fits: reserve it (so the balanced fill doesn't double-claim this node) and grow in place.
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

// orderFitNodesByFreshFD returns free fitting compute nodes ordered so a prefix maximizes distinct-FD
// coverage — plain best-fit-by-cores can pile picks onto a few high-headroom FDs, leaving compute on too
// few failure domains for Weka to initialize. Fresh-FD nodes come first, then covered-FD nodes, each
// FD-spread by orderNodesByFDSpread. In AUTO mode (FD == node) this is the plain cores-desc best-fit sort.
func orderFitNodesByFreshFD(
	nodes []string,
	states map[string]*nodeState,
	pinned, coveredFDs map[string]struct{},
	cores, perContainerHP int,
	fdOf func(node string) string,
	cons *CapacityConstraints,
) []string {
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
		if _, covered := coveredFDs[fdOf(node)]; covered {
			coveredFit = append(coveredFit, node)
		} else {
			freshFit = append(freshFit, node)
		}
	}
	return append(orderNodesByFDSpread(freshFit, headroomOf, fdOf), orderNodesByFDSpread(coveredFit, headroomOf, fdOf)...)
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

// poolAvg returns the average per-container pool-p capacity (0 when none) — the detectImbalance baseline.
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

// detectImbalance gates the heterogeneous fallback (planPoolFreshUniform): true when a fresh per-FD chunk
// (newPerFD) would dwarf the existing per-FD average (newPerFD >= ImbalanceFactor × existingAvg), meaning
// growing the existing tiny FDs uniformly is either infeasible or gates the pool's usable capacity — so lay
// a fresh uniform set instead and flag the small FDs deletable. False with no baseline or factor <= 0.
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
	if desiredRaw <= 0 {
		return
	}

	// growFor/newFor create-or-fetch the mutable records for the existing-grow and new-container paths.
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

	// Heterogeneous-growth fallback (see detectImbalance): abandons rather than grows the tiny existing
	// FDs, so NOT gated on AllowInPlaceGrowth. Falls through to uniform-increase when no fresh set fits.
	if targetFds == 0 &&
		detectImbalance(growChunk(delta, minFd, cons), poolAvg(existingDrives, p), cons) {
		if planPoolFreshUniform(p, desiredRaw, minFd, existingDrives, states, cons, growFor, growth, newByNode, newFor, plan, true /*isFallback*/) {
			return
		}
	}

	// Explicit driveContainers: place exactly targetFds FDs at T=ceil(desiredRaw/targetFds) via
	// placeUniform (grow existing below-T FDs, create the rest), then check feasibility.
	if targetFds > 0 {
		planPoolExplicit(p, desiredRaw, minFd, targetFds, existingDrives, states, cons, growth, growFor, newByNode, newFor, plan)
		return
	}

	// Uniform-FD increase: pool already has FDs with a well-defined per-FD chunk T — replicate it.
	if delta > 0 && poolExistingFds(p, existingDrives) > 0 {
		planPoolUniformIncrease(p, desiredRaw, minFd, current, existingDrives, states, cons, growFor, growth, newByNode, newFor, plan)
		return
	}

	// Greenfield: no FD for this pool yet. Free-select the best uniform (N, T); cross-pool conversion
	// happens via placeUniform's grow path when a chosen FD already hosts an other-pool container.
	planPoolFreshUniform(p, desiredRaw, minFd, existingDrives, states, cons, growFor, growth, newByNode, newFor, plan, false /*isFallback*/)
}

// finalizePoolFeasibility verifies placement realized desiredRaw for pool p with >= minFd FDs, setting
// plan.Infeasible on a shortfall (placeUniform may roll back an FD whose hosts can't hold their even
// share). excludePoolPExisting (greenfield/fresh-only paths) counts only fresh placements toward coverage,
// since existing pool-p FDs are being abandoned or don't exist.
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

// planPoolFreshUniform lays a fresh, internally-uniform FD set for pool p across nodes not already hosting
// pool p (a node with an other-pool container is still a candidate — placeUniform converts it to mixed).
// isFallback distinguishes greenfield (no tiling found means Infeasible) from the heterogeneous fallback
// (abandons the dwarfed existing FDs, flagged deletable). Returns true when a fresh set was placed.
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
	// Co-location bias: prefer nodes already carrying a freshly-planned OTHER-pool container (mixed drive
	// container), splitting only when no co-located node can hold this pool's even share.
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
	placeUniform(p, T, chosen, existingDrives, cons, growFor, newByNode, newFor)
	finalizePoolFeasibility(p, desiredRaw, minFd, existingDrives, growth, newByNode, cons, plan, true)
	if isFallback && plan.Infeasible == "" {
		// Every chosen FD reached T (finalize passed), so len(chosen) is the fresh FD count for the advisory.
		plan.Warnings = append(plan.Warnings, fleetWarning(WarningKindComputeLayout,
			"%s capacity grew heterogeneously: created a fresh balanced set of ~%d GiB across %d failure domain(s). "+
				"The older, smaller drive containers can be deleted manually once data has migrated.",
			p, T, len(chosen)))
	}
	return true
}

// planPoolExplicit places exactly targetFds FDs for pool p (pinned driveContainers) at the uniform
// per-FD chunk T = ceil(desiredRaw/targetFds): placeUniform grows existing below-T FDs to T and creates
// the rest fresh at T. resolveExactNewFds runs first for its fail-fast guards.
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

	// INVARIANT: never grow an existing FD in place with AllowInPlaceGrowth off — report infeasible
	// instead, consistent with planPoolUniformIncrease and the fresh paths.
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

	// Assemble the exactly-targetFds chosen set: every existing pool-p FD plus exactNewFds fresh FDs.
	chosen := existingFdsAsChosen(p, existingDrives, states, cons)
	fresh := orderFreshFdGroups(p, states, freshExclusion(existingDrives, p, cons), cons)
	chosen = append(chosen, takeFreshAtLevel(fresh, exactNewFds, T)...)

	placeUniform(p, T, chosen, existingDrives, cons, growFor, newByNode, newFor)

	// Existing pool-p FDs are grown in place, so they count toward coverage (excludePoolPExisting=false).
	finalizePoolFeasibility(p, desiredRaw, minFd, existingDrives, growth, newByNode, cons, plan, false)
}

// planPoolUniformIncrease prefers creating whole new FDs at the existing uniform per-FD chunk T over
// editing existing specs, capped at MaxOverProvisionFraction. If create-new can't cover the delta, it
// raises T to Lmin, growing every below-Lmin existing FD to Lmin and placing fresh FDs at Lmin — only if
// the relative grow clears MinGrowthFraction and in-place growth is allowed; otherwise infeasible.
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

	// perFd/numExisting/T0 don't filter unscheduled containers while reach/existingReach/existingFdsAsChosen
	// do; that's safe because a capacity-bearing unscheduled drive container can never reach this function
	// (planClusterCapacity defers upstream; see firstUnscheduledDriveContainer).
	// T0: the uniform per-FD chunk to replicate = max(MinChunk, smallest existing per-FD capacity sum).
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
	maxFdCap := 0
	for _, v := range perFd {
		if minFdCap == 0 || v < minFdCap {
			minFdCap = v
		}
		if v > maxFdCap {
			maxFdCap = v
		}
	}
	T0 := max(cons.MinChunkSizeGiB, minFdCap)
	// Tmax: the largest existing per-FD level. A replacement <= Tmax matches a size the cluster already
	// has, so it must never be treated as "imbalanced" — lets a deleted high-tier FD be recreated at that
	// size instead of fragmenting when smaller (deletable) FDs are still dragging poolAvg down.
	Tmax := max(cons.MinChunkSizeGiB, maxFdCap)

	// Fresh candidate FDs (not hosting pool p, headroom >= MinChunk), best-headroom first. With in-place
	// growth off, freshExclusion bars every occupied node, so no different-pool node can convert to mixed.
	freshGroups := orderFreshFdGroups(p, states, freshExclusion(existingDrives, p, cons), cons)
	// Co-location bias: float FDs already carrying a freshly-planned other-pool container to the front
	// (order-only; an under-capacity co-located node still falls back to a split via the level filter).
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

	// existingReach sums per-FD pool capacity reachable at level L over THIS POOL's existing FDs (cap>0): an
	// anchor already >= L contributes its full cap, a growable FD (ceiling >= L) contributes L, an FD that
	// can't reach L makes level L infeasible. Cap-0 FDs (other-pool containers) are SKIPPED here — counted
	// as FRESH candidates instead and CONVERTED to mixed by placeUniform, avoiding double-count.
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
	// overProvisionMsg describes an intentional overshoot from rounding the uniform per-FD size up.
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

	// freshChosen returns the first k fresh candidate FDs able to host `level`.
	freshChosen := func(k, level int) []*fdGroup {
		return takeFreshAtLevel(freshGroups, k, level)
	}

	// finalizeFeasibility verifies what placeUniform ACTUALLY placed (it may roll back an FD whose hosts
	// can't hold their even share). Existing pool-p FDs are grown in place, so they count toward coverage.
	finalizeFeasibility := func() {
		finalizePoolFeasibility(p, desiredRaw, minFd, existingDrives, growth, newByNode, cons, plan, false)
	}

	// Preferred: cover delta with k new FDs sized to CeilDiv(delta, k). Iterate k ascending from kMin
	// (fewest FDs within maxPerFdCap) so as few, as-large FDs as possible are used. kMax caps the count at
	// what T0-cloning would use. detectImbalance/freshCountAtLeast push k up rather than failing outright.
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
			if perFd > Tmax && detectImbalance(perFd, existingAvg, cons) {
				continue // dwarfs even the LARGEST existing FD — try more (smaller) FDs
			}
			if freshCountAtLeast(perFd) < k {
				continue // not enough spare nodes for k FDs this size; try more (smaller) FDs
			}
			total := current + k*perFd
			if total-desiredRaw > overshootCap {
				continue
			}
			placeUniform(p, perFd, freshChosen(k, perFd), existingDrives, cons, growFor, newByNode, newFor)
			finalizeFeasibility()
			if plan.Infeasible == "" && total > desiredRaw {
				// k NEW fresh FDs cover the delta; existing FDs are untouched.
				plan.OverProvisions = append(plan.OverProvisions, overProvisionMsg(0, k, perFd, total))
			}
			return
		}
	}

	// --- Grow phase: search the final FD count N for the smallest feasible uniform level L. ---
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
		// the no-grow even-split above (perFd <= maxPerFdCap, and T0 <= maxPerFdCap) would already have
		// placed and returned on — so this is effectively unreachable. Handle it as a plain create-at-T0 anyway.
		kFresh := best.N - numExisting
		placeUniform(p, T0, freshChosen(kFresh, T0), existingDrives, cons, growFor, newByNode, newFor)
		finalizeFeasibility()
		if plan.Infeasible == "" && best.total > desiredRaw {
			// L==T0 defensive create-at-T0: kFresh NEW FDs at T0; existing FDs are untouched.
			plan.OverProvisions = append(plan.OverProvisions, overProvisionMsg(0, kFresh, T0, best.total))
		}
		return
	}

	// best.L > T0: a uniform grow is required.
	if !cons.AllowInPlaceGrowth {
		// Growth is disabled, so the preferred no-grow cover (even-split-to-delta on fresh FDs up to
		// maxPerFdCap) was already attempted above and found no feasible k — no additional placement is
		// possible here. maxPerFdCap and the T0-clone framing (kNeeded vs kAvail) are recomputed locally
		// just to describe WHY the frozen layout cannot reach the target.
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
	placeUniform(p, best.L, chosen, existingDrives, cons, growFor, newByNode, newFor)
	finalizeFeasibility()
	if plan.Infeasible == "" && best.total > desiredRaw {
		// Grow path: the numExisting existing FDs are grown in place to best.L, plus kFresh NEW FDs at best.L.
		plan.OverProvisions = append(plan.OverProvisions, overProvisionMsg(numExisting, kFresh, best.L, best.total))
	}
}

// selectUniform picks the smallest N >= minFd (greenfield pool) such that the N highest-headroom candidate
// FDs (headroom-desc, per orderFreshFdGroups) each clear T = ceil(desiredRaw/N); ok=false when N runs out.
// preferNodes (may be nil) biases selection toward FDs already carrying a freshly-planned OTHER-pool
// container, so both pools can share a node — see pickPreferringColocated.
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
		// Same N/target as a plain top-N pick; only WHICH N FDs get filled flips toward co-located ones.
		return pickPreferringColocated(candidates, N, target, preferNodes), target, true
	}
	return nil, 0, false
}

// pickPreferringColocated picks N FDs (headroom >= target) from the headroom-desc candidates, taking
// co-located ones (member node in preferNodes) first, preserving headroom-desc order within each tier.
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

// uniformInfeasibleMsg explains why no uniform tiling fits, reporting the per-FD share at the largest
// (most forgiving) N and the smallest candidate FD headroom that falls short of it.
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
	// At the largest N (all candidates) the per-FD share is smallest; even that doesn't fit.
	N := len(candidates)
	T := max(cons.MinChunkSizeGiB, util.CeilDiv(desiredRaw, N))
	smallest := candidates[N-1].headroom
	msg = fmt.Sprintf(
		"%s: cannot place %d GiB uniformly across %d failure domains — the smallest usable FD holds %d GiB, below the %d GiB per-FD share; add capacity or lower clusterCapacity",
		p, desiredRaw, N, smallest, T)
	return msg, "drive capacity", max(0, T-smallest)
}

// rejectedNodesBreakdown buckets every non-candidate node by its rejection reason and formats each bucket,
// e.g. "n4, n5, n6: no QLC drive capacity" — sorted, with "+N more" tails. Returns "" if nothing rejected.
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

// existingFdsAsChosen builds one *fdGroup per existing pool-bearing FD (member nodes, headroom desc) so
// they can join placeUniform alongside fresh FDs (g.headroom is left zero; placeUniform reads only g.nodes).
// Cap-0 (other-pool) containers are included so a TLC-only FD can be converted to mixed for the QLC pool.
func existingFdsAsChosen(p poolKind, existingDrives []ExistingContainer, states map[string]*nodeState, cons *CapacityConstraints) []*fdGroup {
	byFd := map[string]*fdGroup{}
	order := make([]*fdGroup, 0)
	seen := map[string]struct{}{}
	for i := range existingDrives {
		c := &existingDrives[i]
		if c.FDValue == "" || c.Unscheduled || c.Node == "" || states[c.Node] == nil {
			continue
		}
		// An other-pool-only FD isn't pre-existing for this pool — it belongs to the fresh/greenfield path.
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

// placeUniform is the ONE placement primitive: makes each chosen FD hold exactly `T` of pool p, split
// EVENLY across the FD's member hosts. Per host: an existing container is GROWN (cross-pool conversion or
// same-pool top-up); otherwise a new one is CREATED (MinChunk floor, base memory charged once). An FD that
// cannot reach `T` is rolled back and skipped — poolFeasibility then flags the shortfall.
func placeUniform(
	p poolKind,
	target int,
	chosen []*fdGroup,
	existingDrives []ExistingContainer,
	cons *CapacityConstraints,
	growFor func(ExistingContainer) *ContainerGrowth,
	newByNode map[string]*NewContainer,
	newFor func(node, fd string) *NewContainer,
) {
	// A node carrying any existing drive container grows in place; one without creates a new container.
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

		// T split as evenly as possible across member hosts (first `rem` get one extra GiB); each host's
		// share is the ADDITIONAL pool-p capacity needed, net of what it already holds.
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

// freshExclusion returns the nodes fresh placement must avoid: normally just nodes already hosting THIS
// pool (a different-pool node can still be converted to mixed via placeUniform's grow path); but with
// in-place growth disabled, every node hosting any drive container is excluded since none may be converted.
func freshExclusion(existingDrives []ExistingContainer, p poolKind, cons *CapacityConstraints) map[string]struct{} {
	if cons.AllowInPlaceGrowth {
		return poolNodeUsed(existingDrives, p)
	}
	return allDriveNodes(existingDrives)
}

// OverProvisionCapGiB is the GiB a pool may exceed its desired raw capacity without triggering the
// ClusterCapacityShrink advisory — the create-new-before-grow path over-provisions by up to one uniform
// chunk on purpose, so intentional overage stays silent. Also suppresses the advisory for a
// clusterCapacity downsize smaller than this fraction.
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

// resolveExactNewFds maps an explicit driveContainers count (targetFds) onto the exact number of fresh FDs
// to add, or -1 when unset (auto). Fails fast if the pin is below FDs already present, needs placement with
// no room, or drives a per-container share below MinChunk. delta is the capacity still to place.
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
	// hasDeletingDriveContainer: a member node still hosts a this-cluster drive container being deleted;
	// takeFreshAtLevel deprioritizes it so a replacement isn't recreated on the node it was just deleted from.
	hasDeletingDriveContainer bool
	// colocated: set by colocatedFirst when a member node already carries the OTHER pool's pending
	// container. takeFreshAtLevel treats it as the PRIMARY preference (above not-deleting), since that
	// just-freed node is exactly where both pools should co-locate as one mixed container.
	colocated bool
}

// takeFreshAtLevel returns up to k fresh candidate FDs that can host `level`, preferring FDs with no
// deleting container over those that have one (each tier headroom-desc). A just-freed node re-enters the
// fresh pool as the emptiest FD, so raw headroom alone would recreate the replacement right where it was
// deleted; the `level` filter still lets a capable deleting FD win as a fallback over a too-small one.
func takeFreshAtLevel(fresh []*fdGroup, k, level int) []*fdGroup {
	out := make([]*fdGroup, 0, k)
	// Co-location is the PRIMARY key, not-deleting the SECONDARY. Tier order: colocated+notDeleting,
	// colocated+deleting, notColocated+notDeleting, notColocated+deleting.
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

// orderFreshFdGroups returns FDs with placeable headroom for pool p (candidate nodes not in poolNodeUsed,
// each >= MinChunk), grouped by FDValue, ordered by best-node headroom desc. Shared by selectUniform,
// placeUniform, and the uniform-increase scan so they agree on the candidate set and order.
func orderFreshFdGroups(p poolKind, states map[string]*nodeState, poolNodeUsed map[string]struct{}, cons *CapacityConstraints) []*fdGroup {
	var cands []*nodeState
	for _, ns := range states {
		if _, used := poolNodeUsed[ns.nc.NodeName]; used {
			continue
		}
		// Ineligible nodes never start a fresh FD here; existingFdsAsChosen still counts one that already
		// hosts a container in this pool, so a cordoned backend's existing drives stay in the plan.
		if ns.nc.IneligibleReason != "" {
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

// otherPoolPreferNodes is the set of nodes whose pending new container (placed earlier in this plan)
// carries the other pool but not p — placing p there yields one fresh mixed container, so fresh placement
// biases toward them. Excludes existing single-pool containers: co-location only happens by creating a
// fresh mixed container on an empty node, not by converting an already-running one.
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
// increase fresh candidate list so takeFreshAtLevel draws co-located FDs first — an under-capacity
// co-located node is still skipped and placement falls back to a split. No-op when preferNodes is empty.
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

// countPoolCapableNodes counts nodes that can physically host pool p (have its drive type) — spatial
// constraint, not free space — so PlanCapacity can plan the more constrained pool first. An ineligible
// node (cordoned/not ready/untolerated taint) counts only when poolUsed already credits it with a pool-p
// container, since fresh placement can never land on it and would otherwise inflate the candidate count.
func countPoolCapableNodes(states map[string]*nodeState, poolUsed map[string]struct{}, p poolKind) int {
	n := 0
	for name, ns := range states {
		capacity := ns.nc.TlcGiB
		if p == poolQLC {
			capacity = ns.nc.QlcGiB
		}
		if capacity <= 0 {
			continue
		}
		if ns.nc.IneligibleReason != "" {
			if _, used := poolUsed[name]; !used {
				continue
			}
		}
		n++
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
			// Name the real cause: growth was disabled, not a lack of capacity.
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

// distinctDriveFds counts distinct FDs carrying drive capacity in the FINAL state (existing + newly
// created) — in AUTO FD mode this equals the drive-container count, compared against explicit DriveContainers.
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
