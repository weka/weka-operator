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

	"github.com/weka/weka-operator/internal/capacityplanner"
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
			Run: loop.GarbageCollectUnschedulablePlannerContainers,
			Predicates: lifecycle.Predicates{
				func() bool {
					// Covers drive and compute containers under either planner mode; the callee narrows further
					// to pinned containers with a confirmed scheduling failure.
					return loop.plannerManaged()
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
			Name: "EnsureAwsTerminationLifecycleHook",
			Run:  loop.ensureAwsTerminationLifecycleHook,
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

// hasWekaFinalizer reports whether the cluster carries the weka finalizer under either its current
// name (consts.WekaFinalizer) or its pre-v1.15.0 name (consts.WekaFinalizerDeprecated).
func hasWekaFinalizer(o client.Object) bool {
	return controllerutil.ContainsFinalizer(o, consts.WekaFinalizer) ||
		controllerutil.ContainsFinalizer(o, consts.WekaFinalizerDeprecated)
}

// isUninitializedCluster reports whether the cluster has genuinely never been initialized, and so is
// safe to reset the status of.
//
// Two independent signals, either of which vetoes the reset:
//   - a weka finalizer under either name means InitState has already run for this cluster;
//   - a non-empty Status.ClusterID is positive proof the cluster was formed. It survives
//     InitStatus() (which clears only Conditions and Status), so it remains trustworthy even if the
//     conditions were lost for some other reason — a restored backup, a hand-edited status.
func isUninitializedCluster(cluster *weka.WekaCluster) bool {
	return !hasWekaFinalizer(cluster) && cluster.Status.ClusterID == ""
}

func (r *wekaClusterReconcilerLoop) InitState(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	wekaCluster := r.cluster
	if !controllerutil.ContainsFinalizer(wekaCluster, consts.WekaFinalizer) {

		// Reset the status only for a cluster that has genuinely never been initialized. Reaching here
		// means only that the current finalizer is absent, which is also true of every cluster created
		// before the v1.15.0 finalizer rename — those must be migrated to the current finalizer below
		// WITHOUT having their status wiped. See doc/dev/finalizer-bug.md.
		if isUninitializedCluster(wekaCluster) {
			wekaCluster.Status.InitStatus()
			wekaCluster.Status.LastAppliedImage = wekaCluster.Spec.Image
			wekaCluster.Status.LastAppliedPodConfigHash = CalcClusterPodConfigVersion(&wekaCluster.Spec)

			err := r.getClient().Status().Update(ctx, wekaCluster)
			if err != nil {
				logger.Error(err, "failed to init states")
			}
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

// GarbageCollectUnschedulablePlannerContainers deletes pinned planner containers with a confirmed
// scheduling failure past the GC timeout, so the planner can re-place their capacity. Only containers
// that never bound (Status.NodeAffinity == "") are reaped; one that ran carries cluster state.
// Compute is reaped too: autoKeptCompute counts its cores, so leaving it under-serves the ratio.
//
// TODO: move this GC into the wekacontainer reconciler, which already has its own pod loaded and a
// self-delete path. Kept at the wekacluster level for now.
func (r *wekaClusterReconcilerLoop) GarbageCollectUnschedulablePlannerContainers(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	timeout := config.Config.UnschedulablePlannerContainerGCTimeout

	// Each container is independent, so a failure on one must not stop the GC from reaping the rest —
	// per-container errors accumulate instead of aborting the pass.
	results := workers.ProcessConcurrently(ctx, r.containers, 32, func(ctx context.Context, c *weka.WekaContainer) error {
		if c.IsMarkedForDeletion() || c.IsDeletingState() || c.IsDestroyingState() {
			return nil // already going away
		}
		if c.Status.NodeAffinity != "" {
			// Already bound at some point; only never-bound containers are reaped here (see doc above).
			return nil
		}

		if c.Spec.NodeAffinity == "" {
			// Only node-pinned containers need this GC — pinning is what the planner does to every container it
			// places. An unpinned container is deletePodIfUnschedulable's case (flow_active_state.go): the
			// scheduler replaces it elsewhere, so the two reapers stay disjoint on this boundary.
			return nil
		}

		var eventReason, kindDesc string
		switch c.Spec.Mode {
		case weka.WekaContainerModeDrive:
			eventReason, kindDesc = "UnschedulableDriveContainer", "drive"
		case weka.WekaContainerModeCompute:
			eventReason, kindDesc = "UnschedulableComputeContainer", "compute"
		default:
			// Ad-hoc and operational containers (AdhocOp, DriversLoader, Discovery) are node-pinned too and
			// have their own reapers; mode is the only thing keeping them out of this one.
			return nil
		}

		// Requires a confirmed scheduling failure, not merely an unevaluated pod: a drive pod can legitimately
		// sit Pending past the timeout during a DKMS build, and reaping it then would only restart the build.
		pod := &v1.Pod{}
		if err := r.getClient().Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: c.Name}, pod); err != nil {
			if apierrors.IsNotFound(err) {
				return nil // no pod yet: nothing confirms unschedulability
			}
			return fmt.Errorf("GarbageCollectUnschedulablePlannerContainers: failed to get pod for container %s: %w", c.Name, err)
		}
		cond := utils.PodUnschedulableCondition(pod)
		if cond == nil {
			return nil
		}

		// The timeout runs from the scheduler's verdict, not container creation, so reaping never races a
		// transient shortage the next scheduling attempt would clear. Matches deletePodIfUnschedulable
		// (flow_active_state.go), the pod-level reaper for unpinned containers.
		unschedulableFor := time.Since(cond.LastTransitionTime.Time)
		if unschedulableFor < timeout {
			return nil
		}

		// cond.Message is the scheduler's own per-node explanation ("0/8 nodes are available: 2 Insufficient
		// hugepages-2Mi"). It is the only actionable text available: cond.Reason is always the literal
		// "Unschedulable" here, so reporting that would say nothing the event's own reason does not.
		message := fmt.Sprintf("%s container unschedulable for %s (> %s): %s; deleting so capacity can be re-placed",
			kindDesc, unschedulableFor.Round(time.Second), timeout, cond.Message)
		r.Recorder.Event(c, v1.EventTypeWarning, eventReason, message)

		logger.Info("Deleting long-unschedulable planner container", "name", c.Name, "kind", kindDesc,
			"unschedulableFor", unschedulableFor.String(), "schedulerMessage", cond.Message)

		if err := services.SetContainerStateDeleting(ctx, c, r.getClient()); err != nil {
			return fmt.Errorf("GarbageCollectUnschedulablePlannerContainers: failed to delete container %s: %w", c.Name, err)
		}
		return nil
	})

	return results.AsError()
}

// BuildMissingContainers returns the containers still needed to reach the cluster's desired role counts:
// drive containers come from the active capacity-planner mode (clusterCapacity/auto full drives) when one applies,
// otherwise from the normal per-role numbers; compute and other roles always use the normal path, pinned
// to the planner's reserved nodes/layout when a planner mode is active.
func (r *wekaClusterReconcilerLoop) BuildMissingContainers(ctx context.Context) ([]*weka.WekaContainer, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "BuildMissingContainers")
	defer logger.End()

	cluster := r.cluster
	nums := allocator.GetWekaContainerNumbers(cluster.Spec.Dynamic)

	containers := make([]*weka.WekaContainer, 0)
	var skippedReasons []string

	// The planner owns drive containers in both its modes — clusterCapacity's uniform whole-cluster target, and
	// auto full drives' one node-pinned container per eligible node sized from that node's own signed drives —
	// and supplies the compute layout with them. Every other role takes the count-based path.
	mode, plannerManaged := plannerSizingMode(&cluster.Spec)

	// derivedComputeCores: clusterCapacity's post-drive 1:1-with-TLC-drive-cores rule; 0 = template default.
	derivedComputeCores := 0
	// derivedComputeNodes are the planner-reserved compute nodes, so new compute never lands on a drive-pinned
	// node lacking the post-drive hugepages to host both.
	var derivedComputeNodes []string
	// derivedComputeLayout is the planner's per-container compute layout; non-empty is authoritative over the
	// uniform derivedComputeCores path below.
	var derivedComputeLayout []capacityplanner.ComputeContainerSpec

	if plannerManaged {
		driveContainers, skipped, plan, err := r.buildPlannerDriveContainers(ctx, mode)
		if err != nil {
			return nil, err
		}
		containers = append(containers, driveContainers...)
		skippedReasons = append(skippedReasons, skipped...)
		nums.Compute = plan.ComputeContainers
		derivedComputeNodes = plan.ComputeNodes
		derivedComputeLayout = plan.ComputeLayout
		if mode == sizingClusterCapacity {
			derivedComputeCores = plan.ComputeCores
		}
	}

	want := r.desiredRoleCounts(nums)

	for _, role := range wekaContainerRoles {
		if plannerManaged && role == weka.WekaContainerModeDrive {
			continue // built from plan.Create above
		}
		// A per-container compute layout supersedes the uniform count-based path: each missing entry is created
		// on its own node with its own cores and hugepages.
		if plannerManaged && role == weka.WekaContainerModeCompute && len(derivedComputeLayout) > 0 {
			built, skipped := r.buildPlannerComputeContainers(ctx, derivedComputeLayout, r.containers)
			containers = append(containers, built...)
			skippedReasons = append(skippedReasons, skipped...)
			continue
		}

		have := countHealthyByRole(ctx, r.containers, role)
		if have >= want[role] {
			continue
		}

		template := allocator.GetWekaClusterTemplate(cluster.Spec.Dynamic)

		// clusterCapacity's uniform compute path: inject the planner-derived sizing and pin new containers to
		// the nodes it reserved, symmetric with drives. Reached only when the planner produced no per-container
		// layout (its steady-state and no-op plans), since the layout branch above takes precedence.
		var nodePins []string
		if mode == sizingClusterCapacity && role == weka.WekaContainerModeCompute && derivedComputeCores > 0 {
			template.Cores.Compute = derivedComputeCores
			if nums.Compute > 0 {
				template.Containers.Compute = nums.Compute // planner-derived count, not the min-default
			}
			nodePins = unusedComputeNodes(r.containers, derivedComputeNodes)
		}

		built, skipped, err := r.buildRoleContainers(ctx, role, have, want[role], template, nodePins)
		if err != nil {
			return nil, err
		}
		containers = append(containers, built...)
		skippedReasons = append(skippedReasons, skipped...)
	}

	if len(skippedReasons) > 0 {
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "ContainersBuildPending", //nolint:errcheck // event is best effort, if recording fails, we don't want to log the reason
			strings.Join(skippedReasons, "; "), time.Minute)
	}

	return containers, nil
}

// wekaContainerRoles is the order roles are built in; drive and compute come first so the planner-managed
// branches are decided before any count-based role consumes headroom.
var wekaContainerRoles = []string{"drive", "compute", "s3", "envoy", "nfs", "smbw", "telemetry", "data-services"}

// desiredRoleCounts is how many containers each role should have. Before the cluster is formed only drive and
// compute are built, and both are capped at the form-cluster maximum — the rest wait until it is up.
func (r *wekaClusterReconcilerLoop) desiredRoleCounts(nums allocator.IntPerWekaRole) map[string]int {
	cluster := r.cluster
	if !meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondClusterCreated) {
		return map[string]int{
			"compute": util.GetMinValue(nums.Compute, config.Consts.FormClusterMaxComputeContainers),
			"drive":   util.GetMinValue(nums.Drive, config.Consts.FormClusterMaxDriveContainers),
		}
	}

	want := map[string]int{
		"compute": nums.Compute,
		"drive":   nums.Drive,
		"s3":      nums.S3,
		"envoy":   nums.S3, // one envoy per S3 container
		"nfs":     nums.Nfs,
		"smbw":    nums.Smbw,
	}
	// One telemetry container per compute container, but only when exports are configured.
	if cluster.Spec.Telemetry != nil && len(cluster.Spec.Telemetry.Exports) > 0 {
		want["telemetry"] = nums.Compute
	}
	if n := nums.DataServices; n > 0 {
		if cluster.Spec.Dynamic.GetDataServicesFeCores() != 0 {
			_ = r.RecordEventThrottled(v1.EventTypeWarning, "DataServicesValidationFailed", //nolint:errcheck // event recording errors are intentionally ignored
				"dataServicesContainers > 0 requires dataServicesFeCores to be explicitly set to 0; skipping data-services container creation", time.Minute)
		} else {
			want["data-services"] = n
		}
	}
	return want
}

