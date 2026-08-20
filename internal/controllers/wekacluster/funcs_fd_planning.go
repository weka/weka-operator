package wekacluster

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/capacityplanner/inventory"
	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/pkg/util"
)

// protectionScheme resolves the effective protection scheme, falling back to Helm-level defaults. Mirrors
// clusterCapacityProtection/FormCluster so a defaulted cluster can't pass admission then deadlock as Infeasible.
func (r *wekaClusterReconcilerLoop) protectionScheme() capacityplanner.ProtectionScheme {
	sw, rl, hs := globalconfig.Config.DriveSharing.EffectiveProtection(
		r.cluster.Spec.StripeWidth, r.cluster.Spec.RedundancyLevel, r.cluster.Spec.HotSpare,
	)
	return capacityplanner.ProtectionScheme{
		StripeWidth:     sw,
		RedundancyLevel: rl,
		HotSpare:        hs,
	}
}

// planClusterCapacity is the entry point for clusterCapacity planning: it builds the desired per-pool
// target and per-node remaining headroom (net of ALL weka drive containers, not just this cluster's),
// runs capacityplanner.PlanCapacity, emits warning/shrink events, and returns a WaitError when infeasible.
func (r *wekaClusterReconcilerLoop) planClusterCapacity(ctx context.Context) (*capacityplanner.CapacityPlan, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "planClusterCapacity")
	defer logger.End()

	cluster := r.cluster
	s := r.protectionScheme()

	capGiB, err := cluster.Spec.Dynamic.GetClusterCapacityGiB()
	if err != nil {
		return nil, err
	}
	raw := capacityplanner.RawCapacityGiB(capGiB, s.StripeWidth, s.RedundancyLevel, s.HotSpare)
	tlcRaw, qlcRaw := weka.GetTlcQlcCapacity(raw, cluster.Spec.Dynamic.DriveTypesRatio)
	desired := capacityplanner.DesiredCapacity{
		TlcRawGiB:         tlcRaw,
		QlcRawGiB:         qlcRaw,
		ComputeContainers: cluster.Spec.Dynamic.ComputeContainers, // 0 == unset (auto-derive)
		ComputeCores:      cluster.Spec.Dynamic.ComputeCores,      // 0 == unset (auto-derive)
		DriveContainers:   cluster.Spec.Dynamic.DriveContainers,   // 0 == unset (auto-derive)
		DriveCores:        cluster.Spec.Dynamic.DriveCores,        // 0 == unset (auto-derive)
	}

	// Per-role DPDK/cpuPolicy overrides so the planner's fit gates reserve hugepages/CPU exactly as the
	// scheduler will (container_factory.go builds cluster containers with cluster.Spec.CpuPolicy).
	cons := allocator.ConstraintsForClusterSpec(&cluster.Spec)

	// Transient-churn guard: while a drive container is unscheduled (pod being (re)created), the live
	// failure-domain set is temporarily reduced, and planning against that snapshot would wrongly
	// concentrate capacity onto the survivors. Defer here; the reconcile retries once pods settle.
	if name, transient := firstUnscheduledDriveContainer(r.containers); transient {
		r.emitPlannerEvent(reasonClusterCapacityDeferred,
			fmt.Sprintf("deferring clusterCapacity planning: drive container %s is unscheduled (pod (re)scheduling); will retry once it settles", name))
		logger.Debug("deferring clusterCapacity planning while a drive container is transiently unscheduled", "container", name)
		return r.noopCapacityPlan(ctx, cons), nil
	}

	// Steady-state fast path: if existing healthy drive containers already cover the desired capacity and
	// compute needs no change, skip the expensive node-inventory rebuild and return a no-op plan — any
	// capacity loss drops us below desired and re-engages the full plan below, so correctness self-heals.
	if plan, skip := r.steadyStatePlan(ctx, desired, s, cons); skip {
		return plan, nil
	}

	// buildNodeInventoryFn is a test seam overriding only the node-listing step (nil in production, where
	// the shared inventory.Collector is used — also the source for the weka-capacity dry-run CLI).
	buildInventory := r.buildNodeInventoryFn // test seam (nil in production)
	if buildInventory == nil {
		col := inventory.NewCollector(r.getClient())
		buildInventory = func(ctx context.Context) (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
			return col.NodeInventory(ctx, cluster, r.containers, cons)
		}
	}
	fdByNode, nodeInv, computeNodes, err := buildInventory(ctx)
	if err != nil {
		return nil, err
	}
	existingDrives := inventory.ExistingDrives(ctx, cluster, r.containers, fdByNode)
	existingCompute := inventory.ExistingCompute(ctx, r.containers)

	plan := capacityplanner.PlanCapacity(desired, s, existingDrives, existingCompute, nodeInv, computeNodes, cons)

	logger.Info("clusterCapacity plan",
		"desiredTlcGiB", desired.TlcRawGiB, "desiredQlcGiB", desired.QlcRawGiB,
		"minFdNum", s.MinFdNum(), "candidateNodes", len(nodeInv), "existingDrives", len(existingDrives),
		"grow", len(plan.Grow), "create", len(plan.Create),
		"infeasible", plan.Infeasible)
	for _, n := range nodeInv {
		logger.Debug("clusterCapacity node headroom", "node", n.NodeName, "fd", n.FDValue,
			"tlcGiB", n.TlcGiB, "qlcGiB", n.QlcGiB, "cores", n.AllocatableCPU,
			"hugepagesMiB", n.AvailableHugepagesMiB, "memoryMiB", n.AvailableMemoryMiB)
	}

	// An infeasible plan is the sole signal: emit only ClusterCapacityInfeasible and return, skipping
	// the shrink/heterogeneous-growth/over-provision advisories (they would just be noise on a plan
	// that creates/grows nothing).
	if plan.Infeasible != "" {
		r.emitPlannerEvent(reasonClusterCapacityInfeasible, plan.Infeasible)
		return nil, lifecycle.NewWaitErrorWithDuration(fmt.Errorf("clusterCapacity infeasible: %s", plan.Infeasible), time.Minute)
	}
	for _, msg := range plan.ShrinkEvents {
		r.emitPlannerEvent(reasonClusterCapacityShrink, msg)
	}
	// clusterCapacity uses a single reason for all its warnings (layout advisories); only the message is
	// read from the classified Warning. Auto full drives instead splits by cause (autoFullDrivesWarningReason).
	for _, w := range plan.Warnings {
		r.emitPlannerEvent(reasonClusterCapacityHeterogeneousGrowth, w.Message)
	}
	for _, msg := range plan.OverProvisions {
		r.emitPlannerEvent(reasonClusterCapacityOverProvisioned, msg)
	}
	// Feasible plan that places capacity: emit a Normal summary event, gated on Create/Grow so steady-state
	// reconciles stay silent.
	if len(plan.Create) > 0 || len(plan.Grow) > 0 {
		r.emitPlannerEvent(reasonClusterCapacityPlanned,
			formatCapacityPlanSummary(&plan, desired, s, existingDrives))
	}
	return &plan, nil
}

