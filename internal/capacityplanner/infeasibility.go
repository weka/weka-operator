package capacityplanner

import (
	"fmt"
	"sort"
	"strings"
)

// infeasibility.go holds the structured infeasibility report for plan.Infeasible, shared by all
// consumers (weka-capacity CLI, ClusterCapacityInfeasible event).

// NodeRejection is the structured form of one rejectedNodesBreakdown entry.
type NodeRejection struct {
	Node string
	// Binding is the dimension that caps the node below the minimum chunk, or "already hosts a <pool>
	// container" when the node is excluded because it already runs a pool-p drive container.
	Binding string
	// FreeGiB is the node's usable pool headroom (GiB); 0 when the pool's drive type is absent.
	FreeGiB int
	// NeededGiB is the per-FD floor the node must clear to be a candidate (the minimum chunk size).
	NeededGiB int
	// Needed/Available/Unit describe a rejection whose binding dimension is not capacity — the
	// auto-full-drives node-fit gate, where a node falls short on physical CPU, hugepages or memory. Unit
	// is the human unit ("physical CPU", "MiB hugepages", "MiB memory"), empty when the GiB pair above
	// carries the numbers: a GiB humanizer would otherwise print a CPU count of 16 as "16 GiB".
	Needed    int
	Available int
	Unit      string
}

// InfeasibilityReport is the structured explanation for plan.Infeasible, letting callers render the
// binding cause and fixes without re-parsing the message.
type InfeasibilityReport struct {
	// Reason is the human summary; it is byte-identical to plan.Infeasible.
	Reason string
	// Pool is "tlc", "qlc", "compute", or "" (cluster-wide, e.g. protection-floor check).
	Pool string
	// Binding is the tightest cause: "drive capacity" | "cores" | "hugepages" | "memory" |
	// "failure domains" | "protection" | "driveContainers" | "driveCores" | "" (unclassified).
	Binding string
	// ShortfallGiB is how much the pool/dimension is short, when quantifiable; 0 otherwise.
	ShortfallGiB int
	// RejectedNodes is the per-node breakdown of why the candidate set fell short (drive pools only).
	RejectedNodes []NodeRejection
	// Fixes is the ordered, actionable remediation catalog for Binding.
	Fixes []string
}

// autoFullDrivesMaxNamedNodes caps how many node names the node-fit message spells out before switching to a
// "(+N more)" tail; RejectedNodes always carries every offender.
const autoFullDrivesMaxNamedNodes = 10

// fixesAutoFullDrivesMaxNamedNodes caps the node names spelled out in the fix catalog's remediation tips —
// shorter than autoFullDrivesMaxNamedNodes because a fix tip is read, not just skimmed.
const fixesAutoFullDrivesMaxNamedNodes = 5

