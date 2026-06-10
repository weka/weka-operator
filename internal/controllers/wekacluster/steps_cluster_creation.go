// package wekacluster contains the reconciliation logic for WekaCluster resources
package wekacluster

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-lib/pkg/workers"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	util "github.com/weka/weka-operator/pkg/util"
)

// GetClusterSetupSteps returns the node selection and resource allocation steps
func GetClusterSetupSteps(loop *wekaClusterReconcilerLoop) []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{
			Run: loop.InitState,
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name:    condition.CondClusterSecretsCreated,
				Message: "Cluster secrets are created",
			},
			Run: loop.EnsureLoginCredentials,
		},
		&lifecycle.SimpleStep{
			Run: loop.AllocateClusterRanges,
			Predicates: lifecycle.Predicates{
				func() bool {
					return loop.cluster.Status.Ports.BasePort == 0
				},
			},
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.CondPodsCreated,
			},
			Run:                loop.EnsureWekaContainers,
			SkipStepStateCheck: true,
		},
		&lifecycle.SimpleStep{
			Run: loop.GarbageCollectUnschedulableDriveContainers,
			Predicates: lifecycle.Predicates{
				func() bool {
					return loop.cluster.Spec.Dynamic != nil && loop.cluster.Spec.Dynamic.UsesClusterCapacity()
				},
			},
		},
		&lifecycle.SimpleStep{
			Run: loop.HandleSpecUpdates,
		},
		&lifecycle.SimpleStep{
			Run: loop.updateContainersOnNodeSelectorMismatch,
			Predicates: lifecycle.Predicates{
				lifecycle.BoolValue(config.Config.CleanupBackendsOnNodeSelectorMismatch),
			},
			Throttling: &throttling.ThrottlingSettings{
				Interval:          config.Consts.SelectorMismatchCleanupInterval,
				EnsureStepSuccess: true,
			},
		},
		// NOTE: tolerations mismatch and node selector mismatch deletion is now handled
		// at container level in deleteIfTolerationsMismatch and deleteIfNodeSelectorMismatch
	}
}

// GetClusterCreationSteps returns the cluster formation steps for the WekaCluster reconciliation
func GetClusterCreationSteps(loop *wekaClusterReconcilerLoop) []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.CondPodsReady,
			},
			Run: loop.InitialContainersReady,
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.CondClusterCreated,
			},
			Run: loop.FormCluster,
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.CondPostClusterFormedScript,
			},
			Run: loop.RunPostFormClusterScript,
			Predicates: lifecycle.Predicates{
				loop.HasPostFormClusterScript,
				lifecycle.IsNotFunc(loop.cluster.IsExpand),
			},
		},
		&lifecycle.SimpleStep{
			Run: loop.refreshContainersJoinIps,
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.CondJoinedCluster,
			},
			Run: loop.WaitForContainersJoin,
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.CondDrivesAdded,
			},
			Run: loop.WaitForDrivesAdd,
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.CondIoStarted,
			},
			Run: loop.StartIo,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(loop.cluster.IsExpand),
			},
		},
	}
}

func (r *wekaClusterReconcilerLoop) InitState(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	wekaCluster := r.cluster
	if !controllerutil.ContainsFinalizer(wekaCluster, consts.WekaFinalizer) {

		wekaCluster.Status.InitStatus()
		wekaCluster.Status.LastAppliedImage = wekaCluster.Spec.Image
		wekaCluster.Status.LastAppliedPodConfigHash = CalcClusterPodConfigVersion(&wekaCluster.Spec)

		err := r.getClient().Status().Update(ctx, wekaCluster)
		if err != nil {
			logger.Error(err, "failed to init states")
		}

		if updated := controllerutil.AddFinalizer(wekaCluster, consts.WekaFinalizer); updated {
			logger.Info("Adding Finalizer for weka cluster")
			if err := r.getClient().Update(ctx, wekaCluster); err != nil {
				logger.Error(err, "Failed to update custom resource to add finalizer")
				return err
			}

			if err := r.getClient().Get(ctx, client.ObjectKey{Namespace: wekaCluster.Namespace, Name: wekaCluster.Name}, r.cluster); err != nil {
				logger.Error(err, "Failed to re-fetch data")
				return err
			}
			logger.Info("Finalizer added for wekaCluster", "conditions", len(wekaCluster.Status.Conditions))
		}
	}

	clusterGuid := string(wekaCluster.GetUID())

	_, err := services.ClustersCachedInfo.GetClusterCreationTime(ctx, clusterGuid)
	if err != nil {
		// if cluster is already formed, set cluster creation time
		formedClusterCondition := meta.FindStatusCondition(wekaCluster.Status.Conditions, condition.CondClusterCreated)
		if formedClusterCondition == nil || formedClusterCondition.Status == metav1.ConditionFalse {
			return nil
		}

		err = services.ClustersCachedInfo.SetClusterCreationTime(ctx, clusterGuid, formedClusterCondition.LastTransitionTime.Time)
		if err != nil {
			logger.Error(err, "Failed to set cluster creation time")
			return err
		}
	}

	return nil
}

