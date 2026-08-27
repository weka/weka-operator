package capacityplanner

import (
	"fmt"
	"sort"
	"strings"
)

// autofulldrives_compute.go sizes the compute side of an auto-full-drives plan. Compute is derived from the
// drive cores the walk settled on, at the configured ratio with a hard 1:1 floor (RequiredComputeCores); a
// shortfall is covered by new containers on free compute-eligible nodes first, with in-place growth of
// existing containers topping up only what new containers cannot carry — infeasible only when both levers
// fall short. Unlike planCompute (planner.go), this mode does no FD-spread placement: it occupies every node
// its selector matches.

// autoComputeInput is everything the compute step reads. remaining is the per-node headroom the drive walk
// left behind, and is mutated as in-place growth reserves against it.
type autoComputeInput struct {
	desired      AutoFullDrivesDesired
	existing     []ExistingComputeContainer
	computeNodes map[string]bool
	remaining    map[string]NodeCapacity
	// totalTlcGiB is the cluster's realized TLC capacity after this plan, the numerator of every compute
	// container's capacity-based hugepages. Sourced from the drive walk's own tlcGiBTaken total, so it holds
	// regardless of whether Status.Allocations has caught up and claimed the drives yet.
	totalTlcGiB        int
	existingDriveCount int
	cons               *CapacityConstraints
}

// autoComputeEntry is one existing compute container carried into the plan: kept at its current size unless
// the growth pass raises it, plus how many extra data cores its node can still absorb in place.
type autoComputeEntry struct {
	spec         ComputeContainerSpec
	growHeadroom int
}

// computeProbe is one candidate growTake's outcome: the layout the derivation settled on for the target
// that growTake leaves, or growthAlone when the growth covers the whole deficit and there is nothing to
// derive. Zero value with growthAlone false is the "no new containers" case.
type computeProbe struct {
	count, cores int
	warnings     []string
	growthAlone  bool
}

func autoComputeSpecs(kept []autoComputeEntry) []ComputeContainerSpec {
	out := make([]ComputeContainerSpec, 0, len(kept))
	for _, e := range kept {
		out = append(out, e.spec)
	}
	return out
}

