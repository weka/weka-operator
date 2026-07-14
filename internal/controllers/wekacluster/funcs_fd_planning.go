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

	"github.com/weka/weka-operator/internal/capacityplanner/inventory"
	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/pkg/util"
)

// protectionScheme resolves the effective protection scheme, applying the per-cluster spec values
// when set and falling back to the Helm-level defaults (PROTECTION_STRIPE_WIDTH / _REDUNDANCY_LEVEL /
// _HOT_SPARE) otherwise. This mirrors the clusterCapacityProtection webhook and FormCluster so the
// capacity planner forms exactly what admission accepted (a clusterCapacity cluster relying on the
// defaults would otherwise pass admission but deadlock as Infeasible / divide-by-zero here).
func (r *wekaClusterReconcilerLoop) protectionScheme() allocator.ProtectionScheme {
	sw, rl, hs := globalconfig.Config.DriveSharing.EffectiveProtection(
		r.cluster.Spec.StripeWidth, r.cluster.Spec.RedundancyLevel, r.cluster.Spec.HotSpare,
	)
	return allocator.ProtectionScheme{
		StripeWidth:     sw,
		RedundancyLevel: rl,
		HotSpare:        hs,
	}
}

// planClusterCapacity is the single entry point for clusterCapacity planning. It resolves the desired
// per-pool target, builds the per-node remaining headroom (net of ALL weka drive containers — other
// clusters AND this cluster's own — so it is pure remaining capacity) via the shared inventory collector,
// the existing-container view, and runs the pure allocator.PlanCapacity. It emits warnings/shrink events,
// logs the decision, and returns a WaitError when the plan is infeasible so the reconcile retries.
func (r *wekaClusterReconcilerLoop) planClusterCapacity(ctx context.Context) (*allocator.CapacityPlan, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "planClusterCapacity")
	defer logger.End()

	cluster := r.cluster
	s := r.protectionScheme()

	capGiB, err := cluster.Spec.Dynamic.GetClusterCapacityGiB()
	if err != nil {
		return nil, err
	}
	raw := allocator.RawCapacityGiB(capGiB, s.StripeWidth, s.RedundancyLevel, s.HotSpare)
	tlcRaw, qlcRaw := weka.GetTlcQlcCapacity(raw, cluster.Spec.Dynamic.DriveTypesRatio)
	desired := allocator.DesiredCapacity{
		TlcRawGiB:         tlcRaw,
		QlcRawGiB:         qlcRaw,
		ComputeContainers: cluster.Spec.Dynamic.ComputeContainers, // 0 == unset (auto-derive)
		ComputeCores:      cluster.Spec.Dynamic.ComputeCores,      // 0 == unset (auto-derive)
		DriveContainers:   cluster.Spec.Dynamic.DriveContainers,   // 0 == unset (auto-derive)
		DriveCores:        cluster.Spec.Dynamic.DriveCores,        // 0 == unset (auto-derive)
	}

	cons := allocator.CapacityConstraintsFromConfig()
	// The drive/compute PODS request hugepages = base×cores + DPDK base memory×cores (added by
	// GetContainerHugepages). Feed the per-role DPDK base into the planner so its node-fit gate reserves
	// the same hugepages the scheduler will. Per-role, honoring cluster spec overrides.
	cons.DriveDpdkPerCoreMiB = utils.GetDpdkBaseMemoryMbByRole(&cluster.Spec, weka.WekaContainerModeDrive)
	cons.ComputeDpdkPerCoreMiB = utils.GetDpdkBaseMemoryMbByRole(&cluster.Spec, weka.WekaContainerModeCompute)
	// The drive/compute PODS reserve physical CPU = f(numCores, cpuPolicy, node HT); a data core costs 2
	// physical CPUs under dedicated_ht on an HT node. Feed the cluster's cpuPolicy (empty == auto) so the
	// planner's node-CPU gate projects fresh containers the same way. All cluster-built containers use
	// cluster.Spec.CpuPolicy (container_factory.go).
	cons.CpuPolicy = cluster.Spec.CpuPolicy

	// Transient-churn guard: while any of this cluster's drive containers is alive but momentarily
	// unscheduled (its pod is being (re)created — e.g. a mass `kubectl delete pod` during a grow), the
	// live failure-domain set is transiently reduced. Planning against that reduced snapshot would
	// grow-only concentrate the fixed total raw capacity onto the survivors and never recover. Defer
	// planning until the churn settles — the reconcile requeues and self-heals once the pods reschedule.
	// Containers that are marked-for-deletion/deleting/destroying are deliberately NOT counted here: they
	// are already excluded from the existing view and are going away, so planning proceeds and ignores
	// them rather than stalling forever.
	if name, transient := firstUnscheduledDriveContainer(r.containers); transient {
		_ = r.RecordEventThrottled(corev1.EventTypeNormal, "ClusterCapacityDeferred", //nolint:errcheck // best effort
			fmt.Sprintf("deferring clusterCapacity planning: drive container %s is unscheduled (pod (re)scheduling); will retry once it settles", name),
			time.Minute)
		logger.Debug("deferring clusterCapacity planning while a drive container is transiently unscheduled", "container", name)
		return r.noopCapacityPlan(ctx, cons), nil
	}

	// Steady-state fast path: if this cluster's existing healthy drive containers already cover the
	// desired per-pool capacity AND compute needs no change, there is nothing to place — return a no-op
	// plan WITHOUT rebuilding the expensive node inventory (which lists every candidate node and reads
	// each node's shared-drive annotation). Any change that reduces our current (container/node loss)
	// drops cur below desired and re-engages the full plan below, so correctness self-heals.
	if plan, skip := r.steadyStatePlan(ctx, desired, s, cons); skip {
		return plan, nil
	}

	// Node inventory + existing-container view come from the shared collector (also used by the
	// weka-capacity dry-run CLI). The buildNodeInventoryFn seam overrides only the node-listing step in
	// tests; the existing-drive/compute views are always derived from this cluster's own containers. The
	// collector (and its client) is constructed only when the seam is unset, so seam-driven tests need no
	// Manager/client.
	buildInventory := r.buildNodeInventoryFn // test seam (nil in production)
	if buildInventory == nil {
		col := inventory.NewCollector(r.getClient())
		buildInventory = func(ctx context.Context) (map[string]string, []allocator.NodeCapacity, map[string]bool, error) {
			return col.NodeInventory(ctx, cluster, r.containers, cons)
		}
	}
	fdByNode, nodeInv, computeNodes, err := buildInventory(ctx)
	if err != nil {
		return nil, err
	}
	existingDrives := inventory.ExistingDrives(ctx, cluster, r.containers, fdByNode)
	existingCompute := inventory.ExistingCompute(ctx, r.containers)

	plan := allocator.PlanCapacity(desired, s, existingDrives, existingCompute, nodeInv, computeNodes, cons)

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
		_ = r.RecordEventThrottled(corev1.EventTypeWarning, "ClusterCapacityInfeasible", plan.Infeasible, time.Minute) //nolint:errcheck // best effort
		return nil, lifecycle.NewWaitErrorWithDuration(fmt.Errorf("clusterCapacity infeasible: %s", plan.Infeasible), time.Minute)
	}
	for _, msg := range plan.ShrinkEvents {
		_ = r.RecordEventThrottled(corev1.EventTypeNormal, "ClusterCapacityShrink", msg, time.Minute) //nolint:errcheck // best effort
	}
	for _, msg := range plan.Warnings {
		_ = r.RecordEventThrottled(corev1.EventTypeWarning, "ClusterCapacityHeterogeneousGrowth", msg, time.Minute) //nolint:errcheck // best effort
	}
	for _, msg := range plan.OverProvisions {
		_ = r.RecordEventThrottled(corev1.EventTypeNormal, "ClusterCapacityOverProvisioned", msg, time.Minute) //nolint:errcheck // best effort
	}
	// Feasible plan that actually places capacity (creates/grows containers): emit a Normal event with a
	// plan summary so operators get a positive signal (e.g. after recovering from ClusterCapacityInfeasible
	// by adding a node). Gated on Create/Grow so steady-state reconciles stay silent; throttled to avoid
	// spam across the repeated reconciles while the new containers materialize.
	if len(plan.Create) > 0 || len(plan.Grow) > 0 {
		_ = r.RecordEventThrottled(corev1.EventTypeNormal, "ClusterCapacityPlanned", //nolint:errcheck // best effort
			formatCapacityPlanSummary(&plan, desired, s, existingDrives), time.Minute)
	}
	return &plan, nil
}