// AllocateClusterRanges allocates the cluster-level port ranges.
// This step runs before container creation to ensure port ranges are available
func (r *wekaClusterReconcilerLoop) AllocateClusterRanges(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	cluster := r.cluster

	logger.InfoWithStatus(codes.Unset, "Allocating cluster-level port ranges")

	// Fetch feature flags - they determine ports per container
	featureFlags, err := r.GetFeatureFlags(ctx)
	if err != nil {
		return err // Propagate error (including WaitError if ad-hoc container still running)
	}

	resourcesAllocator := allocator.GetAllocator(r.getClient())

	err = resourcesAllocator.AllocateClusterRange(ctx, cluster, featureFlags)
	var allocateRangeErr *allocator.AllocateClusterRangeError
	if errors.As(err, &allocateRangeErr) {
		_ = r.RecordEvent(v1.EventTypeWarning, "AllocateClusterRangeError", allocateRangeErr.Error()) //nolint:errcheck // error is intentionally ignored
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}
	if err != nil {
		logger.Error(err, "Failed to allocate cluster range")
		return err
	}

	// Update cluster status with allocated port ranges
	err = r.getClient().Status().Update(ctx, cluster)
	if err != nil {
		logger.Error(err, "Failed to update cluster status")
		return err
	}

	logger.Info("Successfully allocated cluster port ranges",
		"basePort", cluster.Status.Ports.BasePort,
		"portRange", cluster.Status.Ports.PortRange,
		"lbPort", cluster.Status.Ports.LbPort,
		"s3Port", cluster.Status.Ports.S3Port)

	return nil
}

func (r *wekaClusterReconcilerLoop) refreshContainersJoinIps(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	containers := r.containers
	cluster := r.cluster

	_, err := services.ClustersCachedInfo.JoinIpsAreValid(ctx, string(cluster.GetUID()), cluster.Name, cluster.Namespace)
	if err != nil {
		logger.Debug("Cannot get join ips", "msg", err.Error())
		err := services.ClustersCachedInfo.RefreshJoinIps(ctx, containers, cluster)
		if err != nil {
			logger.Error(err, "Failed to refresh join ips")
			return err
		}
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) EnsureWekaContainers(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	cluster := r.cluster

	// Validate driveTypesRatio before creating containers
	if err := r.ValidateDriveTypesRatio(ctx); err != nil {
		logger.Error(err, "Invalid driveTypesRatio configuration")
		return err
	}

	resolvedURL, source := utils.ResolveDriversDistService(ctx, r.getClient(), cluster.Namespace, cluster.Spec.DriversDistService)
	cluster.Spec.DriversDistService = resolvedURL
	switch source {
	case utils.DriverDistDefault:
		_ = r.RecordEvent(v1.EventTypeNormal, "DriversDistDefault", fmt.Sprintf("No WekaPolicy, using default driversDistService: %s", resolvedURL)) //nolint:errcheck // event recording errors are intentionally ignored
	case utils.DriverDistPolicy:
		_ = r.RecordEvent(v1.EventTypeNormal, "DriversDistAutoResolved", fmt.Sprintf("Resolved driversDistService from WekaPolicy: %s", resolvedURL)) //nolint:errcheck // event recording errors are intentionally ignored
	case utils.DriverDistAmbiguous:
		_ = r.RecordEvent(v1.EventTypeWarning, "DriversDistAmbiguousPolicy", fmt.Sprintf("Multiple WekaPolicy resources found for drivers distribution, falling back to default: %s", resolvedURL)) //nolint:errcheck // event recording errors are intentionally ignored
	}
	missingContainers, err := r.BuildMissingContainers(ctx)
	if err != nil {
		logger.Error(err, "Failed to create missing containers")
		return err
	}
	for _, container := range missingContainers {
		if err := ctrl.SetControllerReference(cluster, container, r.Manager.GetScheme()); err != nil {
			logger.Error(err, "Failed to set controller reference")
			return err
		}
	}

	if len(missingContainers) == 0 {
		return nil
	}

	var joinIps []string
	if meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondClusterCreated) || cluster.IsExpand() {
		//TODO: Update-By-Expansion, cluster-side join-ips until there are own containers
		allowExpansion := false
		err := services.ClustersCachedInfo.RefreshJoinIps(ctx, r.containers, cluster)
		if err != nil {
			allowExpansion = true
		}
		joinIps, err = services.ClustersCachedInfo.GetJoinIps(ctx, string(cluster.GetUID()), cluster.Name, cluster.Namespace)
		// at this point we should have join ips, if not, we should allow expansion
		if len(joinIps) == 0 {
			allowExpansion = true
		}
		if err != nil && len(cluster.Spec.ExpandEndpoints) != 0 && allowExpansion { // TODO: consider removing ExpandEndpoints fallback once join-ip caching is reliable
			joinIps = cluster.Spec.ExpandEndpoints
		} else if err != nil {
			logger.Error(err, "Failed to get join ips")
			return err
		}
	}

	specVersion := CalcClusterPodConfigVersion(&cluster.Spec)

	for _, container := range missingContainers {
		if len(joinIps) != 0 {
			container.Spec.JoinIps = joinIps
		}
		container.Spec.PodConfigHash = specVersion
	}

	results := workers.ProcessConcurrently(ctx, missingContainers, 32, func(ctx context.Context, container *weka.WekaContainer) error {
		err := r.getClient().Create(ctx, container)
		return err
	})

	for _, result := range results.Items {
		if result.Err == nil {
			r.containers = append(r.containers, result.Object)
		}
	}

	return results.AsError()
}