// autoNodeFitInfeasible turns the auto-full-drives walk's collected fit failures into the plan-wide
// infeasibility. There is no partial-fit outcome in that mode — drives are never dropped to make a container
// fit — so one node short of resources blocks the whole cluster, and the fixes say how to exclude it if that
// is the intent.
func autoNodeFitInfeasible(failures []autoFitFailure) *InfeasibilityReport {
	names := make([]string, 0, len(failures))
	details := make([]string, 0, len(failures))
	rejected := make([]NodeRejection, 0, len(failures))
	growthNodes := make([]string, 0, len(failures))
	bindings := map[string]int{}
	for i := range failures {
		f := &failures[i]
		names = append(names, f.node)
		bindings[f.fit.binding]++
		// Growth alone does not make the hazard: the remedy is "delete the compute container on this node", so
		// the node must actually host one of ours. Without this the clause fires on any growth failure, naming a
		// container that does not exist.
		if f.kind == fitKindGrowth && f.ownCompute {
			growthNodes = append(growthNodes, f.node)
		}
		if len(details) < autoFullDrivesMaxNamedNodes {
			details = append(details, fmt.Sprintf(
				"%s (%s: %d drive(s) at %d core(s) needs %d %s, %d free)",
				f.node, f.kind, f.numDrives, f.toCores, f.fit.needed, f.fit.unit, f.fit.available))
		}
		rejected = append(rejected, NodeRejection{
			Node:      f.node,
			Binding:   f.fit.binding,
			Needed:    f.fit.needed,
			Available: f.fit.available,
			Unit:      f.fit.unit,
		})
	}
	list := strings.Join(details, "; ")
	if len(failures) > len(details) {
		list += fmt.Sprintf(" (+%d more)", len(failures)-len(details))
	}

	// Name a single binding dimension only when every node agrees on it; otherwise leave it unclassified
	// rather than let one node's cause stand for the fleet.
	binding := ""
	if len(bindings) == 1 {
		for b := range bindings {
			binding = b
		}
	}

	reason := fmt.Sprintf(
		"auto full drives: %d node(s) cannot host a drive container sized for their own signed full drives — "+
			"drives are never dropped to make a container fit, so the whole plan is infeasible and nothing is "+
			"created: %s",
		len(failures), list)
	if len(growthNodes) > 0 {
		// A growth failure (as opposed to create) means the container already exists and must grow into headroom
		// that is no longer free. Compute reservations only ever rise, so a compute container this cluster placed
		// while the drive container was smaller can be holding exactly that room — "may be", not a claim, since
		// the planner cannot see what actually consumes it. Kept to one sentence: this lands in a Kubernetes
		// event, and the full remedy catalog travels in Fixes for the CLI to render.
		growthList := listNodesCapped(growthNodes, autoFullDrivesMaxNamedNodes)
		reason += fmt.Sprintf(
			" — this growth may be blocked by this cluster's own compute container on %s, whose reservation only "+
				"ever rises; deleting it lets the next reconcile grow the drive container first. If weka refuses "+
				"that deactivation because active compute would drop too low, add compute capacity elsewhere "+
				"first.", growthList)
	}

	return &InfeasibilityReport{
		Reason:        reason,
		Pool:          "drive",
		Binding:       binding,
		RejectedNodes: rejected,
		Fixes:         fixesAutoFullDrivesNodeFit(names, growthNodes),
	}
}

// tag returns the lower-case pool tag ("tlc"/"qlc") used in the structured report's Pool field.
func (p poolKind) tag() string {
	if p == poolQLC {
		return DriveTypeQLC
	}
	return DriveTypeTLC
}

// setInfeasible records report on plan, mirroring report.Reason into the legacy Infeasible string.
func setInfeasible(plan *CapacityPlan, report *InfeasibilityReport) {
	plan.Infeasible = report.Reason
	plan.Infeasibility = report
}

// rejectedNodes is the structured, uncapped form of rejectedNodesBreakdown's text; classification must
// stay in sync with it.
func rejectedNodes(p poolKind, states map[string]*nodeState, poolUsed map[string]struct{}, cons *CapacityConstraints) []NodeRejection {
	names := make([]string, 0, len(states))
	for name := range states {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]NodeRejection, 0, len(names))
	for _, name := range names {
		ns := states[name]
		if _, used := poolUsed[name]; used {
			out = append(out, NodeRejection{Node: name, Binding: fmt.Sprintf("already hosts a %s container", p)})
			continue
		}
		if ns.nc.IneligibleReason != "" {
			out = append(out, NodeRejection{Node: name, Binding: fmt.Sprintf("ineligible (%s)", ns.nc.IneligibleReason)})
			continue
		}
		h, binding := ns.nodeHeadroomBinding(p, cons, true)
		if h >= cons.MinChunkSizeGiB {
			continue // usable candidate — not rejected
		}
		out = append(out, NodeRejection{Node: name, Binding: binding, FreeGiB: h, NeededGiB: cons.MinChunkSizeGiB})
	}
	return out
}

// --- Fix-tip catalog (ordered, actionable). One builder per binding cause. ---

// fixesProtection: the scheme is below the protection floor.
func fixesProtection(minSW, minRL, minHS int) []string {
	return []string{
		fmt.Sprintf("raise stripeWidth to >=%d and redundancyLevel to >=%d (hotSpare >=%d)", minSW, minRL, minHS),
	}
}

// fixesDriveContainers: the pinned driveContainers count cannot be honored.
func fixesDriveContainers(resolved int) []string {
	if resolved > 0 {
		return []string{
			fmt.Sprintf("unset driveContainers (auto) or set it to %d — the count the plan resolves to", resolved),
		}
	}
	return []string{"unset driveContainers (auto) or set it to a value the plan can resolve to"}
}

// fixesDriveCores: the pinned driveCores is too small for the container.
func fixesDriveCores(needed int) []string {
	return []string{
		fmt.Sprintf("raise driveCores to >=%d, or unset it to auto-size from capacity", needed),
	}
}

