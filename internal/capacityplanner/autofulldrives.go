package capacityplanner

import (
	"fmt"
	"sort"
)

// autofulldrives.go implements the "auto full drives" capacity mode: the cluster acts as a daemonset, one
// drive container per eligible node claiming all of that node's signed non-blocked full drives (or exactly
// dynamicTemplate.numDrives of the largest, when set). Cores are decoupled from drives: the driveCores pin,
// else min(drives, MaxCoresPerContainer). If any node cannot host the container its own drives imply, the
// whole plan is infeasible — drives are never dropped and drive cores never traded away to fit compute.
// Growth reads a node's live drive total as OwnDriveCapacitiesGiB (allocated) + DriveCapacitiesGiB (free).

// autoNode is one candidate node with everything the walk needs about it, resolved once up front.
type autoNode struct {
	nc NodeCapacity
	// existing is this cluster's drive container on the node, or nil when there is none yet.
	existing *ExistingContainer
	// free is the node's unallocated drives; all is own+free — both descending, which is what makes a
	// numDrives pin take the largest drives.
	free []int
	all  []int
}

// autoFullDrivesNodes indexes the inventory into name-sorted autoNodes, so Create/Grow/Warnings ordering is
// deterministic and a fleet always plans the same way twice.
func autoFullDrivesNodes(existingDrives []ExistingContainer, inventory []NodeCapacity) []autoNode {
	byNode := make(map[string]*ExistingContainer, len(existingDrives))
	for i := range existingDrives {
		if existingDrives[i].Node != "" {
			byNode[existingDrives[i].Node] = &existingDrives[i]
		}
	}
	nodes := make([]autoNode, 0, len(inventory))
	for i := range inventory {
		nc := inventory[i]
		nodes = append(nodes, autoNode{
			nc:       nc,
			existing: byNode[nc.NodeName],
			free:     SortDriveCapacitiesDesc(nc.DriveCapacitiesGiB),
			all: SortDriveCapacitiesDesc(
				append(append([]int(nil), nc.OwnDriveCapacitiesGiB...), nc.DriveCapacitiesGiB...)),
		})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].nc.NodeName < nodes[j].nc.NodeName })
	return nodes
}

// PlanAutoFullDrives computes the auto-full-drives plan: one drive container per eligible drive-having node,
// grown in place where one already exists, plus the compute layout the resulting drive cores require, from
// the full-drives inventory view (inventory.FullDrivesInventory). A single pass — each node's size follows
// directly from its own drives and pins; a fleet that cannot host the result is reported infeasible.
func PlanAutoFullDrives(
	desired AutoFullDrivesDesired,
	existingDrives []ExistingContainer,
	existingCompute []ExistingComputeContainer,
	inventory []NodeCapacity,
	computeNodes map[string]bool,
	cons *CapacityConstraints,
) CapacityPlan {
	nodes := autoFullDrivesNodes(existingDrives, inventory)
	plan, totals, remaining := planAutoFullDrivesDrives(desired, nodes, cons)

	// Set above the feasibility gate so an infeasible plan can still report the core demand its claimed drives
	// imply, which is what DriveSizingRationale narrates: an infeasible plan creates nothing, so its Grow/Create
	// legs are empty and deriving the figure from them would pair "N of N drives would be claimed" with a
	// phantom zero core demand. totals.driveCoresTaken already charges every node exactly what its own Grow or
	// Create entry would (frozen for an unscheduled container, never trimmed by a failed fit), so it is one
	// basis for both outcomes.
	plan.TotalTlcDriveCores = totals.driveCoresTaken
	plan.RequiredComputeCores = RequiredComputeCores(plan.TotalTlcDriveCores, 0, true, cons)

	if plan.Infeasible == "" {
		// Compute is sized against the FINAL drive state (existing containers as grown above), so one reconcile
		// converges rather than trailing a pass behind.
		planComputeAutoFullDrives(&autoComputeInput{
			desired:            desired,
			existing:           existingCompute,
			computeNodes:       computeNodes,
			remaining:          remaining,
			totalTlcGiB:        totals.tlcGiBTaken,
			existingDriveCount: len(existingDrives),
			cons:               cons,
		}, &plan)
	}

	plan.DriveSizing = buildDriveSizingRationale(&plan, desired, totals, cons)
	return plan
}