// buildClusterCapacityDriveContainers runs the clusterCapacity planner once and applies its full plan:
// it grows existing drive AND compute containers in place (plan.Grow / plan.ComputeCores) and builds
// the new, node-pinned drive containers it asks to CREATE (plan.Create). It returns the drive
// containers to create, any skipped-build reasons, the resolved plan (the caller reads its
// node-core-aware compute sizing — ComputeContainers/ComputeCores/ComputeNodes), and a WaitError when
// the plan is infeasible (caller returns and retries next reconcile). Growth and creation are
// intentionally applied from this single planning pass so both derive from one consistent
// inventory/existing snapshot.
func (r *wekaClusterReconcilerLoop) buildClusterCapacityDriveContainers(ctx context.Context) (driveContainers []*weka.WekaContainer, skipped []string, plan *allocator.CapacityPlan, err error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "buildClusterCapacityDriveContainers")
	defer logger.End()

	cluster := r.cluster

	plan, err = r.planClusterCapacity(ctx)
	if err != nil {
		return nil, nil, nil, err // planClusterCapacity already records the event / returns WaitError
	}

	// Grow existing drive and compute containers in place (no-op when nothing grew) before building the
	// creates, so growth and creation derive from one consistent planning pass.
	if err := r.applyClusterCapacityGrowth(ctx, plan); err != nil {
		return nil, nil, nil, err
	}
	if err := r.applyClusterCapacityComputeGrowth(ctx, plan); err != nil {
		return nil, nil, nil, err
	}

	template := allocator.GetWekaClusterTemplate(cluster.Spec.Dynamic)

	// Drive hugepages must be sized for the planner's PER-CONTAINER core count: the planner derives
	// NumCores from each container's capacity (TLC vs QLC), so a single template-default sizing would
	// under-provision multi-core containers (weka then rejects them as below its minimum memory).
	hpByCores := make(map[int]allocator.ContainerHugepages)

	for _, pc := range plan.Create {
		name := allocator.NewContainerName(weka.WekaContainerModeDrive)
		logger.Info("Building clusterCapacity drive container", "name", name, "node", pc.Node, "type", pc.Type, "tlcGiB", pc.TlcGiB, "qlcGiB", pc.QlcGiB, "cores", pc.NumCores)

		// Per-container template carrying the planner's core count. ClusterTemplate is a value type and
		// Cores is an embedded value field, so this copy does not mutate the shared template.
		perTemplate := template
		perTemplate.Cores.Drive = pc.NumCores

		hp, ok := hpByCores[pc.NumCores]
		if !ok {
			var hpErr error
			hp, hpErr = allocator.GetContainerHugepages(ctx, r.getClient(), perTemplate, cluster, r.containers, weka.WekaContainerModeDrive)
			if hpErr != nil {
				skipped = append(skipped, fmt.Sprintf("role drive hugepages (cores=%d): %s", pc.NumCores, hpErr))
				continue
			}
			hpByCores[pc.NumCores] = hp
		}

		container, err := factory.NewWekaContainerForWekaCluster(cluster, perTemplate, hp, weka.WekaContainerModeDrive, name)
		if err != nil {
			logger.Info("Skipping drive container — failed to build", "name", name, "reason", err)
			skipped = append(skipped, fmt.Sprintf("role drive container %s: %s", name, err))
			continue
		}
		// Override with planner-supplied per-container capacity/ratio. NumCores is carried via
		// perTemplate so it is not overridden here.
		container.Spec.ContainerCapacity = pc.TlcGiB + pc.QlcGiB
		container.Spec.DriveTypesRatio = pc.Ratio
		// Pin every drive container to its planned node via Spec.NodeAffinity (a dedicated field the
		// cluster-level NodeSelector merge never touches). Failure-domain identity is owned by Weka:
		// AUTO mode (no Spec.FailureDomain) makes FD = host; the factory propagates the cluster's
		// label-based Spec.FailureDomain in label mode.
		container.Spec.NodeAffinity = weka.NodeName(pc.Node)
		driveContainers = append(driveContainers, container)
	}

	return driveContainers, skipped, plan, nil
}