// countHealthyByRole counts this cluster's healthy containers of one role. Unhealthy ones are excluded
// whatever the reason, so a replacement gets scheduled for them.
func countHealthyByRole(ctx context.Context, containers []*weka.WekaContainer, role string) int {
	count := 0
	for _, c := range containers {
		if unhealthy, _, _ := utils.IsUnhealthy(ctx, c); unhealthy { //nolint:errcheck // error is intentionally ignored
			continue
		}
		if c.Spec.Mode == role {
			count++
		}
	}
	return count
}

// buildRoleContainers is the count-based path for one role: build (want - have) containers from the template.
// nodePins, when non-empty, pins them to specific nodes, one each, in order. A non-nil error is a
// programming-invariant violation (e.g. GetContainerHugepages called for planner-managed compute) that must
// fail the reconcile rather than be folded into skipped, which reads as an ordinary transient state.
func (r *wekaClusterReconcilerLoop) buildRoleContainers(
	ctx context.Context, role string, have, want int, template allocator.ClusterTemplate, nodePins []string, //nolint:gocritic // hugeParam: ClusterTemplate is passed by value intentionally
) (built []*weka.WekaContainer, skipped []string, err error) {
	logger := instrumentation.CurrentSpanLogger(ctx)
	cluster := r.cluster

	hp, err := allocator.GetContainerHugepages(ctx, r.getClient(), template, cluster, r.containers, role)
	if err != nil {
		if errors.Is(err, allocator.ErrPlannerManagedComputeHugepages) {
			return nil, nil, err
		}
		logger.Info("Skipping role — hugepages not available yet", "role", role, "reason", err)
		return nil, []string{fmt.Sprintf("role %s hugepages: %s", role, err)}, nil
	}

	for range want - have {
		name := allocator.NewContainerName(role)
		logger.Info("Building missing container", "role", role, "name", name)

		container, buildErr := factory.NewWekaContainerForWekaCluster(cluster, template, hp, role, name)
		if buildErr != nil {
			logger.Info("Skipping container — failed to build", "role", role, "name", name, "reason", buildErr)
			skipped = append(skipped, fmt.Sprintf("role %s container %s: %s", role, name, buildErr))
			continue
		}
		if len(nodePins) > 0 {
			container.Spec.NodeAffinity = weka.NodeName(nodePins[0])
			nodePins = nodePins[1:]
		}
		built = append(built, container)
	}
	return built, skipped, nil
}