// planAutoFullDrivesDrives is the node walk: one deterministic pass sizing each node's container from its
// own drives, gating it on node fit, and charging what it accepts. Create and growth share one path — a
// create is a growth from the zero footprint, so the ratchet and fit charge are each written once. Returns
// the per-node headroom left over, which is what the compute step sizes against.
func planAutoFullDrivesDrives(
	desired AutoFullDrivesDesired, nodes []autoNode, cons *CapacityConstraints,
) (CapacityPlan, autoFullDrivesTotals, map[string]NodeCapacity) {
	plan := CapacityPlan{}
	var totals autoFullDrivesTotals

	remaining := make(map[string]NodeCapacity, len(nodes))
	for i := range nodes {
		remaining[nodes[i].nc.NodeName] = nodes[i].nc
	}

	// Both collected across the whole walk and reported after it: stranding because its cause is one
	// fleet-wide pin, failures so the infeasibility names every offending node rather than the first.
	var stranded []strandedNode
	var failures []autoFitFailure

	for i := range nodes {
		n := &nodes[i]
		name := n.nc.NodeName

		if len(n.free) == 0 && len(n.all) == 0 {
			continue // nothing signed on this node at all
		}

		// A create may only consider free drives. A node can report own drives with no `existing` entry — a
		// container mid-deletion, or one with no node affinity — and sizing for drives something else still
		// holds would double-allocate them.
		drives := n.free
		if n.existing != nil {
			drives = n.all
		}

		if n.existing == nil {
			// Ineligible (cordoned/not ready/untolerated taint) nodes never start a new container — only a
			// node already hosting one keeps it. Its free signed drives still swell the fleet denominator
			// (drivesAvailable/tlcGiBAvailable) so "N of M" reflects every signed drive, but never the
			// numerator since nothing is actually taken here.
			if n.nc.IneligibleReason != "" {
				totals.drivesAvailable += len(drives)
				totals.tlcGiBAvailable += sumInts(drives)
				plan.Warnings = append(plan.Warnings, nodeWarning(WarningKindNodeIneligible, name,
					"auto full drives: node %s has %d signed free full drive(s) but is ineligible for a new "+
						"drive container (%s)",
					name, len(drives), n.nc.IneligibleReason))
				continue
			}
			// The one per-node skip that is a skip rather than an infeasibility: it clears itself, so failing
			// the plan would stall every reconcile behind one deletion.
			if n.nc.HasDeletingDriveContainer {
				plan.Warnings = append(plan.Warnings, nodeWarning(WarningKindTransient, name,
					"auto full drives: node %s still hosts a this-cluster drive container that is being deleted — "+
						"skipping new container placement this pass (retried automatically once deletion completes)",
					name))
				continue
			}
			if len(drives) == 0 {
				continue // own drives, but nothing free and no container of ours to grow
			}
		}

		np, report := autoSizeNode(name, drives, desired, cons)
		if report != nil {
			setInfeasible(&plan, report)
			return plan, totals, remaining
		}

		// The never-shrink ratchet, written once. A nil existing reads as the zero footprint, so max() yields
		// the derived size and the create path falls out of the same two lines.
		var cur autoFootprint
		curTlcGiB := 0
		if n.existing != nil {
			cur = autoFootprint{cores: n.existing.NumCores, drives: n.existing.NumDrives}
			curTlcGiB = n.existing.TlcGiB
		}
		to := autoFootprint{cores: max(cur.cores, np.cores), drives: max(cur.drives, np.numDrives())}
		newTlcGiB := max(curTlcGiB, np.tlcGiB())

		// An unscheduled container's cores charge at their frozen value here, matching that its Grow entry
		// never gets written below — the ratcheted `to.cores` is what growth would apply, not what is
		// actually taken while the pod sits unscheduled.
		chargedCores := to.cores
		if n.existing != nil && n.existing.Unscheduled {
			chargedCores = cur.cores
		}

		totals.drivesTaken += to.drives
		totals.drivesAvailable += len(drives)
		totals.tlcGiBTaken += newTlcGiB
		totals.tlcGiBAvailable += sumInts(drives)
		totals.driveCoresTaken += chargedCores

		// Holding fewer drives than the node offers can only be a numDrives pin — the core cap bounds cores,
		// never drives. Measured against the ratcheted count, since a freshly lowered pin can leave a
		// container holding more than the pin asks, and calling those stranded would be false.
		if to.drives < len(drives) {
			stranded = append(stranded, strandedNode{node: name, signed: len(drives), used: to.drives})
		}

		// An unscheduled pod holds no node resources to grow into, and raising its spec would only make it
		// harder to schedule. Skipped after the totals so the fleet accounting still reflects its drives, and
		// before the fit so it cannot manufacture an infeasibility.
		if n.existing != nil && n.existing.Unscheduled {
			plan.Warnings = append(plan.Warnings, nodeWarning(WarningKindTransient, name,
				"auto full drives: node %s hosts a drive container whose pod has not been scheduled yet — "+
					"skipping growth this pass (retried automatically once the pod is scheduled)",
				name))
			continue
		}

		fit := autoNodeFit(&n.nc, cur, to, cons)
		if !fit.ok {
			kind := "create"
			if n.existing != nil {
				kind = "growth"
			}
			failures = append(failures, autoFitFailure{
				node: name, kind: kind, numDrives: to.drives, toCores: to.cores, fit: fit,
			})
			continue
		}

		switch {
		case n.existing == nil:
			plan.Create = append(plan.Create, NewContainer{
				Node:      name,
				FDValue:   n.nc.FDValue,
				TlcGiB:    newTlcGiB,
				NumCores:  to.cores,
				NumDrives: to.drives,
				Type:      DriveTypeTLC,
			})
		case to.drives > cur.drives || to.cores > cur.cores:
			plan.Grow = append(plan.Grow, ContainerGrowth{
				Name:         n.existing.Name,
				NewTlcGiB:    newTlcGiB,
				NewCores:     to.cores,
				NewNumDrives: to.drives,
			})
		}
		// Charge the accepted delta so the compute step sizes against what is actually left. A steady-state
		// node charges an all-zero cost, so this needs no guard.
		nc := remaining[name]
		chargeFit(&nc, fit.cost)
		remaining[name] = nc
	}

	if len(stranded) > 0 {
		plan.Warnings = append(plan.Warnings, formatStrandedWarning(stranded, desired.NumDrives))
	}

	// The infeasibility gate: after the full walk so every offender is named, and before compute is sized, so
	// an infeasible plan carries no ComputeLayout. Partial Create/Grow entries stay on the plan for
	// diagnostics (the CLI labels them "PARTIAL — NOT applied") but nothing downstream applies them.
	if len(failures) > 0 {
		setInfeasible(&plan, autoNodeFitInfeasible(failures))
	}
	return plan, totals, remaining
}