// applyClusterCapacityGrowth edits existing drive containers in place to absorb a clusterCapacity
// or driveTypesRatio increase: it bumps Spec.ContainerCapacity / Spec.DriveTypesRatio (and NumCores
// upward) so the wekacontainer reconcile loop allocates and adds the extra virtual drives live, with
// no pod restart (ContainerCapacity is intentionally absent from the pod config hash). It only ever
// grows — shrink is a no-op surfaced as an event by the planner.
//
// It consumes the plan.Grow already produced by planClusterCapacity (called from
// buildClusterCapacityDriveContainers), so growth and creation are applied from one consistent
// planning pass rather than two re-plans against a shifting baseline.
func (r *wekaClusterReconcilerLoop) applyClusterCapacityGrowth(ctx context.Context, plan *allocator.CapacityPlan) error {
	if len(plan.Grow) == 0 {
		return nil
	}
	logger := instrumentation.CurrentSpanLogger(ctx)
	cluster := r.cluster

	byName := make(map[string]*weka.WekaContainer, len(r.containers))
	for _, c := range r.containers {
		byName[c.Name] = c
	}

	for _, g := range plan.Grow {
		c, ok := byName[g.Name]
		if !ok {
			continue
		}
		newCap := g.NewTlcGiB + g.NewQlcGiB
		if newCap <= c.Spec.ContainerCapacity {
			continue // already at/above target (e.g. drive-add still in flight) — never shrink
		}
		c.Spec.ContainerCapacity = newCap
		c.Spec.DriveTypesRatio = allocator.RatioFromCaps(g.NewTlcGiB, g.NewQlcGiB)
		// Capacity growth is applied live (ContainerCapacity is absent from the pod config hash). A
		// NumCores/Hugepages bump, however, changes the pod spec and only takes effect once the pod is
		// recreated — surfaced as a Warning so operators know a restart is required.
		coresChanged := g.NewCores > c.Spec.NumCores
		if coresChanged {
			c.Spec.NumCores = g.NewCores
			if hp, hpErr := r.driveHugepagesForCores(ctx, cluster, g.NewCores, r.containers); hpErr == nil {
				c.Spec.Hugepages = hp.Hugepages
				c.Spec.HugepagesOffset = hp.HugepagesOffset
			} else {
				logger.Info("could not resize hugepages for grown drive container; keeping existing", "name", c.Name, "reason", hpErr)
			}
		}
		logger.Info("Growing clusterCapacity drive container in place", "name", c.Name, "newContainerCapacity", newCap, "tlcGiB", g.NewTlcGiB, "qlcGiB", g.NewQlcGiB, "cores", c.Spec.NumCores)
		if err := r.getClient().Update(ctx, c); err != nil {
			return fmt.Errorf("applyClusterCapacityGrowth: failed to update container %s: %w", c.Name, err)
		}

		if coresChanged {
			r.Recorder.Event(
				c, v1.EventTypeWarning, "CapacityGrowthApplied",
				fmt.Sprintf("applied clusterCapacity growth to drive container (capacity %d GiB, cores %d); the drive spec changed — the pod must be recreated to apply the new cores/hugepages", newCap, c.Spec.NumCores),
			)
		} else {
			r.Recorder.Event(
				c, v1.EventTypeNormal, "CapacityGrowthApplied",
				fmt.Sprintf("applied clusterCapacity growth to drive container live (capacity %d GiB); no restart required", newCap),
			)
		}
	}
	return nil
}

// driveHugepagesForCores recomputes a drive container's hugepages for a specific (per-container) core
// count. clusterCapacity assigns heterogeneous, planner-derived drive cores that the cluster-level
// template default does not represent, so any code that resizes such a container (in-place growth or
// spec-update propagation) must size hugepages from THIS container's cores or weka rejects the
// multi-core drive process for being below its minimum memory.
func (r *wekaClusterReconcilerLoop) driveHugepagesForCores(ctx context.Context, cluster *weka.WekaCluster, cores int, containers []*weka.WekaContainer) (allocator.ContainerHugepages, error) {
	perTemplate := allocator.GetWekaClusterTemplate(cluster.Spec.Dynamic)
	perTemplate.Cores.Drive = cores
	return allocator.GetContainerHugepages(ctx, r.getClient(), perTemplate, cluster, containers, weka.WekaContainerModeDrive)
}