// planComputeAutoFullDrives derives and lays out the compute containers, writing ComputeContainers,
// ComputeCores, ComputeNodes and ComputeLayout on the plan (or an infeasibility).
func planComputeAutoFullDrives(in *autoComputeInput, plan *CapacityPlan) {
	if in.computeNodes == nil {
		setInfeasible(plan, &InfeasibilityReport{Reason: "internal: compute node set not provided", Pool: "compute"})
		return
	}

	// RequiredComputeCores is the caller's to set: PlanAutoFullDrives writes it before the feasibility gate,
	// so an infeasible plan still reports the demand its claimed drives imply. Everything below reads that
	// one number.
	plan.TotalQlcDriveCores = 0 // full drives is TLC-only by construction

	// Nothing to size against. A plan with drive containers but zero drive cores is internally inconsistent —
	// fail loudly rather than sizing compute against a phantom zero.
	if plan.TotalTlcDriveCores == 0 && len(in.existing) == 0 && len(plan.Create) == 0 && len(plan.Grow) == 0 &&
		in.desired.ComputeCores == 0 {
		if in.existingDriveCount == 0 {
			return
		}
		setInfeasible(plan, &InfeasibilityReport{
			Reason: fmt.Sprintf(
				"internal: %d existing drive container(s) present but TotalTlcDriveCores resolved to 0 — "+
					"refusing to size compute against an internally inconsistent plan", in.existingDriveCount),
			Pool:    "compute",
			Binding: "driveCores",
			Fixes:   fixesAutoFullDrivesCompute(in.cons),
		})
		return
	}

	kept, pinned, keptCores := autoKeptCompute(in)
	deficit := max(plan.RequiredComputeCores-keptCores, 0)

	// Already satisfied. Skipping the derivation is load-bearing, not an optimisation: deriveComputeLayout
	// with a zero target returns max(floor,1) containers of one core each, which would manufacture phantom
	// compute containers on every converged reconcile.
	if deficit == 0 && len(kept) > 0 {
		if !autoRederiveKeptHugepages(kept, len(kept), in, plan) {
			return
		}
		finishComputePlan(plan, autoComputeSpecs(kept), 0)
		return
	}

	// Placeable = eligible nodes with no compute container yet. A pinned node keeps hosting the one it has;
	// offering it to the derivation as well would double-book it.
	placeable := make([]string, 0, len(in.computeNodes))
	for node, eligible := range in.computeNodes {
		if !eligible {
			continue
		}
		if _, known := in.remaining[node]; !known {
			continue
		}
		if _, isPinned := pinned[node]; isPinned {
			continue
		}
		placeable = append(placeable, node)
	}
	sort.Strings(placeable)

	coreHeadroom := make([]int, len(placeable))
	nodeHugepagesMiB := make([]int, len(placeable))
	for i, node := range placeable {
		nc := in.remaining[node]
		coreHeadroom[i] = physicalCPUToDataCores(&nc, 0, in.cons, true)
		nodeHugepagesMiB[i] = nc.AvailableHugepagesMiB
	}

	growTotal := 0
	for _, e := range kept {
		growTotal += e.growHeadroom
	}

	// A cluster cannot form below FormClusterMinComputeContainers, so the derivation must not return the
	// fewest containers carrying the required cores (4x12 for 24 drive cores would hang forever on "expected
	// 5, got 4"). The floor discounts what already exists: 3 kept against a floor of 5 needs 2 more, not 5.
	floor := max(1, in.cons.MinComputeContainers-len(kept))
	// The hugepages divisor counts kept containers too — they share the same capacity-based term.
	hugepagesFor := func(count, cores int) int {
		return ComputeContainerHugepagesMiB(in.totalTlcGiB, 0, len(kept)+count, cores, in.cons)
	}

	// Prefer new containers, top up with in-place growth: the least growth that lets the rest fit on free
	// nodes. probe reports whether a given growTake is coverable, deriving the layout for the target it
	// leaves; it has no side effects, so it is safe to call for candidates that are never committed.
	probe := func(growTake int) (computeProbe, string) {
		// Growth alone closes it, so there is nothing to derive — and deriving against a zero target would
		// resurrect the phantom-container case guarded above. growTake==0 is excluded: a zero deficit there
		// still owes the derivation its floor containers.
		if deficit-growTake <= 0 && growTake > 0 {
			return computeProbe{growthAlone: true}, ""
		}
		// specCount is hard 0: a pinned computeContainers means the cluster is not in this mode at all.
		count, cores, infeasible, warnings := deriveComputeLayout(
			0, in.desired.ComputeCores, deficit-growTake,
			floor, in.cons.MaxCoresPerContainer, coreHeadroom, nodeHugepagesMiB, hugepagesFor,
		)
		return computeProbe{count: count, cores: cores, warnings: warnings}, infeasible
	}

	// unaidedReason is the growTake==0 attempt's own explanation (usually the binding resource, often compute
	// hugepages) for why new containers alone do not fit, and leads the infeasibility below.
	best, unaidedReason := probe(0)
	growTake := 0
	if unaidedReason != "" {
		// The search space is [1, hi]: hi is where growth alone would close the deficit, or all the growth
		// there is, whichever comes first. Either way the least feasible growTake wins — it is the one that
		// disturbs the fewest running containers and places the most new ones.
		found := false
		hi := min(deficit, growTotal)
		if in.desired.ComputeCores == 0 {
			// Auto-derived cores: coverability is monotone in growTake. For any candidate container count the
			// derivation tries, a smaller target means fewer cores per container and therefore strictly less
			// hugepages at the same count — so a growTake that covers the deficit implies every larger one
			// does. That licenses a binary search, which matters because growTotal reaches
			// len(kept)*MaxCoresPerContainer and every probe re-derives the whole layout.
			for lo := 1; lo <= hi; {
				mid := lo + (hi-lo)/2
				if p, infeasible := probe(mid); infeasible == "" {
					best, growTake, found = p, mid, true
					hi = mid - 1 // a smaller growTake may also cover it — keep the least
				} else {
					lo = mid + 1
				}
			}
		} else {
			// A pinned computeCores breaks that monotonicity: the container count is ceil(target/cores), so
			// more growth means FEWER new containers, and the capacity-based hugepages term — divided by the
			// container count — rises as it shrinks. Coverability can therefore hold on an interval and fail
			// above it, which a binary search would walk away from. Scan.
			for g := 1; g <= hi; g++ {
				if p, infeasible := probe(g); infeasible == "" {
					best, growTake, found = p, g, true
					break
				}
			}
		}
		if !found {
			// Both levers exhausted.
			reason := "compute: " + unaidedReason
			if growTotal > 0 {
				reason += fmt.Sprintf(
					"; growing the %d existing compute container(s) in place offers only %d more core(s), "+
						"which does not close the %d-core shortfall", len(kept), growTotal, deficit)
			}
			setInfeasible(plan, &InfeasibilityReport{
				Reason: reason,
				Pool:   "compute",
				Fixes:  fixesAutoFullDrivesCompute(in.cons),
			})
			return
		}
	}

	// `best.count` new containers follow, so the final steady-state total is len(kept)+best.count.
	totalCount := len(kept) + best.count
	autoCommitComputeGrowth(kept, growTake, totalCount, in)
	if !autoRederiveKeptHugepages(kept, totalCount, in, plan) {
		return
	}
	if best.growthAlone {
		finishComputePlan(plan, autoComputeSpecs(kept), 0)
		return
	}
	// Every compute advisory accumulates here and leaves as one Warning: they share the single reason
	// AutoFullDrivesComputeLayout, whose throttle key ignores the message, so a second Warning under that
	// reason is silently dropped for the whole window instead of reported. autoPlaceNewCompute's surplus
	// advisory is the only contributor today: deriveComputeLayout never populates its warnings return.
	advisories := append([]string(nil), best.warnings...)
	autoPlaceNewCompute(in, plan, kept, placeable, coreHeadroom, best.count, best.cores, deficit-growTake, &advisories)
	if len(advisories) > 0 {
		plan.Warnings = append(plan.Warnings,
			fleetWarning(WarningKindComputeLayout, "auto full drives: %s", strings.Join(advisories, "; ")))
	}
}

