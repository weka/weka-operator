package wekacluster

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
)

// listNodesForSelector lists candidate nodes filtered by a role node selector. An empty selector
// matches every node in the cluster (standard Kubernetes label-selector semantics).
func (r *wekaClusterReconcilerLoop) listNodesForSelector(ctx context.Context, selector map[string]string) ([]corev1.Node, error) {
	listOpts := []client.ListOption{}
	if len(selector) > 0 {
		listOpts = append(listOpts, client.MatchingLabels(selector))
	}
	nodeList := &corev1.NodeList{}
	if err := r.getClient().List(ctx, nodeList, listOpts...); err != nil {
		return nil, fmt.Errorf("listNodesForSelector: failed to list nodes: %w", err)
	}
	return nodeList.Items, nil
}

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
// clusters AND this cluster's own — so it is pure remaining capacity), the existing-container view,
// and runs the pure allocator.PlanCapacity. It emits warnings/shrink events, logs the decision, and
// returns a WaitError when the plan is infeasible so the reconcile retries.
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

	buildInventory := r.buildNodeInventory
	if r.buildNodeInventoryFn != nil {
		buildInventory = r.buildNodeInventoryFn // test seam (nil in production)
	}
	fdByNode, inventory, computeNodes, err := buildInventory(ctx)
	if err != nil {
		return nil, err
	}
	existingDrives := r.buildExistingDriveContainers(ctx, fdByNode)
	existingCompute := r.buildExistingComputeContainers(ctx)

	plan := allocator.PlanCapacity(desired, s, existingDrives, existingCompute, inventory, computeNodes, cons)

	logger.Info("clusterCapacity plan",
		"desiredTlcGiB", desired.TlcRawGiB, "desiredQlcGiB", desired.QlcRawGiB,
		"minFdNum", s.MinFdNum(), "candidateNodes", len(inventory), "existingDrives", len(existingDrives),
		"grow", len(plan.Grow), "create", len(plan.Create),
		"infeasible", plan.Infeasible)
	for _, n := range inventory {
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
// Capacity is gauged via driveContainerCapacities (not HasContainerCapacity) so that a container
// expressing capacity through Spec.DriveCapacity/NumDrives — not only Spec.ContainerCapacity — also
// forces deferral. This keeps the planner's invariant honest: no capacity-bearing unscheduled drive
// container ever reaches the capacity planner, which is what makes the planner's Unscheduled skips
// safe (they stay purely defensive).
func firstUnscheduledDriveContainer(containers []*weka.WekaContainer) (string, bool) {
	for _, c := range containers {
		if c.Spec.Mode != weka.WekaContainerModeDrive {
			continue
		}
		if tlc, qlc := driveContainerCapacities(c); tlc == 0 && qlc == 0 {
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

// driveContainerCapacities returns a drive container's per-pool capacity (GiB) from its spec: legacy
// driveCapacity×numDrives is TLC-only, otherwise containerCapacity split by driveTypesRatio (a zero
// containerCapacity yields (0,0)). Single source of truth for the controller's three capacity reads.
func driveContainerCapacities(c *weka.WekaContainer) (tlcGiB, qlcGiB int) {
	if c.Spec.DriveCapacity > 0 {
		return c.Spec.DriveCapacity * c.Spec.NumDrives, 0
	}
	return weka.GetTlcQlcCapacity(c.Spec.ContainerCapacity, c.Spec.DriveTypesRatio)
}

// driveCapacitySummary is the per-pool capacity (GiB) of this cluster's existing healthy drive
// containers plus the TLC drive-core total that drives the compute 1:1 ratio.
type driveCapacitySummary struct {
	tlcGiB             int
	qlcGiB             int
	totalTlcDriveCores int
}

// summarizeDriveContainers sums THIS cluster's existing HEALTHY drive containers per pool (from spec,
// matching buildExistingDriveContainers) plus their total TLC drive cores (matching the allocator's
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
		tlcGiB, qlcGiB := driveContainerCapacities(c)
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

// resolveInventoryFDValue resolves a node's failure-domain key for the planner inventory and reports
// whether the node must be skipped. In label-based mode (fdConfig != nil) the FD key is the resolved
// label value, and a node carrying no FD label belongs to no failure domain and is skipped (skip=true).
// In AUTO mode (fdConfig == nil) every host is its own FD, so the key falls back to the node name.
// Both the drive and compute inventory loops use this so they agree on FD assignment — compute must
// span >= minComputeFds distinct FDs, so an unlabeled compute-only node must not masquerade as its own FD.
func resolveInventoryFDValue(node *corev1.Node, fdConfig *weka.FailureDomain) (fdValue string, skip bool) {
	fdValue = allocator.ResolveNodeFDValue(node, fdConfig)
	if fdConfig != nil && fdValue == "" {
		return "", true
	}
	if fdValue == "" {
		fdValue = node.Name
	}
	return fdValue, false
}

// buildNodeInventory returns (fdByNode, inventory, computeNodes). fdByNode maps every drive candidate
// node to its FD key (the resolved label value in label-based mode, else the node name = AUTO/FD-per-host).
// inventory is the UNION of drive candidates (nodes with usable shared-drive capacity) and compute
// candidates (nodes matching the compute role selector — which may be diskless), with
// capacity/cores/hugepages/memory NET of every weka drive container already on the node (other clusters
// AND this cluster's own). computeNodes marks which inventory nodes the compute layout may use (those
// matching the compute selector); it is always non-nil. Drive and compute selectors are listed once when
// equal, twice when they differ.
func (r *wekaClusterReconcilerLoop) buildNodeInventory(ctx context.Context) (fds map[string]string, inv []allocator.NodeCapacity, eligible map[string]bool, err error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "buildNodeInventory")
	defer logger.End()

	cluster := r.cluster
	driveSelector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeDrive)
	computeSelector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeCompute)

	driveNodes, err := r.listNodesForSelector(ctx, driveSelector)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("buildNodeInventory: %w", err)
	}
	computeNodeList := driveNodes // equal selectors: list once and reuse
	if !maps.Equal(driveSelector, computeSelector) {
		if computeNodeList, err = r.listNodesForSelector(ctx, computeSelector); err != nil {
			return nil, nil, nil, fmt.Errorf("buildNodeInventory: %w", err)
		}
	}

	consumed, err := r.consumedNodeResources(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("buildNodeInventory: %w", err)
	}

	fdConfig := cluster.Spec.FailureDomain

	// Nodes still hosting THIS cluster's drive container being deleted. Such a container is excluded from
	// existingDrives (buildExistingDriveContainers skips marked-for-deletion via utils.IsUnhealthy) AND from
	// the node resource charge (aggregateContainerResources now skips marked-for-deletion), so the node
	// re-enters the fresh-candidate pool with its footprint freed. Flagging the drive entry still lets the
	// planner deprioritize the node for fresh drive placement — otherwise a replacement FD is recreated on
	// the node it was just deleted from. Keyed on the deletion timestamp only (IsMarkedForDeletion).
	deletingDriveNodes := map[string]bool{}
	for _, c := range r.containers {
		if c.Spec.Mode == weka.WekaContainerModeDrive && c.IsMarkedForDeletion() {
			if n := string(c.GetNodeAffinity()); n != "" {
				deletingDriveNodes[n] = true
			}
		}
	}

	// Per-node compute resource headroom (cores/hugepages/memory), net of drive containers on the node.
	headroom := func(node *corev1.Node) (cpu, hugepagesMiB, memoryMiB int) {
		cpu = max(0, int(node.Status.Allocatable.Cpu().Value())-consumed.cores[node.Name])
		hugepagesMiB = max(0, nodeAllocatableHugepagesMiB(node)-consumed.hugepages[node.Name])
		memoryMiB = max(0, nodeAllocatableMemoryMiB(node)-consumed.memory[node.Name])
		return cpu, hugepagesMiB, memoryMiB
	}

	// Drive candidates: nodes with usable shared-drive capacity, carrying TLC/QLC headroom and an FD key.
	fdByNode := map[string]string{}
	var driveInv []allocator.NodeCapacity
	for i := range driveNodes {
		node := &driveNodes[i]
		nodeName := weka.NodeName(node.Name)

		// FD key: label value in label-based mode, else node name (AUTO = FD per host). In label-based
		// mode a node without the FD label belongs to no FD and is skipped entirely.
		fdValue, skip := resolveInventoryFDValue(node, fdConfig)
		if skip {
			continue
		}

		// The node was already fetched by listNodesForSelector above — parse its annotations in place
		// rather than re-fetching it per node.
		info, err := allocator.ParseAllocatorNodeInfo(node)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("buildNodeInventory: reading node info for %q: %w", nodeName, err)
		}

		physTLC, physQLC := sumSharedDriveCapacity(info.SharedDrives)
		if physTLC == 0 && physQLC == 0 {
			continue // node has no usable shared-drive capacity (it may still be a compute candidate below)
		}
		fdByNode[node.Name] = fdValue

		// Keep fully drive-consumed nodes in the inventory (TlcGiB/QlcGiB clamp to 0). Drive placement
		// skips them (no pool headroom), but compute sizing must still see their CPU/hugepage headroom.
		tlcGiB := max(0, physTLC-consumed.tlc[node.Name])
		qlcGiB := max(0, physQLC-consumed.qlc[node.Name])
		cpu, hugepagesMiB, memoryMiB := headroom(node)
		driveInv = append(driveInv, allocator.NodeCapacity{
			NodeName:                  node.Name,
			FDValue:                   fdValue,
			TlcGiB:                    tlcGiB,
			QlcGiB:                    qlcGiB,
			AllocatableCPU:            cpu,
			AvailableHugepagesMiB:     hugepagesMiB,
			AvailableMemoryMiB:        memoryMiB,
			HasDeletingDriveContainer: deletingDriveNodes[node.Name],
		})
	}

	// Compute candidates: every node matching the compute selector, with zero drive capacity. Compute
	// must span >= minComputeFds FDs, so each node carries its resolved FD key (label value in
	// label-based mode, else node name in AUTO = FD per host). In label-based mode a node without the
	// FD label belongs to no FD and is skipped entirely. Nodes shared with drives are merged with their
	// drive entry below.
	var computeInv []allocator.NodeCapacity
	for i := range computeNodeList {
		node := &computeNodeList[i]
		fdValue, skip := resolveInventoryFDValue(node, fdConfig)
		if skip {
			continue
		}
		cpu, hugepagesMiB, memoryMiB := headroom(node)
		computeInv = append(computeInv, allocator.NodeCapacity{
			NodeName:              node.Name,
			FDValue:               fdValue,
			AllocatableCPU:        cpu,
			AvailableHugepagesMiB: hugepagesMiB,
			AvailableMemoryMiB:    memoryMiB,
		})
	}

	inventory, computeNodes := mergeRoleNodes(driveInv, computeInv)
	logger.Debug("clusterCapacity node inventory", "driveCandidates", len(driveInv), "computeEligible", len(computeNodes), "inventory", len(inventory))
	return fdByNode, inventory, computeNodes, nil
}

