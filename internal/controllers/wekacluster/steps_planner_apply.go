package wekacluster

import (
	"context"
	stderrors "errors" // stdlib, for errors.Join: github.com/pkg/errors elsewhere shadows the name
	"fmt"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/pkg/util"
)

// steps_planner_apply.go builds clusterCapacity containers from plan.Create and applies in-place growth; the
// conflict-retrying update is shared with the daemonset path.

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

func computeGrowthMessage(c *weka.WekaContainer) string {
	return fmt.Sprintf("applied compute growth to container (cores %d, hugepages %d MiB); the compute spec changed — the pod must be recreated to apply the new cores/hugepages",
		c.Spec.NumCores, c.Spec.Hugepages)
}

// buildPlannerDriveContainers runs the clusterCapacity planner once and turns the result into work: existing
// drive and compute containers grow in place first, then new drive containers are built from plan.Create — one
// planning pass, so freed compute cores and adopted drives can never disagree. Returns a WaitError when the plan
// is infeasible or its inventory is not ready; the planner has already recorded the event.
func (r *wekaClusterReconcilerLoop) buildPlannerDriveContainers(ctx context.Context) (
	driveContainers []*weka.WekaContainer, skipped []string, plan *capacityplanner.CapacityPlan, err error,
) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "buildPlannerDriveContainers")
	defer logger.End()

	plan, err = r.planClusterCapacity(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	if err := r.applyPlannerDriveGrowth(ctx, plan); err != nil {
		return nil, nil, nil, err
	}
	if err := r.applyPlannerComputeGrowth(ctx, plan); err != nil {
		return nil, nil, nil, err
	}

	cluster := r.cluster
	template := allocator.GetWekaClusterTemplate(cluster.Spec.Dynamic)

	for i := range plan.Create {
		pc := &plan.Create[i]
		name := allocator.NewContainerName(weka.WekaContainerModeDrive)
		logger.Info("Building planner-managed drive container", "name", name,
			"node", pc.Node, "tlcGiB", pc.TlcGiB, "qlcGiB", pc.QlcGiB, "cores", pc.NumCores)

		// Value copy (Cores is an embedded value field), so the shared template is not mutated.
		perTemplate := template
		perTemplate.Cores.Drive = pc.NumCores

		// Sized from this container's own cores, not the cluster-wide template, which cannot represent a
		// heterogeneous fleet. NumDrives is 0 for clusterCapacity (drives are virtual there; CEL forbids numDrives),
		// selecting the per-core-only branch.
		hp := allocator.DriveHugepagesFromPlan(cluster, pc.NumCores, 0)

		container, buildErr := factory.NewWekaContainerForWekaCluster(cluster, perTemplate, hp, weka.WekaContainerModeDrive, name)
		if buildErr != nil {
			logger.Info("Skipping drive container — failed to build", "name", name, "reason", buildErr)
			skipped = append(skipped, fmt.Sprintf("role drive container %s: %s", name, buildErr))
			continue
		}
		container.Spec.ContainerCapacity = pc.TlcGiB + pc.QlcGiB
		container.Spec.DriveTypesRatio = pc.Ratio
		// Pin to the planned node via Spec.NodeAffinity, a dedicated field the cluster-level NodeSelector merge
		// never touches. FD identity stays Weka's: AUTO mode makes FD = host, label mode uses the
		// factory-propagated Spec.FailureDomain.
		container.Spec.NodeAffinity = weka.NodeName(pc.Node)
		driveContainers = append(driveContainers, container)
	}

	return driveContainers, skipped, plan, nil
}

// applyPlannerDriveGrowth grows existing drive containers in place to absorb what the plan assigns, and only
// ever grows. Per-container failures are collected and joined on the way out rather than returned immediately,
// so one conflicting container does not drop the growth of every container after it in the batch.
func (r *wekaClusterReconcilerLoop) applyPlannerDriveGrowth(ctx context.Context, plan *capacityplanner.CapacityPlan) error {
	if len(plan.Grow) == 0 {
		return nil
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
		coresChanged := g.NewCores > c.Spec.NumCores

		// Cores grow only together with capacity: a net-zero-capacity growth record carrying higher NewCores
		// would force a pod recreation on a live drive container.
		if newCap <= c.Spec.ContainerCapacity {
			logger.Debug("skipping growth: container already at or above target capacity", "name", c.Name)
			continue
		}
		hp := allocator.DriveHugepagesFromPlan(cluster, g.NewCores, 0)

		logger.Info("Growing planner-managed drive container in place", "name", c.Name,
			"newContainerCapacity", newCap, "cores", g.NewCores)

		alreadyGrown, updErr := r.updateContainerWithRetry(ctx, c, func(latest *weka.WekaContainer) bool {
			latestGrowsOwn := newCap > latest.Spec.ContainerCapacity
			latestCoresChanged := coresChanged && g.NewCores > latest.Spec.NumCores
			if !latestGrowsOwn && !latestCoresChanged {
				return false // grown concurrently to at least this target — never shrink
			}
			if latestGrowsOwn {
				latest.Spec.ContainerCapacity = newCap
				latest.Spec.DriveTypesRatio = capacityplanner.RatioFromCaps(g.NewTlcGiB, g.NewQlcGiB)
			}
			if latestCoresChanged {
				latest.Spec.NumCores = g.NewCores
			}
			if coresChanged {
				// Written verbatim: the guard above only lets cores rise, and DriveHugepagesFromPlan's fields are
				// monotone in them, so a fresh computation here can never be lower than what is stored.
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

		message := fmt.Sprintf("applied clusterCapacity growth to drive container live (capacity %d GiB); no restart required", newCap)
		if coresChanged {
			message = fmt.Sprintf("applied clusterCapacity growth to drive container (capacity %d GiB, cores %d); the drive spec changed — the pod must be recreated to apply the new cores/hugepages", newCap, c.Spec.NumCores)
		}
		util.RecordEvent(r.Recorder, c, v1.EventTypeWarning, reasonCapacityGrowthApplied, consts.ActionApplyCapacityGrowth, message)
	}
	return stderrors.Join(growErrs...)
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
		util.RecordEvent(r.Recorder, c, v1.EventTypeWarning, reasonCapacityGrowthApplied, consts.ActionApplyCapacityGrowth, computeGrowthMessage(c))
	}
	return stderrors.Join(errs...)
}