// autoKeptCompute collects the existing compute containers this plan carries forward, at their current size,
// with each one's in-place growth headroom. A container is counted only if it is positionable (names a node
// present in the inventory) — one that can neither be kept nor placed is excluded conservatively, since
// counting it would understate the deficit.
func autoKeptCompute(in *autoComputeInput) (kept []autoComputeEntry, pinned map[string]struct{}, keptCores int) {
	pinned = make(map[string]struct{}, len(in.existing))

	// selected is a positionable container; growable records whether pass 2 should derive its in-place
	// headroom. Two passes because the headroom divisor (below) needs the final selected count, which isn't
	// known until this filtering pass over in.existing completes.
	type selected struct {
		ec       *ExistingComputeContainer
		nc       NodeCapacity
		growable bool
	}
	var picks []selected
	for i := range in.existing {
		ec := &in.existing[i]
		if ec.Node == "" {
			continue
		}
		nc, known := in.remaining[ec.Node]
		if !known {
			continue
		}
		keptCores += ec.NumCores
		_, dup := pinned[ec.Node]
		pinned[ec.Node] = struct{}{}

		// Growth needs a scheduled pod to grow into, and a node's headroom can only be offered once — so a
		// second container on the same node is kept but not growable.
		picks = append(picks, selected{ec: ec, nc: nc, growable: !ec.Unscheduled && !dup})
	}

	// `count` (new containers) is derived from the headroom this pass computes, so the final steady-state
	// total is unavailable here. len(picks) is the smallest the final count can be, which yields the largest
	// per-container hugepages and therefore the conservative headroom bound: any actual final count is
	// >= len(picks), so committed growth always fits within what headroom promised.
	kept = make([]autoComputeEntry, 0, len(picks))
	for i := range picks {
		p := &picks[i]
		headroom := 0
		if p.growable {
			headroom = autoComputeGrowHeadroom(p.ec, &p.nc, len(picks), in)
		}
		kept = append(kept, autoComputeEntry{
			spec:         ComputeContainerSpec{Node: p.ec.Node, NumCores: p.ec.NumCores, HugepagesMiB: p.ec.HugepagesMiB},
			growHeadroom: headroom,
		})
	}
	return kept, pinned, keptCores
}

// autoComputeGrowHeadroom is how many extra data cores ec's node can absorb in place, bounded by
// MaxCoresPerContainer and the node's remaining CPU/hugepages/memory. All bounds are deltas: the container's
// current footprint is already charged against remaining. The hugepages bound has no closed form, so the
// candidate size walks down until the delta fits.
func autoComputeGrowHeadroom(ec *ExistingComputeContainer, nc *NodeCapacity, keptCount int, in *autoComputeInput) int {
	// includeBase=false throughout: the management core and memory base are already reserved by the running
	// container, so only the per-core increments are charged.
	maxCores := ec.NumCores + physicalCPUToDataCores(nc, 0, in.cons, false)
	if in.cons.MaxCoresPerContainer > 0 {
		maxCores = min(maxCores, in.cons.MaxCoresPerContainer)
	}
	if in.cons.MemoryPerCoreMiB > 0 {
		maxCores = min(maxCores, ec.NumCores+nc.AvailableMemoryMiB/in.cons.MemoryPerCoreMiB)
	}
	for maxCores > ec.NumCores &&
		ComputeContainerHugepagesMiB(in.totalTlcGiB, 0, max(keptCount, 1), maxCores, in.cons)-ec.HugepagesMiB > nc.AvailableHugepagesMiB {
		maxCores--
	}
	return max(maxCores-ec.NumCores, 0)
}