// applyClusterCapacityComputeGrowth bumps this cluster's existing compute containers UP to the planner's
// per-container core target (and resizes their hugepages) when a clusterCapacity increase raised the
// compute sizing. It only ever grows — a lower target leaves existing compute untouched (no shrink),
// mirroring applyClusterCapacityGrowth for drives. The in-place core change requires the pod to be
// recreated to take effect, surfaced as an event. Additional compute containers (count increase) are
// created by the normal role loop in BuildMissingContainers.
func (r *wekaClusterReconcilerLoop) applyClusterCapacityComputeGrowth(ctx context.Context, plan *allocator.CapacityPlan) error {
	if plan.ComputeCores <= 0 {
		return nil
	}
	logger := instrumentation.CurrentSpanLogger(ctx)
	cluster := r.cluster

	// Per-container target by node: with a heterogeneous layout, each existing compute grows only to ITS
	// node's target — a FROZEN compute's target equals its current cores (no-op, no disruption). Fall back
	// to the uniform plan.ComputeCores for every node when no layout is present (legacy/uniform case).
	targetByNode := make(map[string]int, len(plan.ComputeLayout))
	for _, e := range plan.ComputeLayout {
		targetByNode[e.Node] = e.NumCores
	}
	totalCount := plan.ComputeContainers

	for _, c := range r.containers {
		if c.Spec.Mode != weka.WekaContainerModeCompute {
			continue
		}
		if unhealthy, _, _ := utils.IsUnhealthy(ctx, c); unhealthy { //nolint:errcheck // intentional
			continue
		}
		target := plan.ComputeCores
		if len(plan.ComputeLayout) > 0 {
			node := string(c.GetNodeAffinity())
			t, ok := targetByNode[node]
			if !ok {
				continue // not in the layout (e.g. unpinned/unknown node) — leave untouched
			}
			target = t
		}
		if c.Spec.NumCores >= target {
			continue // already at/above its target — never shrink (frozen computes land here)
		}
		c.Spec.NumCores = target
		if hp, hpErr := r.computeHugepagesForCores(ctx, cluster, target, totalCount, r.containers); hpErr == nil {
			c.Spec.Hugepages = hp.Hugepages
			c.Spec.HugepagesOffset = hp.HugepagesOffset
		} else {
			logger.Info("could not resize hugepages for grown compute container; keeping existing", "name", c.Name, "reason", hpErr)
		}
		logger.Info("Growing clusterCapacity compute container in place", "name", c.Name, "cores", c.Spec.NumCores)
		if err := r.getClient().Update(ctx, c); err != nil {
			return fmt.Errorf("applyClusterCapacityComputeGrowth: failed to update container %s: %w", c.Name, err)
		}
		r.Recorder.Event(
			c, v1.EventTypeWarning, "CapacityGrowthApplied",
			fmt.Sprintf("applied clusterCapacity compute growth to container (cores %d); the compute spec changed — the pod must be recreated to apply the new cores/hugepages", c.Spec.NumCores),
		)
	}
	return nil
}

// computeHugepagesForCores recomputes a compute container's hugepages for a specific (per-container)
// core count, used when growing clusterCapacity compute containers in place. Mirrors
// driveHugepagesForCores: the cluster-level template default does not represent the planner-derived
// compute cores, so hugepages must be sized from THIS container's cores.
func (r *wekaClusterReconcilerLoop) computeHugepagesForCores(ctx context.Context, cluster *weka.WekaCluster, cores, computeContainers int, wekaContainers []*weka.WekaContainer) (allocator.ContainerHugepages, error) {
	perTemplate := allocator.GetWekaClusterTemplate(cluster.Spec.Dynamic)
	perTemplate.Cores.Compute = cores
	if computeContainers > 0 {
		perTemplate.Containers.Compute = computeContainers // planner-derived count, not the min-default 5
	}
	return allocator.GetContainerHugepages(ctx, r.getClient(), perTemplate, cluster, wekaContainers, weka.WekaContainerModeCompute)
}

