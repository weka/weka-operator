package cluster

import (
	"context"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"k8s.io/apimachinery/pkg/api/meta"
)

type ContainerJoinError struct {
	Err error
}

func (e ContainerJoinError) Error() string {
	return "error joining containers to cluster: " + e.Err.Error()
}

func (state *ClusterState) ContainersJoinedCluster() lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "ContainersJoinedCluster")
		defer end()

		if state.Subject == nil {
			return &errors.ArgumentError{ArgName: "Cluster", Message: "Cluster is nil"}
		}
		wekaCluster := state.Subject

		if state.Containers == nil {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is nil"}
		}
		if len(state.Containers) == 0 {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is empty"}
		}
		containers := state.Containers

		logger.Debug("Ensuring all containers are up in the cluster")
		joinedContainers := 0
		for _, container := range containers {
			if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondJoinedCluster) {
				logger.Info("Container has not joined the cluster yet", "container", container.Name)
				logger.SetPhase("CONTAINERS_NOT_JOINED_CLUSTER")

				return &errors.RetryableError{Err: nil, RetryAfter: time.Second * 3}
			} else {
				if wekaCluster.Status.ClusterID == "" {
					wekaCluster.Status.ClusterID = container.Status.ClusterID
					client := state.Client
					if err := client.Status().Update(ctx, wekaCluster); err != nil {
						return &ContainerJoinError{Err: err}
					}
					logger.Info("Container joined cluster successfully", "container_name", container.Name)
				}
				joinedContainers++
			}
		}
		if joinedContainers == len(containers) {
			logger.SetPhase("ALL_CONTAINERS_ALREADY_JOINED")
		} else {
			logger.SetPhase("CONTAINERS_JOINED_CLUSTER")
		}

		wekaClusterService, err := state.NewWekaClusterService()
		if err != nil {
			return &ContainerJoinError{Err: err}
		}
		if err := wekaClusterService.EnsureClusterContainerIds(ctx, containers); err != nil {
			return &errors.RetryableError{
				Err:        &ContainerJoinError{Err: err},
				RetryAfter: time.Second * 3,
			}
		}

		return nil
	}
}