// planAutoFullDrives is the entry point for auto-full-drives planning: one node-pinned container per
// drive-role node, sized from that node's own signed drives (the opposite of clusterCapacity's uniform
// whole-cluster target). It has neither a steady-state short-circuit nor a transient-churn guard: its
// containers are pinned regardless of scheduling state, and it plans against the fleet's own drives.
func (r *wekaClusterReconcilerLoop) planAutoFullDrives(ctx context.Context) (*capacityplanner.CapacityPlan, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "planAutoFullDrives")
	defer logger.End()

	cluster := r.cluster
	// An omitted spec.dynamicTemplate is this mode — the shortest way to ask for it — so a nil here is the
	// common case, not an edge one, and every pin below reads as unset.
	dyn := cluster.Spec.Dynamic
	if dyn == nil {
		dyn = &weka.WekaClusterTemplate{}
	}

	// No ComputeContainers/DriveContainers fields here: under the both-or-neither CEL rule, reaching this
	// planning path already means both are unset on dyn — see AutoFullDrivesDesired's doc comment.
	desired := capacityplanner.AutoFullDrivesDesired{
		ComputeCores: dyn.ComputeCores, // 0 == unset (auto-derive)
		DriveCores:   dyn.DriveCores,   // 0 == unset (auto-derive)
		NumDrives:    dyn.NumDrives,    // 0 == unset (take every signed drive)
	}

	cons := allocator.ConstraintsForClusterSpec(&cluster.Spec)

	// Full-drives inventory reads a disjoint annotation from NodeInventory (see FullDrivesInventory).
	buildInventory := r.buildFullDrivesInventoryFn // test seam (nil in production)
	if buildInventory == nil {
		col := inventory.NewCollector(r.getClient())
		buildInventory = func(ctx context.Context) (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
			return col.FullDrivesInventory(ctx, cluster, r.containers, cons)
		}
	}
	fdByNode, nodeInv, computeNodes, err := buildInventory(ctx)
	if err != nil {
		return nil, err
	}

	// nodeInv also carries compute-selector nodes with no drives (for compute headroom), so a bare
	// len(nodeInv)==0 check is wrong — check for a node actually carrying drives. "Signed" means own OR
	// free: on a fully-converged cluster every drive is owned, so a free-only test would misreport a
	// healthy cluster as unsigned.
	hasSignedDrives := false
	for i := range nodeInv {
		n := &nodeInv[i]
		if len(n.DriveCapacitiesGiB) > 0 || len(n.OwnDriveCapacitiesGiB) > 0 {
			hasSignedDrives = true
			break
		}
	}
	if !hasSignedDrives {
		r.emitPlannerEvent(reasonAutoFullDrivesNoSignedDrives,
			"deferring auto full drives planning: no node matching the drive-role selector has any signed, non-blocked full drive yet; sign drives (weka.io/weka-full-drives) and the operator will pick them up on its own")
		logger.Debug("deferring auto full drives planning: no node has signed full drives yet", "candidateNodes", len(nodeInv))
		return nil, lifecycle.NewWaitErrorWithDuration(fmt.Errorf("auto full drives: no node has signed full drives yet"), time.Minute)
	}

	existingDrives := inventory.ExistingDrives(ctx, cluster, r.containers, fdByNode)
	existingCompute := inventory.ExistingCompute(ctx, r.containers)

	plan := capacityplanner.PlanAutoFullDrives(desired, existingDrives, existingCompute, nodeInv, computeNodes, cons)

	// Drive-core totals for the log line: DriveSizing is populated by PlanAutoFullDrives on every return
	// path, but the nil-guard keeps this log statement safe even if that invariant ever slips. There are
	// no cap/attempt fields to report — drive cores are min(driveCount, maxCoresPerContainer) or the pin,
	// and are never traded down to fit compute, so there is no search and nothing to explain.
	var dsDrivesTaken, dsDrivesAvailable, dsDriveCores int
	if ds := plan.DriveSizing; ds != nil {
		dsDrivesTaken, dsDrivesAvailable, dsDriveCores = ds.DrivesTaken, ds.DrivesAvailable, ds.TotalTlcDriveCores
	}
	logger.Info("auto full drives plan",
		"candidateNodes", len(nodeInv), "existingDrives", len(existingDrives), "create", len(plan.Create),
		"infeasible", plan.Infeasible,
		"drivesTaken", dsDrivesTaken, "drivesAvailable", dsDrivesAvailable, "driveCores", dsDriveCores)
	for i := range nodeInv {
		n := &nodeInv[i]
		logger.Debug("auto full drives node headroom", "node", n.NodeName, "driveCapacitiesGiB", n.DriveCapacitiesGiB,
			"cores", n.AllocatableCPU, "hugepagesMiB", n.AvailableHugepagesMiB, "memoryMiB", n.AvailableMemoryMiB)
	}

	// An infeasible plan is the sole signal: emit only AutoFullDrivesInfeasible and return, skipping the
	// warnings advisory (it would just be noise on a plan that creates nothing).
	if plan.Infeasible != "" {
		r.emitPlannerEvent(reasonAutoFullDrivesInfeasible, plan.Infeasible)
		return nil, lifecycle.NewWaitErrorWithDuration(fmt.Errorf("auto full drives infeasible: %s", plan.Infeasible), time.Minute)
	}
	// Drive cores are never traded away to make compute fit — a fleet that cannot host the required
	// compute is infeasible above, not silently converged at a smaller core count.
	//
	// plan.Grow is not announced here: applyPlannerDriveGrowth can decline an entry or fail its Update,
	// and emits AutoFullDrivesGrowthDetected for what it actually wrote.

	// One reason per cause: each Warning here is already an aggregate naming every node it affects, so one
	// event per warning is one event per condition, not per node.
	for _, w := range plan.Warnings {
		r.emitPlannerEvent(autoFullDrivesWarningReason(w.Kind), w.Message)
	}
	// Gated on Create only: plan.Grow is applied separately by applyPlannerDriveGrowth, whose caller emits
	// own cluster-level AutoFullDrivesGrowthDetected and per-container CapacityGrowthApplied events.
	if len(plan.Create) > 0 {
		r.emitPlannerEvent(reasonAutoFullDrivesPlanned, formatAutoFullDrivesPlanSummary(&plan))
	}
	return &plan, nil
}