// GarbageCollectUnschedulableDriveContainers deletes clusterCapacity drive containers that have never
// been scheduled (Status.NodeAffinity == "") longer than the configured timeout, so the planner can
// re-place that capacity on a node that can actually host it on the next reconcile. It targets only
// never-scheduled containers: an established (once-scheduled) drive container keeps Status.NodeAffinity
// set and is skipped here.
//
// TODO: move this GC into the wekacontainer reconciler — each drive container can evaluate its own
// unscheduled age there (it already has its pod loaded and a self-delete path), avoiding the
// cluster-level scan. Kept at the wekacluster level for now.
func (r *wekaClusterReconcilerLoop) GarbageCollectUnschedulableDriveContainers(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	timeout := config.Config.UnschedulableDriveContainerGCTimeout

	for _, c := range r.containers {
		if c.Spec.Mode != weka.WekaContainerModeDrive || !c.HasContainerCapacity() {
			continue
		}
		if c.Status.NodeAffinity != "" || c.IsMarkedForDeletion() || c.IsDeletingState() || c.IsDestroyingState() {
			continue // scheduled, or already going away
		}
		age := time.Since(c.CreationTimestamp.Time)
		if age < timeout {
			continue
		}

		r.Recorder.Event(
			c, v1.EventTypeWarning, "UnschedulableDriveContainer",
			fmt.Sprintf("drive container unscheduled for %s (> %s); deleting so capacity can be re-placed", age.Round(time.Second), timeout),
		)

		logger.Info("Deleting long-unschedulable clusterCapacity drive container", "name", c.Name, "age", age.String())

		if err := services.SetContainerStateDeleting(ctx, c, r.getClient()); err != nil {
			return fmt.Errorf("GarbageCollectUnschedulableDriveContainers: failed to delete container %s: %w", c.Name, err)
		}
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) BuildMissingContainers(ctx context.Context) ([]*weka.WekaContainer, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "BuildMissingContainers")
	defer logger.End()

	cluster := r.cluster
	existingContainers := r.containers

	nums := allocator.GetWekaContainerNumbers(cluster.Spec.Dynamic)

	containers := make([]*weka.WekaContainer, 0)
	var skippedReasons []string

	clusterReady := meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondClusterCreated)

	// Check if telemetry exports are configured
	hasTelemetryExports := cluster.Spec.Telemetry != nil && len(cluster.Spec.Telemetry.Exports) > 0

	// clusterCapacity mode: planner builds DRIVE containers; remaining roles use the normal path.
	clusterCapacityMode := cluster.Spec.Dynamic != nil && cluster.Spec.Dynamic.UsesClusterCapacity()

	// computeCores derived from clusterCapacity planner (1:1 TLC drive cores rule).
	// 0 means "use template default" (non-clusterCapacity path).
	derivedComputeCores := 0
	// derivedComputeNodes are the planner-reserved compute nodes (post-drive headroom). New compute
	// containers are pinned to them so they never schedule onto a drive-pinned node lacking the
	// post-drive hugepages to host both (which would leave the pinned drive pod unschedulable).
	var derivedComputeNodes []string
	// derivedComputeLayout is the planner's PER-CONTAINER compute layout (heterogeneous when an existing
	// compute is frozen at its current size and the deficit is covered by compensating containers). When
	// non-empty it is the authoritative source for compute creation: each net-new entry is built on its
	// own node with its own cores/hugepages. Empty falls back to the uniform derivedComputeCores path.
	var derivedComputeLayout []allocator.ComputeContainerSpec

	if clusterCapacityMode {
		driveContainers, skipped, plan, err := r.buildClusterCapacityDriveContainers(ctx)
		if err != nil {
			return nil, err
		}
		containers = append(containers, driveContainers...)
		skippedReasons = append(skippedReasons, skipped...)

		// Compute container count and per-container cores are computed by the planner from the POST-drive
		// per-node core/hugepage headroom (1:1 with TLC drive cores, bounded by real per-node headroom).
		// Advisory warnings are emitted as plan.Warnings in planClusterCapacity; an unsatisfiable 1:1 was
		// already returned as a WaitError above (so no drive/compute container was created or grown).
		derivedComputeCores = plan.ComputeCores
		nums.Compute = plan.ComputeContainers
		derivedComputeNodes = plan.ComputeNodes
		derivedComputeLayout = plan.ComputeLayout
	}

	for _, role := range []string{"drive", "compute", "s3", "envoy", "nfs", "smbw", "telemetry", "data-services"} {
		// Drive containers are handled by the planner branch above in clusterCapacity mode.
		if clusterCapacityMode && role == weka.WekaContainerModeDrive {
			continue
		}

		// Compute in clusterCapacity mode with a per-container layout: create each missing layout entry on
		// its own node with its own cores/hugepages (heterogeneous when a frozen compute is being
		// compensated). This supersedes the uniform compute path below.
		if clusterCapacityMode && role == weka.WekaContainerModeCompute && len(derivedComputeLayout) > 0 {
			built, skipped := r.buildClusterCapacityComputeContainers(ctx, derivedComputeLayout, existingContainers)
			containers = append(containers, built...)
			skippedReasons = append(skippedReasons, skipped...)
			continue
		}

		var numContainers int

		if clusterReady {
			switch role {
			case "compute":
				numContainers = nums.Compute
			case "drive":
				numContainers = nums.Drive
			case "s3":
				numContainers = nums.S3
			case "envoy":
				numContainers = nums.S3 // Envoy containers are 1-per-S3 container
			case "nfs":
				numContainers = nums.Nfs
			case "smbw":
				numContainers = nums.Smbw
			case "telemetry":
				// Telemetry containers are created 1-per-compute container when telemetry exports are configured
				if hasTelemetryExports {
					numContainers = nums.Compute
				} else {
					numContainers = 0
				}
			case "data-services":
				numContainers = nums.DataServices
				if numContainers > 0 && cluster.Spec.Dynamic.GetDataServicesFeCores() != 0 {
					_ = r.RecordEventThrottled(v1.EventTypeWarning, "DataServicesValidationFailed", //nolint:errcheck // event recording errors are intentionally ignored
						"dataServicesContainers > 0 requires dataServicesFeCores to be explicitly set to 0; skipping data-services container creation", time.Minute)
					numContainers = 0
				}
			}
		} else {
			switch role {
			case "compute":
				numContainers = util.GetMinValue(nums.Compute, config.Consts.FormClusterMaxComputeContainers)
			case "drive":
				numContainers = util.GetMinValue(nums.Drive, config.Consts.FormClusterMaxDriveContainers)
			default:
				continue
			}
		}

		currentCount := 0
		for _, container := range existingContainers {
			if unhealthy, _, _ := utils.IsUnhealthy(ctx, container); unhealthy { //nolint:errcheck // error is intentionally ignored
				continue // we don't care why it's unhealthy, but if it is - we do not account for it and replacement will be scheduled
			}
			if container.Spec.Mode == role {
				currentCount++
			}
		}

		if currentCount >= numContainers {
			continue
		}

		template := allocator.GetWekaClusterTemplate(cluster.Spec.Dynamic)

		// nodePins, when non-empty, pins the containers about to be created to specific nodes (consumed
		// one-per-container below). In clusterCapacity mode we also inject the 1:1-derived compute cores
		// and pin compute to the planner-reserved nodes — symmetric with drives — so it never lands on a
		// drive-pinned node lacking the post-drive hugepages to host both.
		var nodePins []string
		if clusterCapacityMode && role == weka.WekaContainerModeCompute && derivedComputeCores > 0 {
			template.Cores.Compute = derivedComputeCores
			if nums.Compute > 0 {
				template.Containers.Compute = nums.Compute // planner-derived count, not the min-default 5
			}
			nodePins = unusedComputeNodes(existingContainers, derivedComputeNodes)
		}

		hp, err := allocator.GetContainerHugepages(ctx, r.getClient(), template, cluster, r.containers, role)
		if err != nil {
			logger.Info("Skipping role — hugepages not available yet", "role", role, "reason", err)
			skippedReasons = append(skippedReasons, fmt.Sprintf("role %s hugepages: %s", role, err))
			continue
		}

		for i := currentCount; i < numContainers; i++ {
			name := allocator.NewContainerName(role)
			logger.Info("Building missing container", "role", role, "name", name)

			container, err := factory.NewWekaContainerForWekaCluster(cluster, template, hp, role, name)
			if err != nil {
				logger.Info("Skipping container — failed to build", "role", role, "name", name, "reason", err)
				skippedReasons = append(skippedReasons, fmt.Sprintf("role %s container %s: %s", role, name, err))
				continue
			}
			if len(nodePins) > 0 {
				container.Spec.NodeAffinity = weka.NodeName(nodePins[0])
				nodePins = nodePins[1:]
			}
			containers = append(containers, container)
		}
	}

	if len(skippedReasons) > 0 {
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "ContainersBuildPending", //nolint:errcheck // event is best effort, if recording fails, we don't want to log the reason
			strings.Join(skippedReasons, "; "), time.Minute)
	}

	return containers, nil
}

