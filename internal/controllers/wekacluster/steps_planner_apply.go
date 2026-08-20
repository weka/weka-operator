package wekacluster

import (
	"context"
	stderrors "errors" // stdlib, for errors.Join: github.com/pkg/errors elsewhere shadows the name
	"fmt"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/controllers/utils"
)

// steps_planner_apply.go is the shared build/apply layer for both capacity-planner modes: building containers
// from plan.Create, in-place growth, and the conflict-retrying update. Mode differences are switches on
// plannerSizing, visible side by side.

// plannerSizing names how a cluster's drive and compute containers are sized.
type plannerSizing int

const (
	// sizingCountBased sizes from the template's own container counts; not planner-managed.
	sizingCountBased plannerSizing = iota
	sizingClusterCapacity
	sizingAutoFullDrives
)

func (m plannerSizing) String() string {
	switch m {
	case sizingClusterCapacity:
		return "clusterCapacity"
	case sizingAutoFullDrives:
		return "autoFullDrives"
	default:
		return "countBased"
	}
}

// plannerSizingMode is the mode-detection site. UsesAutoFullDrives is a catch-all for "neither container
// counts nor any capacity field set", so it must be tested last. Nil-safe: a nil spec.dynamicTemplate is the
// daemonset mode.
func plannerSizingMode(spec *weka.WekaClusterSpec) (mode plannerSizing, plannerManaged bool) {
	switch dyn := spec.Dynamic; {
	case dyn.UsesClusterCapacity():
		return sizingClusterCapacity, true
	case dyn.UsesAutoFullDrives():
		return sizingAutoFullDrives, true
	default:
		return sizingCountBased, false
	}
}

// plannerManaged reports whether the capacity planner owns this cluster's drive and compute sizing. Same
// answer as allocator.IsPlannerManaged, routed through the one detection site so the two can never disagree
// about a cluster.
func (r *wekaClusterReconcilerLoop) plannerManaged() bool {
	_, managed := plannerSizingMode(&r.cluster.Spec)
	return managed
}

// updateContainerWithRetry re-reads c inside retry.RetryOnConflict and re-applies mutate to that copy, since
// r.containers's resourceVersion is routinely stale by growth time. mutate returns false when the latest copy
// already satisfies the target (grown concurrently); true writes only the fields mutate touched. On success
// the server copy replaces c so later steps and the event text read what was actually written.
func (r *wekaClusterReconcilerLoop) updateContainerWithRetry(
	ctx context.Context, c *weka.WekaContainer, mutate func(latest *weka.WekaContainer) bool,
) (skipped bool, err error) {
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &weka.WekaContainer{}
		if getErr := r.getClient().Get(ctx, client.ObjectKeyFromObject(c), latest); getErr != nil {
			return getErr
		}
		if !mutate(latest) {
			skipped = true
			return nil
		}
		skipped = false
		if updErr := r.getClient().Update(ctx, latest); updErr != nil {
			return updErr
		}
		latest.DeepCopyInto(c)
		return nil
	})
	return skipped, err
}