// formatAutoFullDrivesPlanSummary renders a one-line summary of a feasible auto-full-drives plan's Create
// leg for the AutoFullDrivesPlanned event; plan.Grow gets its own CapacityGrowthApplied event from
// announceDriveGrowth instead. Simpler than formatCapacityPlanSummary since auto full drives has no
// TLC/QLC-ratio target or protection scheme.
func formatAutoFullDrivesPlanSummary(plan *capacityplanner.CapacityPlan) string {
	nodes := map[string]struct{}{}
	var placedGiB int
	for _, c := range plan.Create {
		nodes[c.Node] = struct{}{}
		placedGiB += c.TlcGiB + c.QlcGiB
	}
	summary := fmt.Sprintf("auto full drives plan applied: creating %d drive container(s) across %d node(s), placing %s",
		len(plan.Create), len(nodes), util.HumanReadableGiB(placedGiB))
	if len(plan.ComputeLayout) > 0 {
		computeNodes := map[string]struct{}{}
		var totalCores int
		for _, c := range plan.ComputeLayout {
			computeNodes[c.Node] = struct{}{}
			totalCores += c.NumCores
		}
		summary += fmt.Sprintf("; compute %d container(s), %d cores on %d node(s)",
			len(plan.ComputeLayout), totalCores, len(computeNodes))
	}
	// Always append the rationale when there is one — it states what was planned.
	if ds := plan.DriveSizing; ds != nil && ds.Reason != "" {
		summary += "; " + ds.Reason
	}
	return summary
}