// buildClusterCapacityComputeContainers creates the compute containers in the planner's per-container
// layout that do not yet exist on their pinned node. Each entry carries its OWN cores/hugepages, so a
// heterogeneous layout (frozen existing compute + grown others + extra compensating containers) is
// realized correctly — unlike the uniform path which applies one core count to every container. Nodes
// already hosting a compute container are skipped here (in-place growth handles their resizing); only the
// missing entries (net-new and compensating containers) are built. Hugepages are sized authoritatively
// per distinct core count via computeHugepagesForCores (cached), using the layout's total container count.
func (r *wekaClusterReconcilerLoop) buildClusterCapacityComputeContainers(ctx context.Context, layout []allocator.ComputeContainerSpec, existing []*weka.WekaContainer) (built []*weka.WekaContainer, skippedReasons []string) {
	logger := instrumentation.CurrentSpanLogger(ctx)
	cluster := r.cluster

	occupied := make(map[string]struct{})
	for _, c := range existing {
		if c.Spec.Mode == weka.WekaContainerModeCompute {
			if n := string(c.GetNodeAffinity()); n != "" {
				occupied[n] = struct{}{}
			}
		}
	}

	totalCount := len(layout)
	hpByCores := make(map[int]allocator.ContainerHugepages)
	for _, entry := range layout {
		if entry.Node == "" {
			continue
		}
		if _, taken := occupied[entry.Node]; taken {
			continue // an existing compute already lives here; in-place growth resizes it
		}

		hp, ok := hpByCores[entry.NumCores]
		if !ok {
			h, err := r.computeHugepagesForCores(ctx, cluster, entry.NumCores, totalCount, r.containers)
			if err != nil {
				logger.Info("Skipping compute container — hugepages not available yet", "node", entry.Node, "cores", entry.NumCores, "reason", err)
				skippedReasons = append(skippedReasons, fmt.Sprintf("compute node %s hugepages: %s", entry.Node, err))
				continue
			}
			hp = h
			hpByCores[entry.NumCores] = h
		}

		template := allocator.GetWekaClusterTemplate(cluster.Spec.Dynamic)
		template.Cores.Compute = entry.NumCores
		template.Containers.Compute = totalCount

		name := allocator.NewContainerName(weka.WekaContainerModeCompute)
		logger.Info("Building missing clusterCapacity compute container", "name", name, "node", entry.Node, "cores", entry.NumCores)
		container, err := factory.NewWekaContainerForWekaCluster(cluster, template, hp, weka.WekaContainerModeCompute, name)
		if err != nil {
			logger.Info("Skipping compute container — failed to build", "name", name, "node", entry.Node, "reason", err)
			skippedReasons = append(skippedReasons, fmt.Sprintf("compute container %s: %s", name, err))
			continue
		}
		container.Spec.NodeAffinity = weka.NodeName(entry.Node)
		built = append(built, container)
	}
	return built, skippedReasons
}