// buildPlannerDriveContainers runs the mode's planner once and turns the result into work: existing drive and
// compute containers grow in place first, then new drive containers are built from plan.Create — one planning
// pass, so freed compute cores and adopted drives can never disagree. Returns a WaitError when the plan is
// infeasible or its inventory is not ready; the planner has already recorded the event.
func (r *wekaClusterReconcilerLoop) buildPlannerDriveContainers(ctx context.Context, mode plannerSizing) (
	driveContainers []*weka.WekaContainer, skipped []string, plan *capacityplanner.CapacityPlan, err error,
) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "buildPlannerDriveContainers")
	defer logger.End()

	switch mode {
	case sizingClusterCapacity:
		plan, err = r.planClusterCapacity(ctx)
	default:
		plan, err = r.planAutoFullDrives(ctx)
	}
	if err != nil {
		return nil, nil, nil, err
	}

	applied, failed, growErr := r.applyPlannerDriveGrowth(ctx, mode, plan)
	// Only the daemonset mode announces growth at cluster level; clusterCapacity has its own event surface.
	if mode == sizingAutoFullDrives {
		r.announceDriveGrowth(plan, applied, failed, growErr)
	}
	if growErr != nil {
		return nil, nil, nil, growErr
	}
	if err := r.applyPlannerComputeGrowth(ctx, plan); err != nil {
		return nil, nil, nil, err
	}

	cluster := r.cluster
	template := allocator.GetWekaClusterTemplate(cluster.Spec.Dynamic)

	for i := range plan.Create {
		pc := &plan.Create[i]
		name := allocator.NewContainerName(weka.WekaContainerModeDrive)
		logger.Info("Building planner-managed drive container", "mode", mode.String(), "name", name,
			"node", pc.Node, "tlcGiB", pc.TlcGiB, "qlcGiB", pc.QlcGiB, "numDrives", pc.NumDrives, "cores", pc.NumCores)

		// Value copy (Cores is an embedded value field), so the shared template is not mutated.
		perTemplate := template
		perTemplate.Cores.Drive = pc.NumCores

		// Sized from this container's own cores and drives, not the cluster-wide template, which cannot represent a
		// heterogeneous fleet. NumDrives is 0 for clusterCapacity (drives are virtual there; CEL forbids numDrives),
		// selecting the per-core-only branch.
		hp := allocator.DriveHugepagesFromPlan(cluster, pc.NumCores, pc.NumDrives)

		container, buildErr := factory.NewWekaContainerForWekaCluster(cluster, perTemplate, hp, weka.WekaContainerModeDrive, name)
		if buildErr != nil {
			logger.Info("Skipping drive container — failed to build", "name", name, "reason", buildErr)
			skipped = append(skipped, fmt.Sprintf("role drive container %s: %s", name, buildErr))
			continue
		}
		// The one capacity dimension each mode owns, keyed on mode rather than which plan fields are non-zero: the
		// daemonset planner also fills TlcGiB, and writing that as ContainerCapacity would silently convert a
		// full-drives container into a drive-sharing one.
		switch mode {
		case sizingClusterCapacity:
			container.Spec.ContainerCapacity = pc.TlcGiB + pc.QlcGiB
			container.Spec.DriveTypesRatio = pc.Ratio
		case sizingAutoFullDrives:
			container.Spec.NumDrives = pc.NumDrives
		}
		// Pin to the planned node via Spec.NodeAffinity, a dedicated field the cluster-level NodeSelector merge
		// never touches. FD identity stays Weka's: AUTO mode makes FD = host, label mode uses the
		// factory-propagated Spec.FailureDomain.
		container.Spec.NodeAffinity = weka.NodeName(pc.Node)
		driveContainers = append(driveContainers, container)
	}

	return driveContainers, skipped, plan, nil
}

// appliedGrowth is one drive container whose growth was actually written, for the cluster-level announcement.
type appliedGrowth struct {
	summary      string
	coresChanged bool
}

// applyPlannerDriveGrowth grows existing drive containers in place to absorb what the plan assigns, and only
// ever grows. Returns what was actually written, so the announcement cannot claim growth the plan merely
// intended. Per-container failures are collected and joined on the way out rather than returned immediately,
// so one conflicting container does not drop the growth of every container after it in the batch.
func (r *wekaClusterReconcilerLoop) applyPlannerDriveGrowth(
	ctx context.Context, mode plannerSizing, plan *capacityplanner.CapacityPlan,
) (applied []appliedGrowth, failed int, err error) {
	if len(plan.Grow) == 0 {
		return nil, 0, nil
	}
	logger := instrumentation.CurrentSpanLogger(ctx)
	cluster := r.cluster

	byName := make(map[string]*weka.WekaContainer, len(r.containers))
	for _, c := range r.containers {
		byName[c.Name] = c
	}

	var growErrs []error
	for i := range plan.Grow {
		g := &plan.Grow[i]
		c, ok := byName[g.Name]
		if !ok {
			logger.Info("skipping growth for a container no longer in this reconcile's container list", "name", g.Name)
			continue
		}

		newCap := g.NewTlcGiB + g.NewQlcGiB
		// The one dimension this mode owns, besides cores.
		growsOwn := false
		switch mode {
		case sizingClusterCapacity:
			growsOwn = newCap > c.Spec.ContainerCapacity
		case sizingAutoFullDrives:
			growsOwn = g.NewNumDrives > c.Spec.NumDrives
		}
		coresChanged := g.NewCores > c.Spec.NumCores

		// Daemonset ratchets cores independently of drive growth; clusterCapacity must not — a net-zero-capacity
		// growth record carrying higher NewCores would force a pod recreation on a live drive container.
		coresGrowIndependently := mode == sizingAutoFullDrives
		if !growsOwn && (!coresChanged || !coresGrowIndependently) {
			logger.Debug("skipping growth: container already at or above target on every dimension this mode grows",
				"name", c.Name, "mode", mode.String())
			continue
		}

		// Hugepages are refreshed exactly when a written field changes: cores for both modes, plus drives for the
		// daemonset mode (200 MiB each). A clusterCapacity capacity-only growth leaves them untouched.
		writeHugepages := coresChanged || (mode == sizingAutoFullDrives && growsOwn)
		hp := allocator.DriveHugepagesFromPlan(cluster, g.NewCores, g.NewNumDrives)

		logger.Info("Growing planner-managed drive container in place", "mode", mode.String(), "name", c.Name,
			"newContainerCapacity", newCap, "numDrives", g.NewNumDrives, "cores", g.NewCores)

		alreadyGrown, updErr := r.updateContainerWithRetry(ctx, c, func(latest *weka.WekaContainer) bool {
			latestGrowsOwn := false
			switch mode {
			case sizingClusterCapacity:
				latestGrowsOwn = newCap > latest.Spec.ContainerCapacity
			case sizingAutoFullDrives:
				latestGrowsOwn = g.NewNumDrives > latest.Spec.NumDrives
			}
			latestCoresChanged := coresChanged && g.NewCores > latest.Spec.NumCores
			if !latestGrowsOwn && !latestCoresChanged {
				return false // grown concurrently to at least this target — never shrink
			}
			if latestGrowsOwn {
				switch mode {
				case sizingClusterCapacity:
					latest.Spec.ContainerCapacity = newCap
					latest.Spec.DriveTypesRatio = capacityplanner.RatioFromCaps(g.NewTlcGiB, g.NewQlcGiB)
				case sizingAutoFullDrives:
					latest.Spec.NumDrives = g.NewNumDrives
				}
			}
			if latestCoresChanged {
				latest.Spec.NumCores = g.NewCores
			}
			if writeHugepages {
				// Written verbatim: the guard above only lets drives/cores rise, and DriveHugepagesFromPlan's
				// fields are monotone in both, so a fresh computation here can never be lower than what is stored.
				latest.Spec.Hugepages = hp.Hugepages
				latest.Spec.HugepagesOffset = hp.HugepagesOffset
			}
			return true
		})
		if updErr != nil {
			growErrs = append(growErrs, fmt.Errorf("applyPlannerDriveGrowth: failed to update container %s: %w", c.Name, updErr))
			continue
		}
		if alreadyGrown {
			logger.Debug("skipping growth: container was already grown to the target concurrently", "name", c.Name)
			continue
		}

		node := string(c.GetNodeAffinity())
		if node == "" {
			node = "<unpinned>"
		}
		applied = append(applied, appliedGrowth{
			summary:      r.driveGrowthSummary(mode, c, node, newCap),
			coresChanged: coresChanged,
		})
		r.Recorder.Event(c, v1.EventTypeWarning, reasonCapacityGrowthApplied,
			r.driveGrowthMessage(mode, c, coresChanged, newCap))
	}
	return applied, len(growErrs), stderrors.Join(growErrs...)
}