// formatCapacityPlanSummary renders a one-line summary of a feasible clusterCapacity plan for the
// ClusterCapacityPlanned event: create breakdown by pool type, per-FD chunk and placed capacity; the grow
// leg's added capacity and from→to cores (vs existingDrives); compute spread; minFdNum; and
// placed-vs-target raw capacity/protection.
func formatCapacityPlanSummary(plan *capacityplanner.CapacityPlan, desired capacityplanner.DesiredCapacity, s capacityplanner.ProtectionScheme, existingDrives []capacityplanner.ExistingContainer) string {
	var parts []string

	// --- Create leg: type breakdown, per-FD chunk, and capacity placed ---
	var createTlc, createQlc int
	if len(plan.Create) > 0 {
		nodes := map[string]struct{}{}
		fds := map[string]struct{}{}
		var mixed, tlcOnly, qlcOnly int
		for _, c := range plan.Create {
			nodes[c.Node] = struct{}{}
			if c.FDValue != "" {
				fds[c.FDValue] = struct{}{}
			}
			createTlc += c.TlcGiB
			createQlc += c.QlcGiB
			switch {
			case c.TlcGiB > 0 && c.QlcGiB > 0:
				mixed++
			case c.QlcGiB > 0:
				qlcOnly++
			default:
				tlcOnly++
			}
		}
		fdCount := len(fds)
		// Fold the type into the noun when creates are homogeneous ("3 QLC drive container(s)"); the
		// bracketed breakdown only appears when types are mixed, so it never merely restates the count.
		var create string
		if mixedKinds := nonZeroKinds(mixed, tlcOnly, qlcOnly); len(mixedKinds) == 1 {
			create = fmt.Sprintf("creating %d %s drive container(s) across %d node(s) / %d failure domain(s)",
				len(plan.Create), soleKindLabel(tlcOnly, qlcOnly), len(nodes), fdCount)
		} else {
			create = fmt.Sprintf("creating %d drive container(s) [%s] across %d node(s) / %d failure domain(s)",
				len(plan.Create), strings.Join(mixedKinds, ", "), len(nodes), fdCount)
		}
		if fdCount > 0 {
			create += fmt.Sprintf(" @ ~%s/FD", util.HumanReadableGiB((createTlc+createQlc)/fdCount))
		}
		create += fmt.Sprintf(", placing %s", util.FormatTlcQlcColumn(createTlc, createQlc))
		parts = append(parts, create)
	}

	// --- Grow leg: added capacity and from→to cores (vs existingDrives) ---
	var growTlc, growQlc int
	if len(plan.Grow) > 0 {
		oldByName := make(map[string]capacityplanner.ExistingContainer, len(existingDrives))
		for _, e := range existingDrives {
			oldByName[e.Name] = e
		}
		var addedTlc, addedQlc, addedCores int
		uniformCores := true
		var fromCores, toCores int
		shown := 0 // Grow entries with a matching existingDrives record (the only ones we can describe)
		for _, g := range plan.Grow {
			old, ok := oldByName[g.Name]
			if !ok {
				// A Grow always corresponds to an existing container, so a miss is a logic error. Skip it
				// rather than subtract a zero baseline, which would inflate the reported added cores/capacity.
				continue
			}
			addedTlc += g.NewTlcGiB - old.TlcGiB
			addedQlc += g.NewQlcGiB - old.QlcGiB
			addedCores += g.NewCores - old.NumCores
			if shown == 0 {
				fromCores, toCores = old.NumCores, g.NewCores
			} else if old.NumCores != fromCores || g.NewCores != toCores {
				uniformCores = false
			}
			shown++
		}
		if shown > 0 {
			growTlc, growQlc = addedTlc, addedQlc
			grow := fmt.Sprintf("growing %d existing container(s) (+%s", shown, util.FormatTlcQlcColumn(addedTlc, addedQlc))
			if uniformCores {
				grow += fmt.Sprintf(", cores %d→%d)", fromCores, toCores)
			} else {
				grow += fmt.Sprintf(", cores +%d)", addedCores)
			}
			parts = append(parts, grow)
		}
	}

	// --- Compute leg: container count, total cores, node spread ---
	if len(plan.ComputeLayout) > 0 {
		computeNodes := map[string]struct{}{}
		var totalCores int
		for _, c := range plan.ComputeLayout {
			computeNodes[c.Node] = struct{}{}
			totalCores += c.NumCores
		}
		parts = append(parts, fmt.Sprintf("compute %d container(s), %d cores on %d node(s)",
			len(plan.ComputeLayout), totalCores, len(computeNodes)))
	} else if plan.ComputeContainers > 0 {
		computeNodeCount := plan.ComputeContainers
		if len(plan.ComputeNodes) > 0 {
			computeNodeCount = len(plan.ComputeNodes)
		}
		parts = append(parts, fmt.Sprintf("compute %d×%d cores on %d node(s)",
			plan.ComputeContainers, plan.ComputeCores, computeNodeCount))
	}

	placed := createTlc + createQlc + growTlc + growQlc
	return fmt.Sprintf("clusterCapacity plan applied: %s; minFdNum %d; target raw %s (placed %s), protection %d+%d+%d",
		strings.Join(parts, "; "),
		s.MinFdNum(),
		util.FormatTlcQlcColumn(desired.TlcRawGiB, desired.QlcRawGiB),
		util.HumanReadableGiB(placed),
		s.StripeWidth, s.RedundancyLevel, s.HotSpare)
}