// unusedComputeNodes returns the planner-reserved compute nodes that are not already hosting a compute
// container, preserving the planner's order. New compute containers pin to these (one each) so they never
// schedule onto a drive-pinned node lacking the post-drive hugepages to host both.
func unusedComputeNodes(existing []*weka.WekaContainer, planned []string) []string {
	used := make(map[string]struct{})
	for _, c := range existing {
		if c.Spec.Mode == weka.WekaContainerModeCompute {
			if n := string(c.GetNodeAffinity()); n != "" {
				used[n] = struct{}{}
			}
		}
	}
	free := make([]string, 0, len(planned))
	for _, n := range planned {
		if _, taken := used[n]; !taken {
			free = append(free, n)
		}
	}
	return free
}

func (r *wekaClusterReconcilerLoop) updateContainersOnNodeSelectorMismatch(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	kubeService := kubernetes.NewKubeService(r.getClient())
	var toDelete []*weka.WekaContainer
	var toUpdate []*weka.WekaContainer
	maxBackendsDeletePerReconcile := config.Consts.MaxContainersDeletedOnSelectorMismatch

	cluster := r.cluster

	for _, container := range r.containers {
		// do not destroy more than 4 containers per reconcile
		if len(toDelete) >= maxBackendsDeletePerReconcile {
			break
		}

		if container.IsMarkedForDeletion() || container.IsDestroyingState() || container.IsDeletingState() {
			continue
		}

		if container.Spec.Mode == weka.WekaContainerModeEnvoy {
			continue
		}

		nodeName := container.GetNodeAffinity()
		if nodeName == "" {
			continue
		}

		node, err := kubeService.GetNode(ctx, types.NodeName(nodeName))
		if err != nil {
			if apierrors.IsNotFound(err) {
				// should be handled by container reconciler
				continue
			}
			return err
		}

		if !util.NodeSelectorMatchesNode(container.Spec.NodeSelector, node) {
			if util.NodeSelectorMatchesNode(cluster.Spec.NodeSelector, node) {
				toUpdate = append(toUpdate, container)
			} else {
				toDelete = append(toDelete, container)
			}
		}
	}

	if len(toDelete) == 0 && len(toUpdate) == 0 {
		return nil
	}

	logger.Info("Updating containers with node selector mismatch", "toUpdate", len(toUpdate))
	updateErr := workers.ProcessConcurrently(ctx, toUpdate, maxBackendsDeletePerReconcile, func(ctx context.Context, container *weka.WekaContainer) error {
		// clusterCapacity drive containers pin to their node via Spec.NodeAffinity (a separate field
		// this NodeSelector replace never touches), so no hostname preservation is needed here.
		patch := []map[string]any{
			{
				"op":    "replace",
				"path":  "/spec/nodeSelector",
				"value": cluster.Spec.NodeSelector,
			},
		}
		r.Recorder.Event(container, v1.EventTypeNormal, "NodeSelectorMismatch", "Node selector mismatch, updating container nodeSelector")
		patchBytes, err := json.Marshal(patch)
		if err != nil {
			return fmt.Errorf("failed to marshal patch for container %s: %w", container.Name, err)
		}

		return errors.Wrap(
			// use JSONPatchType to fully replace nodeSelector, not merge, for cases when a field is removed
			r.getClient().Patch(ctx, container, client.RawPatch(types.JSONPatchType, patchBytes)),
			fmt.Sprintf("failed to update container state %s: %v", container.Name, err),
		)
	}).AsError()

	logger.Info("Deleting containers with node selector mismatch", "toDelete", len(toDelete))
	deleteErr := workers.ProcessConcurrently(ctx, toDelete, maxBackendsDeletePerReconcile, func(ctx context.Context, container *weka.WekaContainer) error {
		r.Recorder.Event(container, v1.EventTypeNormal, "NodeSelectorMismatch", "Node selector mismatch, deleting container")

		return errors.Wrap(
			services.SetContainerStateDeleting(ctx, container, r.getClient()),
			fmt.Sprintf("failed to update container state %s", container.Name),
		)
	}).AsError()

	return &workers.MultiError{Errors: []error{updateErr, deleteErr}}
}