// mergeRoleNodes unions the drive-candidate and compute-candidate node sets into one planner inventory
// and the compute-eligibility map. A node present in both keeps its drive entry (which carries real
// TLC/QLC capacity); a compute-only node is appended with zero drive capacity so drive placement skips
// it while compute sizing can still use it. Every compute candidate is marked eligible.
func mergeRoleNodes(driveInv, computeInv []allocator.NodeCapacity) (inventory []allocator.NodeCapacity, computeNodes map[string]bool) {
	inventory = append([]allocator.NodeCapacity(nil), driveInv...)
	index := make(map[string]struct{}, len(inventory))
	for _, nc := range inventory {
		index[nc.NodeName] = struct{}{}
	}
	computeNodes = make(map[string]bool, len(computeInv))
	for _, nc := range computeInv {
		computeNodes[nc.NodeName] = true
		if _, ok := index[nc.NodeName]; !ok {
			index[nc.NodeName] = struct{}{}
			inventory = append(inventory, nc)
		}
	}
	return inventory, computeNodes
}

// nodeResources holds per-node resource consumption across all drive-sharing containers.
type nodeResources struct {
	tlc, qlc, cores, hugepages, memory map[string]int
}

// consumedNodeResources returns, per node, the TLC/QLC shared-drive capacity, CPU cores, hugepages
// (MiB) and memory (MiB) already claimed by EVERY WekaContainer scheduled or pinned to the node — all
// modes, both other clusters' and this cluster's own. The planner's inventory is therefore pure
// remaining headroom; this cluster's own drive AND compute containers are represented separately in
// existing[] and the planner consumes only the growth increment against the headroom.
func (r *wekaClusterReconcilerLoop) consumedNodeResources(ctx context.Context) (nodeResources, error) {
	kubeService := kubernetes.NewKubeService(r.getClient())
	// List every WekaContainer (all modes, all namespaces) and fold them into one per-node footprint.
	containers, err := kubeService.GetWekaContainersSimple(ctx, "", "", nil)
	if err != nil {
		return nodeResources{}, fmt.Errorf("listing weka containers: %w", err)
	}
	return aggregateContainerResources(containers, allocator.CapacityConstraintsFromConfig()), nil
}