// nonZeroKinds renders the "%d <type>" clauses for the create type breakdown, in the order mixed, TLC,
// QLC, omitting any type with no containers.
func nonZeroKinds(mixed, tlcOnly, qlcOnly int) []string {
	var kinds []string
	if mixed > 0 {
		kinds = append(kinds, fmt.Sprintf("%d mixed", mixed))
	}
	if tlcOnly > 0 {
		kinds = append(kinds, fmt.Sprintf("%d TLC", tlcOnly))
	}
	if qlcOnly > 0 {
		kinds = append(kinds, fmt.Sprintf("%d QLC", qlcOnly))
	}
	return kinds
}

// soleKindLabel returns the bare type label ("mixed"/"TLC"/"QLC") of whichever bucket is the only non-zero
// one. Callers must guarantee exactly one of the three is non-zero (the homogeneous create case).
func soleKindLabel(tlcOnly, qlcOnly int) string {
	switch {
	case tlcOnly > 0:
		return "TLC"
	case qlcOnly > 0:
		return "QLC"
	default:
		return "mixed"
	}
}

// firstUnscheduledDriveContainer returns the name of the first alive, capacity-bearing drive container
// with no scheduled node, or ok=false if none. Capacity is gauged via inventory.DriveContainerCapacities
// so a Spec.DriveCapacity/NumDrives-only container also forces deferral.
func firstUnscheduledDriveContainer(containers []*weka.WekaContainer) (string, bool) {
	for _, c := range containers {
		if c.Spec.Mode != weka.WekaContainerModeDrive {
			continue
		}
		if tlc, qlc := inventory.DriveContainerCapacities(c); tlc == 0 && qlc == 0 {
			continue // carries no planner capacity — its (un)scheduling cannot distort capacity math
		}
		if c.IsMarkedForDeletion() || c.IsDeletingState() || c.IsDestroyingState() {
			continue // leaving — do not let it stall planning
		}
		if c.Status.NodeAffinity == "" {
			return c.Name, true
		}
	}
	return "", false
}