// formatCapacityPlanSummary renders a one-line human summary of a feasible clusterCapacity plan for the
// ClusterCapacityPlanned event. Beyond bare counts it reports: the create breakdown by pool type
// (mixed/TLC/QLC), the per-FD chunk and the capacity placed by creates; the grow leg's added capacity
// and from→to cores (looked up against existingDrives); the compute node spread; minFdNum; and the
// placed-vs-target raw capacity / protection.
func formatCapacityPlanSummary(plan *allocator.CapacityPlan, desired allocator.DesiredCapacity, s allocator.ProtectionScheme, existingDrives []allocator.ExistingContainer) string {
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
		// When the creates are all one type, fold the type into the noun ("creating 3 QLC drive
		// container(s)"); a bracketed breakdown only adds information when they span types ("[2 mixed,
		// 1 TLC]"), so "3 drive container(s) [3 QLC]" — which just restates the count — never appears.
		var create string
		if mixedKinds := nonZeroKinds(mixed, tlcOnly, qlcOnly); len(mixedKinds) == 1 {
			create = fmt.Sprintf("creating %d %s drive container(s) across %d node(s) / %d failure domain(s)",
				len(plan.Create), soleKindLabel(mixed, tlcOnly, qlcOnly), len(nodes), fdCount)
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
		oldByName := make(map[string]allocator.ExistingContainer, len(existingDrives))
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
func soleKindLabel(mixed, tlcOnly, qlcOnly int) string {
	switch {
	case tlcOnly > 0:
		return "TLC"
	case qlcOnly > 0:
		return "QLC"
	default:
		return "mixed"
	}
}

// firstUnscheduledDriveContainer returns the name of the first owned drive container that is alive
// (not marked-for-deletion / deleting / destroying) yet has no scheduled node — its pod is being
// (re)created (Status.NodeAffinity == ""). Containers that are leaving are skipped on purpose: they
// must not stall planning. Returns ok=false when every alive drive container is scheduled.
//
// Capacity is gauged via inventory.DriveContainerCapacities (not HasContainerCapacity) so that a
// container expressing capacity through Spec.DriveCapacity/NumDrives — not only Spec.ContainerCapacity —
// also forces deferral. This keeps the planner's invariant honest: no capacity-bearing unscheduled drive
// container ever reaches the capacity planner, which is what makes the planner's Unscheduled skips
// safe (they stay purely defensive).
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

// noopCapacityPlan builds a no-op CapacityPlan (no Grow, no Create) that still carries this cluster's
// current compute sizing, derived from its existing healthy containers. It mirrors the no-op plan
// steadyStatePlan returns, so deferring a plan never disturbs the compute role loop downstream.
func (r *wekaClusterReconcilerLoop) noopCapacityPlan(ctx context.Context, cons *allocator.CapacityConstraints) *allocator.CapacityPlan {
	drv := summarizeDriveContainers(ctx, r.containers, cons)
	cmp := summarizeComputeContainers(ctx, r.containers)
	return &allocator.CapacityPlan{
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
func summarizeDriveContainers(ctx context.Context, containers []*weka.WekaContainer, cons *allocator.CapacityConstraints) driveCapacitySummary {
	var sum driveCapacitySummary
	for _, c := range containers {
		if c.Spec.Mode != weka.WekaContainerModeDrive {
			continue
		}
		if unhealthy, _, _ := utils.IsUnhealthy(ctx, c); unhealthy { //nolint:errcheck // intentional
			continue
		}
		tlcGiB, qlcGiB := inventory.DriveContainerCapacities(c)
		sum.tlcGiB += tlcGiB
		sum.qlcGiB += qlcGiB
		sum.totalTlcDriveCores += allocator.TlcDriveCores(tlcGiB, cons)
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

// steadyStatePlan returns a no-op CapacityPlan and skip=true when the desired per-pool capacity is
// already covered by this cluster's existing healthy drive containers AND the compute set needs no
// change — letting planClusterCapacity skip the expensive node-inventory rebuild. It returns skip=false
// (full re-plan required) when either pool is short or compute could need to grow. When a pool is
// over-provisioned (cur > desired) it emits the same throttled ShrinkEvent the full path would, then
// still skips (a shrink is never auto-applied).
func (r *wekaClusterReconcilerLoop) steadyStatePlan(ctx context.Context, desired allocator.DesiredCapacity, s allocator.ProtectionScheme, cons *allocator.CapacityConstraints) (*allocator.CapacityPlan, bool) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "steadyStatePlan")
	defer logger.End()

	drv := summarizeDriveContainers(ctx, r.containers, cons)
	if allocator.CapacityShort(drv.tlcGiB, desired.TlcRawGiB, cons) ||
		allocator.CapacityShort(drv.qlcGiB, desired.QlcRawGiB, cons) {
		return nil, false // a pool needs growth beyond the deadband → full plan places it
	}

	cmp := summarizeComputeContainers(ctx, r.containers)
	if allocator.ComputeLayoutWouldGrow(desired.ComputeContainers, desired.ComputeCores,
		drv.totalTlcDriveCores, s.MinFdNum(), cons.MaxComputeCoresPerNode, cmp.count, cmp.minCores, cmp.totalCores) {
		return nil, false // compute may need to grow → full plan re-derives against real headroom
	}

	plan := &allocator.CapacityPlan{
		ComputeContainers:  cmp.count,
		ComputeCores:       cmp.minCores,
		TotalTlcDriveCores: drv.totalTlcDriveCores,
	}
	// Over-provisioned pools: emit the shrink advisory (throttled, identical to the full path) but never
	// auto-shrink. Drives are still "covered", so we skip inventory.
	emitShrink := func(pool string, cur, want int) {
		// Suppress the advisory for an in-cap overage — the create-new-before-grow path over-provisions by
		// up to one uniform chunk on purpose (see allocator.OverProvisionCapGiB).
		if cur-want <= allocator.OverProvisionCapGiB(want, cons) {
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