// aggregateContainerResources sums, per node, the resource footprint of weka containers by mode
// (skipping containers marked for deletion — their resources are about to be freed), so headroom()
// can subtract one unified figure (allocatable − consumed) and the planner needs no separate
// per-mode charging:
//   - drive (drive-sharing only): cores/hugepages/memory are derived from per-pool CAPACITY via the
//     single shared sizing model (allocator.RequiredDriveResources) rather than read from its (possibly
//     stale) spec, so the headroom lines up exactly with how the planner sizes/grows that same container;
//     plus its TLC/QLC capacity.
//   - compute: cores/hugepages straight from spec (what the pod requests) and memory from the shared
//     base+per-core model. Charging it here (instead of inside PlanCapacity) also captures OTHER
//     clusters' compute, which the planner never saw.
//   - other modes (e.g. ssdproxy): cores and 2Mi hugepages straight from spec. Memory is not charged —
//     there is no shared sizing model for arbitrary modes.
func aggregateContainerResources(containers []weka.WekaContainer, cons *allocator.CapacityConstraints) nodeResources {
	res := nodeResources{
		tlc:       map[string]int{},
		qlc:       map[string]int{},
		cores:     map[string]int{},
		hugepages: map[string]int{},
		memory:    map[string]int{},
	}
	for i := range containers {
		c := &containers[i]
		if c.IsMarkedForDeletion() { // resources of a container being torn down are about to be freed — don't charge them against node headroom (mirrors buildExistingDriveContainers dropping it from the existing view)
			continue
		}
		node := string(c.GetNodeAffinity())
		if node == "" {
			continue
		}
		switch c.Spec.Mode {
		case weka.WekaContainerModeDrive:
			if !c.UsesDriveSharing() {
				continue
			}
			t, q := driveContainerCapacities(c)
			cores, hugepagesMiB, memoryMiB := allocator.RequiredDriveResources(t, q, cons)
			res.cores[node] += cores
			res.hugepages[node] += hugepagesMiB
			res.memory[node] += memoryMiB
			res.tlc[node] += t
			res.qlc[node] += q
		case weka.WekaContainerModeCompute:
			res.cores[node] += c.Spec.NumCores
			res.hugepages[node] += spec2MiHugepages(c)
			res.memory[node] += allocator.ComputeMemoryFootprintMiB(c.Spec.NumCores, cons)
		default:
			// Any other hugepage-using mode (e.g. the per-node ssdproxy WekaContainer ~2962 MiB).
			res.cores[node] += c.Spec.NumCores
			res.hugepages[node] += spec2MiHugepages(c)
		}
	}
	return res
}