// fixesMaxCoresPerContainer: a drive container's core count exceeds the hard per-container limit; the
// fix is more containers over the same capacity, not raising the limit.
func fixesMaxCoresPerContainer(limit int) []string {
	return []string{
		fmt.Sprintf("raise driveContainers so each container holds less capacity and needs at most %d cores", limit),
		"or lower clusterCapacity so the same container count suffices",
		fmt.Sprintf("driveCores, when pinned, must itself be <=%d — weka allows no more cores in one container", limit),
	}
}

// fixesCapacity: a pool is drive-capacity bound (the short type cannot be tiled uniformly).
func fixesCapacity(p poolKind, allowInPlaceGrowth bool) []string {
	fixes := []string{
		"shift driveTypesRatio toward the abundant type (e.g. 1:N)",
		fmt.Sprintf("add drives or nodes of the short type (%s)", p),
	}
	if !allowInPlaceGrowth {
		fixes = append(fixes, "enable enableDynamicDriveScalingForSharedDrives to grow existing containers in place")
	}
	return fixes
}

// fixesFailureDomains: fewer reachable failure domains than minFdNum.
func fixesFailureDomains(p poolKind, minFd int) []string {
	return []string{
		fmt.Sprintf("add nodes / failure domains that can host a %s drive container (need minFdNum = stripeWidth+redundancyLevel+hotSpare = %d)", p, minFd),
	}
}

// fixesGrowthDisabledFDs: growth disabled and not enough spare nodes to add T0-sized FDs.
func fixesGrowthDisabledFDs(extraNodes int) []string {
	return []string{
		fmt.Sprintf("add %d more node(s) not already running this pool's drive container", extraNodes),
		"or enable enableDynamicDriveScalingForSharedDrives to grow the existing containers in place",
	}
}

// fixesGrowthDisabledOverProvision: growth disabled and a T0-only cover would over-provision.
func fixesGrowthDisabledOverProvision(t0 int) []string {
	return []string{
		"enable enableDynamicDriveScalingForSharedDrives to grow existing containers in place",
		fmt.Sprintf("or set clusterCapacity to a value the %d GiB failure-domain size divides evenly", t0),
	}
}

// fixesGrowTooSmall: an in-place grow is available but below minGrowthFraction.
func fixesGrowTooSmall(extraNodes, t0 int, minGrowthFraction float64) []string {
	return []string{
		fmt.Sprintf("add %d more node(s) not already running this pool's drive container", extraNodes),
		fmt.Sprintf("raise clusterCapacity by at least one %d GiB failure-domain chunk", t0),
		fmt.Sprintf("or lower minGrowthFraction (currently %.2f)", minGrowthFraction),
	}
}

// fixesAddCapacity: generic "add nodes or lower clusterCapacity" for the uniform-increase dead end.
func fixesAddCapacity(p poolKind) []string {
	return []string{
		fmt.Sprintf("add more nodes (or nodes with more free capacity/cores/hugepages/memory) that can host a %s drive container", p),
		"or lower clusterCapacity",
	}
}

// fixesCompute: a compute-sizing infeasibility (not enough compute nodes / FDs / resources).
func fixesCompute() []string {
	return []string{
		"add compute-eligible nodes (matching the cluster's compute role selector) with free cores + hugepages",
		"or lower computeContainers / computeCores, or reduce clusterCapacity so fewer TLC drive cores are needed",
	}
}

// fixesDriveCoresAboveDriveCount: a pinned dynamicTemplate.driveCores exceeds the node's physical
// full-drive count (full drives allow at most one core per device).
func fixesDriveCoresAboveDriveCount(numDrives int) []string {
	return []string{
		fmt.Sprintf("lower dynamicTemplate.driveCores to at most %d — the node's signed full-drive count", numDrives),
		"or drop the pin so the operator derives one core per drive, per node",
		"or switch to a drive-sharing mode (containerCapacity or clusterCapacity) to run more cores than physical drives",
	}
}