// buildDriveSizingRationale composes the plan's drive-sizing accounting: what was claimed, and the compute
// the resulting drive cores imply. Populated on every return path, including infeasible ones — "48 of 48
// drives would be claimed, but the fleet is short" is the evidence that the mode did not quietly shrink.
func buildDriveSizingRationale(
	plan *CapacityPlan, desired AutoFullDrivesDesired, totals autoFullDrivesTotals, cons *CapacityConstraints,
) *DriveSizingRationale {
	r := &DriveSizingRationale{
		DrivesTaken:              totals.drivesTaken,
		DrivesAvailable:          totals.drivesAvailable,
		TlcGiBTaken:              totals.tlcGiBTaken,
		TlcGiBAvailable:          totals.tlcGiBAvailable,
		TotalTlcDriveCores:       plan.TotalTlcDriveCores,
		TotalQlcDriveCores:       plan.TotalQlcDriveCores,
		RequiredComputeCores:     plan.RequiredComputeCores,
		ComputeContainers:        plan.ComputeContainers,
		ComputeCoresPerContainer: plan.ComputeCores,
	}
	// Report the hugepages of a container that actually holds ComputeCoresPerContainer cores. The layout is
	// sorted by node, so its first entry need not be the one whose core count is being reported — a
	// heterogeneous layout would otherwise pair one container's cores with another's hugepages and read as
	// an arithmetic error. Falls back to the largest figure when no entry matches.
	for _, c := range plan.ComputeLayout {
		if c.NumCores == r.ComputeCoresPerContainer {
			r.ComputeHugepagesMiB = c.HugepagesMiB
			break
		}
		r.ComputeHugepagesMiB = max(r.ComputeHugepagesMiB, c.HugepagesMiB)
	}

	sizing := fmt.Sprintf("one core per drive (at most %d per container)", cons.MaxCoresPerContainer)
	if desired.DriveCores > 0 {
		sizing = fmt.Sprintf("the pinned dynamicTemplate.driveCores=%d", desired.DriveCores)
	}
	if plan.Infeasible != "" {
		// Names the drive-core figure the compute demand is derived from, so the sentence cannot read as
		// arithmetic that does not add up (claimed drives beside a core demand with no stated basis).
		r.Reason = fmt.Sprintf(
			"auto full drives: %d of %d drive(s) would be claimed (%d of %d GiB TLC) at %s, needing %d drive "+
				"core(s) and %d compute core(s) — but the plan is infeasible and nothing is created: %s",
			totals.drivesTaken, totals.drivesAvailable, totals.tlcGiBTaken, totals.tlcGiBAvailable,
			sizing, r.TotalTlcDriveCores, r.RequiredComputeCores, plan.Infeasible)
		return r
	}
	r.Reason = fmt.Sprintf(
		"auto full drives: every eligible node claims its full drive set at %s — %d of %d drive(s) taken "+
			"(%d of %d GiB TLC), %d drive core(s) requiring %d compute core(s) across %d container(s)",
		sizing, totals.drivesTaken, totals.drivesAvailable, totals.tlcGiBTaken, totals.tlcGiBAvailable,
		plan.TotalTlcDriveCores, plan.RequiredComputeCores, plan.ComputeContainers)
	return r
}
