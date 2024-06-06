package cluster

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	werror "github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type handleDeletionState struct {
	*ClusterState

	WekaFinalizer string
}

type HandleDeletionError struct {
	WrappedError error
	Cluster      *wekav1alpha1.WekaCluster
}

func (e *HandleDeletionError) Error() string {
	return fmt.Sprintf("error handling deletion for cluster %s: %v", e.Cluster.Name, e.WrappedError)
}

func (state *ClusterState) HandleDeletion(
	wekaFinalizer string,
) lifecycle.StepFunc {
	handler := &handleDeletionState{
		ClusterState: state,

		WekaFinalizer: wekaFinalizer,
	}
	return handler.StepFn
}

func (state *handleDeletionState) StepFn(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleDeletion")
	defer end()

	wekaClusterService, err := state.NewWekaClusterService()
	if err != nil {
		return &HandleDeletionError{
			WrappedError: werror.WrappedError{
				Err:  err,
				Span: instrumentation.GetLogName(ctx),
			},
			Cluster: state.Subject,
		}
	}
	if err := state.handleDeletion(ctx, wekaClusterService); err != nil {
		return &werror.RetryableError{
			Err:        err,
			RetryAfter: time.Second * 3,
		}
	}
	logger.SetPhase("CLUSTER_IS_BEING_DELETED")
	return nil
}

func (state *handleDeletionState) handleDeletion(ctx context.Context, clusterService services.WekaClusterService) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "handleDeletion")
	defer end()
	wekaCluster := clusterService.GetCluster()
	if controllerutil.ContainsFinalizer(wekaCluster, state.WekaFinalizer) {
		logger.Info("Performing Finalizer Operations for wekaCluster before delete CR")

		// Perform all operations required before remove the finalizer and allow
		// the Kubernetes API to remove the custom resource.
		err := state.finalizeWekaCluster(ctx, clusterService)
		if err != nil {
			return err
		}

		logger.Info("Removing Finalizer for wekaCluster after successfully perform the operations")
		if ok := controllerutil.RemoveFinalizer(wekaCluster, state.WekaFinalizer); !ok {
			err := &HandleDeletionError{
				WrappedError: werror.NewWrappedError(ctx, errors.New("failed to remove finalizer for wekaCluster")),
				Cluster:      wekaCluster,
			}
			return err
		}

		if err := state.Client.Update(ctx, wekaCluster); err != nil {
			logger.Error(err, "Failed to remove finalizer for wekaCluster")
			return err
		}

	}
	return nil
}

func (state *handleDeletionState) finalizeWekaCluster(ctx context.Context, clusterService services.WekaClusterService) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "FinalizeWekaCluster")
	defer end()

	cluster := clusterService.GetCluster()
	if cluster == nil {
		return errors.New("cluster is nil")
	}

	err := clusterService.EnsureNoS3Containers(ctx)
	if err != nil {
		return err
	}

	if cluster.Spec.Topology == "" {
		logger.Info("Topology is not set, skipping deallocation")
		return nil
	}

	if state.Client == nil {
		return &lifecycle.StateError{
			Property: "Client",
			Message:  "Client is nil",
			Span:     instrumentation.GetLogName(ctx),
		}
	}

	topology, err := domain.Topologies[cluster.Spec.Topology](ctx, state.Client, cluster.Spec.NodeSelector)
	if err != nil {
		return err
	}
	allocator := domain.NewAllocator(topology)
	allocations, allocConfigMap, err := state.CrdManager.GetOrInitAllocMap(ctx)
	if err != nil {
		logger.Error(err, "Failed to get alloc map")
		return err
	}

	changed := allocator.DeallocateCluster(domain.OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace}, allocations)
	if changed {
		if err := state.CrdManager.UpdateAllocationsConfigmap(ctx, allocations, allocConfigMap); err != nil {
			logger.Error(err, "Failed to update alloc map")
			return err
		}
	}
	state.Recorder.Event(cluster, "Warning", "Deleting",
		fmt.Sprintf("Custom Resource %s is being deleted from the namespace %s",
			cluster.Name,
			cluster.Namespace))
	return nil
}