// noopCapacityPlan builds a no-op CapacityPlan carrying this cluster's current compute sizing (from its
// existing healthy containers), mirroring steadyStatePlan's no-op so a deferral never disturbs compute downstream.
func (r *wekaClusterReconcilerLoop) noopCapacityPlan(ctx context.Context, cons *capacityplanner.CapacityConstraints) *capacityplanner.CapacityPlan {
	drv := summarizeDriveContainers(ctx, r.containers, cons)
	cmp := summarizeComputeContainers(ctx, r.containers)
	return &capacityplanner.CapacityPlan{
		ComputeContainers:  cmp.count,
		ComputeCores:       cmp.minCores,
		TotalTlcDriveCores: drv.totalTlcDriveCores,
	}
}

// driveCapacitySummary is the per-pool capacity (GiB) of this cluster's existing healthy drive
// containers plus the TLC drive-core total that drives the compute 1:1 ratio.
type driveCapacitySummary struct {
	tlcGiB             int
	qlcGiB             int
	totalTlcDriveCores int
}

// summarizeDriveContainers sums THIS cluster's existing HEALTHY drive containers per pool (from spec,
// matching inventory.ExistingDrives) plus their total TLC drive cores (matching the allocator's
// totalTlcDriveCores). It reads only r.containers (already cached owned objects) — no node listing.
func summarizeDriveContainers(ctx context.Context, containers []*weka.WekaContainer, cons *capacityplanner.CapacityConstraints) driveCapacitySummary {
	var sum driveCapacitySummary
	for _, c := range containers {
		if c.Spec.Mode != weka.WekaContainerModeDrive {
			continue
		}
		// Mirrors inventory.ExistingDrives, not utils.IsUnhealthy — the same rule expressed once so the
		// two functions cannot drift apart. See DriveContainerHoldsDrives.
		if !inventory.DriveContainerHoldsDrives(c) {
			continue
		}
		tlcGiB, qlcGiB := inventory.DriveContainerCapacities(c)
		sum.tlcGiB += tlcGiB
		sum.qlcGiB += qlcGiB
		sum.totalTlcDriveCores += capacityplanner.TlcDriveCores(tlcGiB, cons)
	}
	return sum
}

// computeCapacitySummary is the count and smallest per-container core size of this cluster's existing
// healthy compute containers — the basis for deciding whether compute can be left untouched.
type computeCapacitySummary struct {
	count      int
	minCores   int
	totalCores int
}

