// package wekacluster contains the reconciliation logic for WekaCluster resources
package wekacluster

import (
	"context"
	"encoding/json"
	"fmt"
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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	wekaCluster := r.cluster
	if !controllerutil.ContainsFinalizer(wekaCluster, consts.WekaFinalizer) {

		wekaCluster.Status.InitStatus()
		wekaCluster.Status.LastAppliedImage = wekaCluster.Spec.Image

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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

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
		_ = r.RecordEvent(v1.EventTypeWarning, "AllocateClusterRangeError", allocateRangeErr.Error())
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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster

	// Validate driveTypesRatio before creating containers
	if err := r.ValidateDriveTypesRatio(ctx); err != nil {
		logger.Error(err, "Invalid driveTypesRatio configuration")
		return err
	}

	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		keys := make([]string, 0, len(allocator.WekaClusterTemplates))
		for k := range allocator.WekaClusterTemplates {
			keys = append(keys, k)
		}
		err := errors.New("template not found")
		logger.Error(err, "", "template", cluster.Spec.Template, "keys", keys)
		return err
	}

	//newContainersLimit := config.Consts.NewContainersLimit
	missingContainers, err := BuildMissingContainers(ctx, cluster, template, r.containers)
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
		if err != nil && len(cluster.Spec.ExpandEndpoints) != 0 && allowExpansion { //TO
			joinIps = cluster.Spec.ExpandEndpoints
		} else {
			if err != nil {
				logger.Error(err, "Failed to get join ips")
				return err
			}
		}
	}

	for _, container := range missingContainers {
		if len(joinIps) != 0 {
			container.Spec.JoinIps = joinIps
		}
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

func BuildMissingContainers(ctx context.Context, cluster *weka.WekaCluster, template allocator.ClusterTemplate, existingContainers []*weka.WekaContainer) ([]*weka.WekaContainer, error) {
	_, logger, end := instrumentation.GetLogSpan(ctx, "BuildMissingContainers")
	defer end()

	containers := make([]*weka.WekaContainer, 0)

	clusterReady := meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondClusterCreated)

	existingByRole := map[string]int{}
	totalByrole := map[string]int{}

	for _, container := range existingContainers {
		existingByRole[container.Spec.Mode]++
	}

	// Check if telemetry exports are configured
	hasTelemetryExports := cluster.Spec.Telemetry != nil && len(cluster.Spec.Telemetry.Exports) > 0

	for _, role := range []string{"drive", "compute", "s3", "envoy", "nfs", "telemetry"} {
		var numContainers int

		if clusterReady {
			switch role {
			case "compute":
				numContainers = template.ComputeContainers
			case "drive":
				numContainers = template.DriveContainers
			case "s3":
				numContainers = template.S3Containers
			case "envoy":
				numContainers = template.S3Containers
			case "nfs":
				numContainers = template.NfsContainers
			case "telemetry":
				// Telemetry containers are created 1-per-compute container when telemetry exports are configured
				if hasTelemetryExports {
					numContainers = template.ComputeContainers
				} else {
					numContainers = 0
				}
			}
		} else {
			switch role {
			case "compute":
				numContainers = util.GetMinValue(template.ComputeContainers, config.Consts.FormClusterMaxComputeContainers)
			case "drive":
				numContainers = util.GetMinValue(template.DriveContainers, config.Consts.FormClusterMaxDriveContainers)
			default:
				continue
			}
		}

		currentCount := 0
		for _, container := range existingContainers {
			if unhealthy, _, _ := utils.IsUnhealthy(ctx, container); unhealthy {
				continue // we don't care why it's unhealthy, but if it is - we do not account for it and replacement will be scheduled
			}
			if container.Spec.Mode == role {
				currentCount++
			}
		}

		toCreateNum := numContainers - currentCount
		totalByrole[role] = existingByRole[role] + toCreateNum
		if role == "envoy" {
			numContainers = totalByrole["s3"]
		}
		if role == "telemetry" && hasTelemetryExports {
			numContainers = totalByrole["compute"]
		}

		for i := currentCount; i < numContainers; i++ {

			name := allocator.NewContainerName(role)
			logger.Info("Building missing container", "role", role, "name", name)

			container, err := factory.NewWekaContainerForWekaCluster(cluster, template, role, name)
			if err != nil {
				logger.Error(err, "Failed to build container", "role", role, "name", name)
				return nil, err
			}
			containers = append(containers, container)
		}
	}

	return containers, nil
}

func (r *wekaClusterReconcilerLoop) updateContainersOnNodeSelectorMismatch(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

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
		patch := []map[string]interface{}{
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