// spec2MiHugepages returns the container's 2Mi hugepage request (MiB). The planner's headroom tracks
// hugepages-2Mi only; a container reserving 1Gi hugepages draws from a distinct pool, so it contributes
// nothing to the 2Mi headroom.
func spec2MiHugepages(c *weka.WekaContainer) int {
	if c.Spec.Hugepages <= 0 || c.Spec.HugepagesSize == "1Gi" {
		return 0
	}
	return c.Spec.Hugepages
}

// buildExistingDriveContainers builds the planner's view of this cluster's healthy drive containers.
func (r *wekaClusterReconcilerLoop) buildExistingDriveContainers(ctx context.Context, fdByNode map[string]string) []allocator.ExistingContainer {
	fdConfig := r.cluster.Spec.FailureDomain
	var existingDrives []allocator.ExistingContainer
	for _, c := range r.containers {
		if c.Spec.Mode != weka.WekaContainerModeDrive {
			continue
		}
		if unhealthy, _, _ := utils.IsUnhealthy(ctx, c); unhealthy { //nolint:errcheck // intentional
			continue
		}
		node := string(c.GetNodeAffinity())
		fd := fdByNode[node]
		if fd == "" && fdConfig == nil {
			fd = node // auto mode: one failure domain per host
		}

		tlcGiB, qlcGiB := driveContainerCapacities(c)

		existingDrives = append(existingDrives, allocator.ExistingContainer{
			Name:        c.Name,
			Node:        node,
			FDValue:     fd,
			TlcGiB:      tlcGiB,
			QlcGiB:      qlcGiB,
			NumCores:    c.Spec.NumCores,
			Unscheduled: c.Status.NodeAffinity == "",
		})
	}
	return existingDrives
}

