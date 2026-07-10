package capacityplanner

import (
	"fmt"
	"sort"
)

// infeasibility.go holds the structured infeasibility report that accompanies the free-text
// plan.Infeasible string. The report carries the binding cause, the per-node rejection breakdown and
// an ordered list of actionable fix tips. The tips live HERE (in the planner) so every consumer — the
// weka-capacity dry-run CLI and the controller's ClusterCapacityInfeasible event — shares one source
// of truth rather than re-deriving remediation advice from the message text.

// NodeRejection records why one candidate node cannot host a failure domain for a pool: the tightest
// binding dimension and the free-vs-needed capacity. It is the structured form of one entry in the
// rejectedNodesBreakdown message.
type NodeRejection struct {
	Node string
	// Binding is the dimension that caps the node below the minimum chunk, or "already hosts a <pool>
	// container" when the node is excluded because it already runs a pool-p drive container.
	Binding string
	// FreeGiB is the node's usable pool headroom (GiB); 0 when the pool's drive type is absent.
	FreeGiB int
	// NeededGiB is the per-FD floor the node must clear to be a candidate (the minimum chunk size).
	NeededGiB int
}

// InfeasibilityReport is the structured explanation for plan.Infeasible. Reason mirrors the free-text
// Infeasible string (kept for back-compat); the remaining fields let callers render the binding cause
// and actionable fixes without re-parsing the message.
type InfeasibilityReport struct {
	// Reason is the human summary; it is byte-identical to plan.Infeasible.
	Reason string
	// Pool is the pool the verdict is about: "tlc", "qlc", "compute", or "" (cluster-wide, e.g. the
	// protection-floor check).
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

// tag returns the lower-case pool tag ("tlc"/"qlc") used in the structured report's Pool field.
func (p poolKind) tag() string {
	if p == poolQLC {
		return DriveTypeQLC
	}
	return DriveTypeTLC
}

// setInfeasible records both the free-text reason (Infeasible, kept for back-compat) and the structured
// report on the plan. report.Reason is authoritative; plan.Infeasible mirrors it exactly.
func setInfeasible(plan *CapacityPlan, report *InfeasibilityReport) {
	plan.Infeasible = report.Reason
	plan.Infeasibility = report
}

// rejectedNodes returns the structured per-node breakdown that rejectedNodesBreakdown formats into
// text: one NodeRejection per node that is NOT a usable pool-p candidate, in sorted-name order and
// uncapped (the string formatter applies its own caps). Mirrors rejectedNodesBreakdown's classification
// exactly so the structured list and the message never disagree.
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