// driveGrowthSummary is one container's entry in the cluster-level announcement.
func (r *wekaClusterReconcilerLoop) driveGrowthSummary(mode plannerSizing, c *weka.WekaContainer, node string, newCap int) string {
	if mode == sizingClusterCapacity {
		return fmt.Sprintf("%s on %s to %d GiB/%d core(s)", c.Name, node, newCap, c.Spec.NumCores)
	}
	return fmt.Sprintf("%s on %s to %d drive(s)/%d core(s)", c.Name, node, c.Spec.NumDrives, c.Spec.NumCores)
}

// driveGrowthMessage is the per-container CapacityGrowthApplied text. Both cases are Warnings: a cores bump
// changes the pod spec, and even a drives-only growth raises the hugepages reservation by 200 MiB per drive,
// which a running pod's immutable hugepages limit does not cover until it is recreated.
func (r *wekaClusterReconcilerLoop) driveGrowthMessage(mode plannerSizing, c *weka.WekaContainer, coresChanged bool, newCap int) string {
	if mode == sizingClusterCapacity {
		if coresChanged {
			return fmt.Sprintf("applied clusterCapacity growth to drive container (capacity %d GiB, cores %d); the drive spec changed — the pod must be recreated to apply the new cores/hugepages", newCap, c.Spec.NumCores)
		}
		return fmt.Sprintf("applied clusterCapacity growth to drive container live (capacity %d GiB); no restart required", newCap)
	}
	if coresChanged {
		return fmt.Sprintf("applied auto full drives growth to drive container (numDrives %d, cores %d); the drive spec changed — the pod must be recreated to apply the new cores/hugepages", c.Spec.NumDrives, c.Spec.NumCores)
	}
	return fmt.Sprintf("applied auto full drives growth to drive container (numDrives %d); drives were added live to the running weka container, but its hugepages reservation grew with them — recreate the pod so its hugepages limit and weka.io/drives request match the new drive count", c.Spec.NumDrives)
}