// buildExistingComputeContainers builds the planner's view of this cluster's healthy compute containers.
func (r *wekaClusterReconcilerLoop) buildExistingComputeContainers(ctx context.Context) []allocator.ExistingComputeContainer {
	var existing []allocator.ExistingComputeContainer
	for _, c := range r.containers {
		if c.Spec.Mode != weka.WekaContainerModeCompute {
			continue
		}
		if unhealthy, _, _ := utils.IsUnhealthy(ctx, c); unhealthy { //nolint:errcheck // intentional
			continue
		}
		existing = append(existing, allocator.ExistingComputeContainer{
			Name:         c.Name,
			Node:         string(c.GetNodeAffinity()),
			NumCores:     c.Spec.NumCores,
			HugepagesMiB: c.Spec.Hugepages,
			Unscheduled:  c.Status.NodeAffinity == "",
		})
	}
	return existing
}

// sumSharedDriveCapacity totals a node's shared-drive annotation entries by drive type.
func sumSharedDriveCapacity(drives []domain.SharedDriveInfo) (tlcGiB, qlcGiB int) {
	for _, sd := range drives {
		switch sd.Type {
		case "TLC":
			tlcGiB += sd.CapacityGiB
		case "QLC":
			qlcGiB += sd.CapacityGiB
		}
	}
	return tlcGiB, qlcGiB
}

// nodeAllocatableHugepagesMiB returns the node's allocatable 2Mi hugepages in MiB.
func nodeAllocatableHugepagesMiB(node *corev1.Node) int {
	name := corev1.ResourceName(string(corev1.ResourceHugePagesPrefix) + "2Mi")
	q := node.Status.Allocatable[name]
	return int(q.Value() / (1 << 20))
}

// nodeAllocatableMemoryMiB returns the node's allocatable memory in MiB.
func nodeAllocatableMemoryMiB(node *corev1.Node) int {
	q := node.Status.Allocatable[corev1.ResourceMemory]
	return int(q.Value() / (1 << 20))
}