// buildPlannerComputeContainers creates the compute containers in the planner's per-container layout that
// don't yet exist on their pinned node, each with its own cores and hugepages from the layout entry. Nodes
// already hosting a compute container are skipped — in-place growth resizes them instead.
func (r *wekaClusterReconcilerLoop) buildPlannerComputeContainers(ctx context.Context, layout []capacityplanner.ComputeContainerSpec, existing []*weka.WekaContainer) (built []*weka.WekaContainer, skippedReasons []string) {
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

	for _, entry := range layout {
		if entry.Node == "" {
			continue
		}
		if _, taken := occupied[entry.Node]; taken {
			continue // an existing compute already lives here; in-place growth resizes it
		}
		// Hugepages come straight from the layout entry: it's the planner's own figure, so what the node-fit
		// gate reserved is what the pod requests. Recomputing would need every drive container's
		// Status.Allocations, which is still empty for containers created moments ago in this pass.
		if entry.HugepagesMiB <= 0 {
			logger.Info("Skipping compute container — planner supplied no hugepages figure", "node", entry.Node, "cores", entry.NumCores)
			skippedReasons = append(skippedReasons, fmt.Sprintf("compute node %s: planner supplied no hugepages figure", entry.Node))
			continue
		}
		hp := allocator.ComputeHugepagesFromPlan(cluster, entry.HugepagesMiB, entry.NumCores)

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
