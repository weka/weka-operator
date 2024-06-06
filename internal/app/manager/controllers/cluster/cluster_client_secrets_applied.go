package cluster

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
)

type ClientSecretApplicationError struct {
	errors.WrappedError
	Cluster *wekav1alpha1.WekaCluster
}

func (state *ClusterState) ClusterClientSecretsApplied() lifecycle.StepFunc {
	return func(ctx context.Context) error {
		wekaCluster := state.Subject
		if wekaCluster == nil {
			return &errors.ArgumentError{ArgName: "Cluster", Message: "Cluster is nil"}
		}

		if state.Containers == nil {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is nil"}
		}
		containers := state.Containers
		if len(containers) == 0 {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is empty"}
		}

		wekaClusterService, err := state.NewWekaClusterService()
		if err != nil {
			return &ClientSecretApplicationError{
				WrappedError: errors.NewWrappedError(ctx, err),
				Cluster:      wekaCluster,
			}
		}
		if err := wekaClusterService.ApplyClientLoginCredentials(ctx, containers); err != nil {
			return &ClientSecretApplicationError{
				WrappedError: errors.WrappedError{Err: err},
				Cluster:      wekaCluster,
			}
		}
		return nil
	}
}