// autoCommitComputeGrowth hands out `need` extra cores across the kept containers, largest-headroom-first
// with node name as tiebreak so the same fleet always grows the same containers, reserving what it hands out
// against remaining. totalCount is the plan's final steady-state compute container count, the authoritative
// divisor for the capacity-based hugepages term every container shares.
func autoCommitComputeGrowth(kept []autoComputeEntry, need, totalCount int, in *autoComputeInput) {
	if need <= 0 {
		return
	}
	order := make([]int, 0, len(kept))
	for i := range kept {
		if kept[i].growHeadroom > 0 {
			order = append(order, i)
		}
	}
	sort.Slice(order, func(a, b int) bool {
		x, y := kept[order[a]], kept[order[b]]
		if x.growHeadroom != y.growHeadroom {
			return x.growHeadroom > y.growHeadroom
		}
		return x.spec.Node < y.spec.Node
	})

	for _, i := range order {
		if need <= 0 {
			return
		}
		e := &kept[i]
		take := min(e.growHeadroom, need)
		need -= take

		newCores := e.spec.NumCores + take
		// Never below what the container already reserves: the pod's hugepages limit is immutable, and this
		// plan's headroom accounting charges hugepages from the spec — a lower figure would credit back
		// capacity the pod has not released, and the apply layer would refuse to write it anyway.
		newHP := max(ComputeContainerHugepagesMiB(in.totalTlcGiB, 0, max(totalCount, 1), newCores, in.cons),
			e.spec.HugepagesMiB)

		nc := in.remaining[e.spec.Node]
		nc.AllocatableCPU = max(nc.AllocatableCPU-physicalCPUCost(&nc, take, in.cons, false), 0)
		nc.AvailableHugepagesMiB = max(nc.AvailableHugepagesMiB-(newHP-e.spec.HugepagesMiB), 0)
		nc.AvailableMemoryMiB = max(nc.AvailableMemoryMiB-take*in.cons.MemoryPerCoreMiB, 0)
		in.remaining[e.spec.Node] = nc

		e.spec.NumCores, e.spec.HugepagesMiB = newCores, newHP
	}
}

// autoRederiveKeptHugepages re-derives every kept container's hugepages at the plan's final container count:
// the capacity-based term is a share of the cluster total divided by that count, so it changes whenever
// claimed capacity or the count moves, even when a container's own cores didn't. The delta is charged against
// the node's remaining hugepages; a node that cannot absorb a rise makes the plan infeasible instead.
func autoRederiveKeptHugepages(kept []autoComputeEntry, totalCount int, in *autoComputeInput, plan *CapacityPlan) bool {
	for i := range kept {
		e := &kept[i]
		// Clamped for the same reason as autoCommitComputeGrowth: the computed figure falls when the
		// container count rises, but only a rise is ever written, so a fall is a no-op here rather than a
		// headroom credit.
		newHP := max(ComputeContainerHugepagesMiB(in.totalTlcGiB, 0, max(totalCount, 1), e.spec.NumCores, in.cons),
			e.spec.HugepagesMiB)
		delta := newHP - e.spec.HugepagesMiB
		if delta == 0 {
			continue
		}
		nc := in.remaining[e.spec.Node]
		if delta > nc.AvailableHugepagesMiB {
			setInfeasible(plan, &InfeasibilityReport{
				Reason: fmt.Sprintf(
					"compute: node %s cannot absorb the re-derived hugepages for its existing %d-core compute "+
						"container (needs %d MiB, %d MiB more than it reserves, but only %d MiB is free) — "+
						"claimed capacity or the compute container count moved, so every compute container's "+
						"share of the capacity-based term changed",
					e.spec.Node, e.spec.NumCores, newHP, delta, nc.AvailableHugepagesMiB),
				Pool:    "compute",
				Binding: "hugepages",
				Fixes:   fixesAutoFullDrivesCompute(in.cons),
			})
			return false
		}
		nc.AvailableHugepagesMiB = max(nc.AvailableHugepagesMiB-delta, 0)
		in.remaining[e.spec.Node] = nc
		e.spec.HugepagesMiB = newHP
	}
	return true
}

