package cluster

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

type ClusterCreationError struct {
	Err     error
	Cluster *wekav1alpha1.WekaCluster
}

func (e ClusterCreationError) Error() string {
	return fmt.Sprintf("cluster creation error: %v, cluster: %s", e.Err, e.Cluster.Name)
}

func (state *ClusterState) ClusterCreated() lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "ClusterCreated")
		defer end()

		wekaCluster := state.Subject
		if wekaCluster == nil {
			return &errors.ArgumentError{ArgName: "Cluster", Message: "Cluster is nil"}
		}
		containers := state.Containers
		if containers == nil {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is nil"}
		}
		if len(containers) == 0 {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is empty"}
		}

		logger.SetPhase("CLUSTERIZING")
		wekaClusterService, err := state.NewWekaClusterService()
		if err != nil {
			return &ClusterCreationError{Err: err, Cluster: wekaCluster}
		}
		if err := wekaClusterService.Create(ctx, containers); err != nil {
			return &ClusterCreationError{Err: err, Cluster: wekaCluster}
		}
		logger.SetPhase("CLUSTER_FORMED")
		return nil
	}
}