// summarizeComputeContainers counts THIS cluster's existing HEALTHY compute containers and the smallest
// NumCores among them (matching the healthy filter BuildMissingContainers / applyClusterCapacityComputeGrowth
// use). minCores is 0 when there are none. Reads only r.containers — no node listing.
func summarizeComputeContainers(ctx context.Context, containers []*weka.WekaContainer) computeCapacitySummary {
	var sum computeCapacitySummary
	for _, c := range containers {
		if c.Spec.Mode != weka.WekaContainerModeCompute {
			continue
		}
		if unhealthy, _, _ := utils.IsUnhealthy(ctx, c); unhealthy { //nolint:errcheck // intentional
			continue
		}
		if sum.count == 0 || c.Spec.NumCores < sum.minCores {
			sum.minCores = c.Spec.NumCores
		}
		sum.totalCores += c.Spec.NumCores
		sum.count++
	}
	return sum
}

// steadyStatePlan returns a no-op plan and skip=true when desired capacity is covered by existing healthy
// containers and compute needs no change, letting planClusterCapacity skip the node-inventory rebuild.
// skip=false when a pool is short or compute could grow; an over-provisioned pool still skips but emits ShrinkEvent.
func (r *wekaClusterReconcilerLoop) steadyStatePlan(ctx context.Context, desired capacityplanner.DesiredCapacity, s capacityplanner.ProtectionScheme, cons *capacityplanner.CapacityConstraints) (*capacityplanner.CapacityPlan, bool) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "steadyStatePlan")
	defer logger.End()

	drv := summarizeDriveContainers(ctx, r.containers, cons)
	if capacityplanner.CapacityShort(drv.tlcGiB, desired.TlcRawGiB, cons) ||
		capacityplanner.CapacityShort(drv.qlcGiB, desired.QlcRawGiB, cons) {
		return nil, false // a pool needs growth beyond the deadband → full plan places it
	}

	cmp := summarizeComputeContainers(ctx, r.containers)
	// fullDrives=false (clusterCapacity fast path): summarizeDriveContainers only carries the TLC figure
	// here, so the ratio term excludes QLC drive cores — any under-count can only force a full replan,
	// never wrongly skip one.
	requiredComputeCores := capacityplanner.RequiredComputeCores(drv.totalTlcDriveCores, 0, false, cons)
	if capacityplanner.ComputeLayoutWouldGrow(desired.ComputeContainers, desired.ComputeCores,
		requiredComputeCores, s.MinFdNum(), cons.MaxCoresPerContainer, cmp.count, cmp.minCores, cmp.totalCores) {
		return nil, false // compute may need to grow → full plan re-derives against real headroom
	}

	plan := &capacityplanner.CapacityPlan{
		ComputeContainers:    cmp.count,
		ComputeCores:         cmp.minCores,
		TotalTlcDriveCores:   drv.totalTlcDriveCores,
		RequiredComputeCores: requiredComputeCores,
	}
	// Over-provisioned pools: emit the shrink advisory (throttled, identical to the full path) but never
	// auto-shrink. Drives are still "covered", so we skip inventory.
	emitShrink := func(pool string, cur, want int) {
		// Suppress the advisory for an in-cap overage — the create-new-before-grow path over-provisions by
		// up to one uniform chunk on purpose (see capacityplanner.OverProvisionCapGiB).
		if cur-want <= capacityplanner.OverProvisionCapGiB(want, cons) {
			return
		}
		msg := fmt.Sprintf("%s capacity is over-provisioned by %d GiB (desired %d, current %d); delete WekaContainers manually to shrink — the operator never auto-shrinks",
			pool, cur-want, want, cur)
		_ = r.RecordEventThrottled(corev1.EventTypeNormal, "ClusterCapacityShrink", msg, time.Minute) //nolint:errcheck // best effort
	}
	emitShrink("TLC", drv.tlcGiB, desired.TlcRawGiB)
	emitShrink("QLC", drv.qlcGiB, desired.QlcRawGiB)

	logger.Debug("clusterCapacity covered by existing containers, skipping node inventory",
		"curTlcGiB", drv.tlcGiB, "curQlcGiB", drv.qlcGiB,
		"desiredTlcGiB", desired.TlcRawGiB, "desiredQlcGiB", desired.QlcRawGiB,
		"computeContainers", cmp.count, "computeCores", cmp.minCores, "totalTlcDriveCores", drv.totalTlcDriveCores)
	return plan, true
}