// autoPlaceNewCompute places `count` new containers of `cores` each across the free nodes that fit, filling
// them to cover `target` cores, and finishes the plan.
func autoPlaceNewCompute(
	in *autoComputeInput, plan *CapacityPlan, kept []autoComputeEntry,
	placeable []string, coreHeadroom []int, count, cores, target int, advisories *[]string,
) {
	layout := autoComputeSpecs(kept)
	if count <= 0 {
		finishComputePlan(plan, layout, cores)
		return
	}
	totalCount := len(kept) + count
	perContainerHP := ComputeContainerHugepagesMiB(in.totalTlcGiB, 0, max(totalCount, 1), cores, in.cons)

	// Candidates are the free nodes that actually fit the uniform footprint, most core headroom first. Node
	// name breaks ties: cores and hugepages are pod-spec fields, so an unstable order would churn pod
	// recreations on every reconcile. No FD-spread ordering — see the file header.
	type candidate struct {
		node string
		cap  int
	}
	candidates := make([]candidate, 0, len(placeable))
	for i, node := range placeable {
		nc := in.remaining[node]
		if physicalCPUCost(&nc, cores, in.cons, true) > nc.AllocatableCPU || nc.AvailableHugepagesMiB < perContainerHP {
			continue
		}
		candidates = append(candidates, candidate{node: node, cap: coreHeadroom[i]})
	}
	sort.Slice(candidates, func(a, b int) bool {
		if candidates[a].cap != candidates[b].cap {
			return candidates[a].cap > candidates[b].cap
		}
		return candidates[a].node < candidates[b].node
	})

	// The fill target is at least one core per container: the form-cluster floor can ask for more containers
	// than the shortfall needs, and the balanced split below must not hand a container zero cores. A
	// computeCores pin fixes each container's size instead.
	shortfall := max(target, count)
	if in.desired.ComputeCores > 0 {
		shortfall = count * cores
	} else if target < count {
		*advisories = append(*advisories, fmt.Sprintf(
			"a cluster cannot form below %d compute container(s) but the compute:drive ratio needs only %d more "+
				"core(s); the surplus container(s) are created with the 1-core minimum",
			count, target))
	}

	if count > len(candidates) {
		setInfeasible(plan, &InfeasibilityReport{
			Reason: fmt.Sprintf(
				"compute: cannot place %d new compute container(s) to cover the %d-core shortfall — "+
					"only %d free fitting compute node(s) (each holds up to %d cores + %d MiB hugepages)",
				count, shortfall, len(candidates), cores, perContainerHP),
			Pool:    "compute",
			Binding: "cores",
			Fixes:   fixesAutoFullDrivesCompute(in.cons),
		})
		return
	}

	// Balanced fill: the first `rem` containers get one extra core.
	base, rem := shortfall/count, shortfall%count
	for i := range count {
		cCores := base
		if i < rem {
			cCores++
		}
		node := candidates[i].node
		cHP := ComputeContainerHugepagesMiB(in.totalTlcGiB, 0, totalCount, cCores, in.cons)
		if nc := in.remaining[node]; candidates[i].cap < cCores || nc.AvailableHugepagesMiB < cHP {
			// Unreachable by construction (candidates were filtered against the uniform footprint); a guard
			// rather than a panic, since over-committing a node is worse than a loud plan failure.
			setInfeasible(plan, &InfeasibilityReport{
				Reason: fmt.Sprintf("compute: free compute node %s cannot host a %d-core compute container", node, cCores),
				Pool:   "compute", Binding: "hugepages", Fixes: fixesAutoFullDrivesCompute(in.cons),
			})
			return
		}
		layout = append(layout, ComputeContainerSpec{Node: node, NumCores: cCores, HugepagesMiB: cHP})
	}
	finishComputePlan(plan, layout, cores)
}

// finishComputePlan is the single writer of the plan's compute fields, so the four can never disagree.
// derivedCores is the uniform target when there was a derivation; 0 means "report the largest container",
// which keeps the summary honest on the growth-only path.
func finishComputePlan(plan *CapacityPlan, layout []ComputeContainerSpec, derivedCores int) {
	sort.Slice(layout, func(i, j int) bool { return layout[i].Node < layout[j].Node })
	nodes := make([]string, 0, len(layout))
	for _, l := range layout {
		nodes = append(nodes, l.Node)
	}
	plan.ComputeContainers = len(layout)
	plan.ComputeNodes = nodes
	plan.ComputeLayout = layout
	plan.ComputeCores = derivedCores
	if derivedCores == 0 {
		for _, l := range layout {
			plan.ComputeCores = max(plan.ComputeCores, l.NumCores)
		}
	}
}