// announceDriveGrowth emits the cluster-level growth pair for what was actually written. A partial batch
// reports both legs as a count, so success never implies the whole plan was applied.
func (r *wekaClusterReconcilerLoop) announceDriveGrowth(plan *capacityplanner.CapacityPlan, applied []appliedGrowth, failed int, err error) {
	if len(applied) > 0 {
		summaries := make([]string, 0, len(applied))
		restartRequired := false
		for _, a := range applied {
			summaries = append(summaries, a.summary)
			restartRequired = restartRequired || a.coresChanged
		}
		msg := fmt.Sprintf("auto full drives growth applied to %d drive container(s): %s",
			len(applied), strings.Join(summaries, "; "))
		if restartRequired {
			msg += "; cores changed, so the affected pod(s) must be recreated before the new sizing takes effect (see the per-container CapacityGrowthApplied events)"
		}
		if err != nil {
			msg += fmt.Sprintf("; %d of %d planned container(s) could not be grown and will be retried (see the operator log)",
				failed, len(plan.Grow))
		}
		r.emitPlannerEvent(reasonAutoFullDrivesGrowthDetected, msg)
		return
	}
	if err != nil {
		r.emitPlannerEvent(reasonAutoFullDrivesGrowthDeferred,
			fmt.Sprintf("auto full drives growth was planned for %d drive container(s) but none could be applied: %v; the operator retries on the next reconcile, but a later plan may no longer offer the same growth",
				len(plan.Grow), err))
	}
}

// applyPlannerComputeGrowth grows existing compute containers toward the cores and hugepages ComputeLayout
// assigns to their node. Both fields ratchet and never shrink: cores stay at their layout entry once offered,
// and hugepages take the higher of the entry's figure and what the container already reserves. Needs no mode
// parameter: the layout is the sole source of compute sizing for both modes.
func (r *wekaClusterReconcilerLoop) applyPlannerComputeGrowth(ctx context.Context, plan *capacityplanner.CapacityPlan) error {
	if len(plan.ComputeLayout) == 0 {
		return nil
	}
	logger := instrumentation.CurrentSpanLogger(ctx)
	cluster := r.cluster

	targetByNode := make(map[string]capacityplanner.ComputeContainerSpec, len(plan.ComputeLayout))
	for _, e := range plan.ComputeLayout {
		if e.Node != "" {
			targetByNode[e.Node] = e
		}
	}

	var errs []error
	for _, c := range r.containers {
		if c.Spec.Mode != weka.WekaContainerModeCompute {
			continue
		}
		if unhealthy, _, _ := utils.IsUnhealthy(ctx, c); unhealthy { //nolint:errcheck // intentional
			continue
		}
		entry, ok := targetByNode[string(c.GetNodeAffinity())]
		if !ok {
			continue // not in the layout (unpinned or unknown node) — leave untouched
		}
		if entry.NumCores < c.Spec.NumCores {
			// Cores are never shrunk, and this entry's hugepages figure is derived for that smaller core
			// count, so it is not a valid reservation for what is actually running either.
			continue
		}
		// Ratchet: hugepages only rise here, since the pod's hugepages limit is immutable and the planner's
		// headroom accounting charges hugepages from the spec — a lower figure would look like freed capacity.
		hp := allocator.ComputeHugepagesFromPlan(cluster, entry.HugepagesMiB, entry.NumCores)
		if entry.NumCores <= c.Spec.NumCores && hp.Hugepages <= c.Spec.Hugepages {
			continue // layout offers nothing higher on either axis
		}
		logger.Info("Applying planner-managed compute growth in place", "name", c.Name, "node", entry.Node,
			"cores", entry.NumCores, "hugepages", hp.Hugepages)

		alreadyGrown, updErr := r.updateContainerWithRetry(ctx, c, func(latest *weka.WekaContainer) bool {
			if entry.NumCores < latest.Spec.NumCores {
				return false
			}
			if entry.NumCores <= latest.Spec.NumCores && hp.Hugepages <= latest.Spec.Hugepages {
				return false
			}
			latest.Spec.NumCores = entry.NumCores
			// Ratcheted per field: cores can rise while the layout's hugepages fall, and keeping the old
			// hugepages verbatim then could land below the floor for the new core count.
			latest.Spec.Hugepages = max(hp.Hugepages, latest.Spec.Hugepages)
			// HugepagesOffset is written verbatim, not ratcheted: only Hugepages is charged against claimed
			// capacity, so this field carries no risk of the planner believing freed capacity a pod hasn't released.
			latest.Spec.HugepagesOffset = hp.HugepagesOffset
			return true
		})
		if updErr != nil {
			errs = append(errs, fmt.Errorf("applyPlannerComputeGrowth: failed to update container %s: %w", c.Name, updErr))
			continue
		}
		if alreadyGrown {
			logger.Debug("skipping growth: container was already grown to the target concurrently", "name", c.Name)
			continue
		}
		r.Recorder.Event(c, v1.EventTypeWarning, reasonCapacityGrowthApplied,
			fmt.Sprintf("applied compute growth to container (cores %d, hugepages %d MiB); the compute spec changed — the pod must be recreated to apply the new cores/hugepages",
				c.Spec.NumCores, c.Spec.Hugepages))
	}
	return stderrors.Join(errs...)
}