// fixesAutoFullDrivesNodeFit: one or more nodes cannot host a drive container sized for their own signed
// full drives, which in auto-full-drives mode fails the whole plan (drives are never dropped to fit).
// growthNodes names the subset (possibly all, possibly none) whose failure is a growth rather than a create:
// when non-empty, a lead tip names the cross-reconcile hazard — a compute container this cluster placed while
// the drive container was still small can be holding the room it now needs — since deleting that container is
// the direct fix and cheaper to try than the general remedies that follow. With no growth nodes, the catalog
// is exactly today's: pinning driveCores lower leads, since it keeps every drive and costs no capacity at all.
func fixesAutoFullDrivesNodeFit(nodes, growthNodes []string) []string {
	list := listNodesCapped(nodes, fixesAutoFullDrivesMaxNamedNodes)

	fixes := make([]string, 0, 5)
	if len(growthNodes) > 0 {
		glist := listNodesCapped(growthNodes, fixesAutoFullDrivesMaxNamedNodes)
		fixes = append(fixes, fmt.Sprintf(
			"delete this cluster's compute container on %s and let the operator re-place it — compute cores "+
				"and hugepages only ever rise, so a container placed while the drive container was smaller keeps "+
				"the room the growth now needs; the next reconcile grows the drive container first and sizes the "+
				"replacement against what is left. Do one node at a time. If weka refuses the deactivation "+
				"because active compute would drop too low, add capacity elsewhere first — a new container on a "+
				"spare compute-eligible node, or grow an existing one and recreate its pod so the extra cores "+
				"actually become active — then retry.", glist))
	}
	return append(fixes,
		"pin dynamicTemplate.driveCores lower — drives are decoupled from cores, so a lower pin keeps every "+
			"drive on every node and simply runs them on fewer cores",
		fmt.Sprintf("or free physical CPU / hugepages / memory on %s (evict other pods, raise the node's "+
			"hugepages reservation)", list),
		fmt.Sprintf("or take those nodes out of the drive role — narrow spec.roleNodeSelector.drive so it no "+
			"longer matches %s, or unsign their drives — so the plan is not required to place a container there", list),
		"or switch to a drive-sharing mode (containerCapacity or clusterCapacity), which sizes containers from "+
			"a capacity target instead of each node's full drive set",
	)
}

// fixesAutoFullDrivesCompute: compute cannot be sized/placed in auto-full-drives mode. Every drive is
// claimed, so total capacity is fixed by the fleet and the capacity-based share of each compute
// container's hugepages scales with it — the remedies are about that coefficient, the divisor (compute
// node count), or claiming less capacity. There is no computeContainers lever here.
func fixesAutoFullDrivesCompute(cons *CapacityConstraints) []string {
	fixes := []string{
		"add compute-eligible nodes (matching spec.roleNodeSelector.compute) with free cores + hugepages — " +
			"the capacity-based share of compute hugepages is divided by the compute container count, so more " +
			"compute nodes is the direct lever",
	}
	if cons != nil && cons.ComputeHugepagesTlcRatio > 0 {
		fixes = append(fixes, fmt.Sprintf(
			"or raise the hugepagesTlcRatio Helm value (currently %d — each compute container asks for "+
				"clusterTlcGiB*1024/%d MiB divided by the container count)",
			cons.ComputeHugepagesTlcRatio, cons.ComputeHugepagesTlcRatio))
	}
	if cons != nil && cons.ComputeMaxHugepagesMiB > 0 {
		fixes = append(fixes, fmt.Sprintf(
			"or lower the computeMaxHugepagesMiB Helm value (currently %d MiB) to cap what one compute container "+
				"may request", cons.ComputeMaxHugepagesMiB))
	}
	return append(fixes,
		"or pin dynamicTemplate.driveCores lower — the planner will NOT reduce drive cores on its own to make "+
			"compute fit, so this is the operator's lever, and it costs no drives at all: every drive stays "+
			"claimed, on fewer cores, and the compute requirement falls with the ratio",
		"or pin dynamicTemplate.numDrives lower so each node claims fewer drives — less claimed capacity means a "+
			"smaller compute hugepages bill",
		"or lower dynamicTemplate.computeCores if it is pinned")
}

// fixesNumDrivesAboveCount: a pinned dynamicTemplate.numDrives asks for more full drives than a node has
// signed. The pin is fleet-wide, so it must hold on the shortest eligible node.
func fixesNumDrivesAboveCount(pin, count int, node string) []string {
	return []string{
		fmt.Sprintf("lower dynamicTemplate.numDrives to at most %d — node %s has only that many signed full "+
			"drive(s), and the pin applies to every eligible node", count, node),
		"or unset numDrives so each node claims every full drive it has signed, however many that is",
		fmt.Sprintf("or sign %d more full drive(s) on %s, or take it out of spec.roleNodeSelector.drive",
			pin-count, node),
	}
}
