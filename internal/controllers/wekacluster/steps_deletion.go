// package wekacluster contains the reconciliation logic for WekaCluster resources
package wekacluster

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/go-lib/pkg/workers"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// GetDeletionSteps returns the deletion and cleanup steps for the WekaCluster reconciliation
func GetDeletionSteps(loop *wekaClusterReconcilerLoop) []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SingleStep{
			Predicates: []lifecycle.PredicateFunc{
				loop.ClusterIsInGracefulDeletion,
			},
			Run: loop.HandleGracefulDeletion,
		},
		&lifecycle.SingleStep{
			Predicates: []lifecycle.PredicateFunc{
				lifecycle.IsNotFunc(loop.ClusterIsInGracefulDeletion),
			},
			Run: loop.HandleDeletion,
		},
	}
}

func (r *wekaClusterReconcilerLoop) HandleGracefulDeletion(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	activeContainer := discovery.SelectActiveContainer(r.containers)
	if activeContainer != nil {
		wekaService := services.NewWekaService(r.ExecService, activeContainer)
		msg := "Destroying Weka cluster"
		err := wekaService.EmitCustomEvent(ctx, msg, utils.GetKubernetesVersion(r.Manager))
		if err != nil {
			logger.Warn("Failed to emit custom event", "event", msg)
		}
	}

	cluster := r.cluster

	err := r.updateClusterStatusIfNotEquals(ctx, weka.WekaClusterStatusGracePeriod)
	if err != nil {
		return err
	}

	err = r.ensureContainersPaused(ctx, weka.WekaContainerModeS3)
	if err != nil {
		return err
	}

	err = r.ensureContainersPaused(ctx, weka.WekaContainerModeNfs)
	if err != nil {
		return err
	}

	err = r.ensureContainersPaused(ctx, "")
	if err != nil {
		return err
	}

	gracefulDestroyDuration := r.cluster.GetGracefulDestroyDuration()
	deletionTime := cluster.GetDeletionTimestamp().Time.Add(gracefulDestroyDuration)

	logger.Info("Cluster is in graceful deletion", "deletionTime", deletionTime)

	return nil
}

func (r *wekaClusterReconcilerLoop) ensureContainersPaused(ctx context.Context, mode string) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureContainersPaused", "mode", mode)
	defer end()

	return workers.ProcessConcurrently(ctx, r.containers, 32, func(ctx context.Context, container *weka.WekaContainer) error {
		ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureContainerPaused", "container", container.Name, "mode", mode, "container_mode", container.Spec.Mode)
		defer end()
		if mode != "" && container.Spec.Mode != mode {
			return nil
		}

		ctx, _, end2 := instrumentation.GetLogSpan(ctx, "ensureMatchingContainerPaused", "container", container.Name, "mode", mode, "container_mode", container.Spec.Mode)
		defer end2()

		if !container.IsPaused() {
			patch := map[string]interface{}{
				"spec": map[string]interface{}{
					"state": weka.ContainerStatePaused,
				},
			}

			patchBytes, err := json.Marshal(patch)
			if err != nil {
				return fmt.Errorf("failed to marshal patch for container %s: %w", container.Name, err)
			}

			err = errors.Wrap(
				r.getClient().Patch(ctx, container, client.RawPatch(types.MergePatchType, patchBytes)),
				fmt.Sprintf("failed to update container state %s: %v", container.Name, err),
			)
			if err != nil {
				return err
			}
		}

		if container.Status.Status != weka.Paused {
			return fmt.Errorf("container %s is not paused yet", container.Name)
		}

		return nil
	}).AsError()
}

func (r *wekaClusterReconcilerLoop) HandleDeletion(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	gracefulDestroyDuration := r.cluster.GetGracefulDestroyDuration()
	deletionTime := r.cluster.GetDeletionTimestamp().Time.Add(gracefulDestroyDuration)
	logger.Debug("Not graceful deletion", "deletionTime", deletionTime, "now", time.Now(), "gracefulDestroyDuration", gracefulDestroyDuration)

	err := r.updateClusterStatusIfNotEquals(ctx, weka.WekaClusterStatusDestroying)
	if err != nil {
		return err
	}

	if controllerutil.ContainsFinalizer(r.cluster, resources.WekaFinalizer) {
		logger.Info("Performing Finalizer Operations for wekaCluster before delete CR")

		// Perform all operations required before remove the finalizer and allow
		// the Kubernetes API to remove the custom resource.
		err := r.finalizeWekaCluster(ctx)
		if err != nil {
			return err
		}

		err = services.ClustersCachedInfo.DeleteJoinIps(ctx, string(r.cluster.GetUID()))
		if err != nil {
			logger.Error(err, "Failed to delete join ips for wekaCluster")
		}

		err = services.ClustersCachedInfo.DeleteClusterCreationTime(ctx, string(r.cluster.GetUID()))
		if err != nil {
			logger.Error(err, "Failed to delete cluster creation time for wekaCluster")
		}

		logger.Info("Removing Finalizer for wekaCluster after successfully perform the operations")
		if ok := controllerutil.RemoveFinalizer(r.cluster, resources.WekaFinalizer); !ok {
			err := errors.New("Failed to remove finalizer for wekaCluster")
			return err
		}

		if err := r.getClient().Update(ctx, r.cluster); err != nil {
			logger.Error(err, "Failed to remove finalizer for wekaCluster")
			return err
		}
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) finalizeWekaCluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster
	clusterService := r.clusterService

	wekaClients, err := discovery.GetWekaClientsForCluster(ctx, r.getClient(), cluster)
	if err != nil {
		return err
	}

	if len(wekaClients) > 0 {
		err := fmt.Errorf("cannot delete cluster with dependent WekaClients, please delete them first")

		_ = r.RecordEventThrottled(v1.EventTypeWarning, "DependentWekaClients", err.Error(), time.Second*30)

		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	err = clusterService.EnsureNoContainers(ctx, weka.WekaContainerModeS3)
	if err != nil {
		reason := fmt.Sprintf("EnsureNo%sContainersError", weka.WekaContainerModeS3)
		_ = r.RecordEventThrottled(v1.EventTypeWarning, reason, err.Error(), time.Second*30)

		return err
	}

	err = clusterService.EnsureNoContainers(ctx, weka.WekaContainerModeNfs)
	if err != nil {
		reason := fmt.Sprintf("EnsureNo%sContainersError", weka.WekaContainerModeNfs)
		_ = r.RecordEventThrottled(v1.EventTypeWarning, reason, err.Error(), time.Second*30)

		return err
	}

	err = clusterService.EnsureNoContainers(ctx, "")
	if err != nil {
		reason := "EnsureNoContainersError"
		_ = r.RecordEventThrottled(v1.EventTypeWarning, reason, err.Error(), time.Second*30)

		return err
	}

	logger.Debug("All containers are removed, deallocating cluster resources")

	err = r.updateClusterStatusIfNotEquals(ctx, weka.WekaClusterStatusDeallocating)
	if err != nil {
		return err
	}

	_ = r.RecordEventThrottled(v1.EventTypeNormal, "DeallocatingClusterResources", "Deallocating cluster resources", time.Second*15)

	resourcesAllocator, err := allocator.NewResourcesAllocator(ctx, r.getClient())
	if err != nil {
		return err
	}

	err = resourcesAllocator.DeallocateCluster(ctx, cluster)
	if err != nil {
		return err
	}
	return nil
}
