package cluster

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

type ClientSecretCreationError struct {
	errors.WrappedError
}

func (state *ClusterState) ClusterClientSecretsCreated() lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, _, end := instrumentation.GetLogSpan(ctx, "ClusterClientSecretsCreated")
		defer end()

		if state.Subject == nil {
			return &errors.ArgumentError{ArgName: "Cluster", Message: "Cluster is nil"}
		}
		wekaCluster := state.Subject

		if state.Containers == nil {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is nil"}
		}
		containers := state.Containers
		if len(containers) == 0 {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is empty"}
		}

		secretsService := state.SecretsService
		if err := secretsService.EnsureClientLoginCredentials(ctx, wekaCluster, containers); err != nil {
			return &ClientSecretCreationError{}
		}
		return nil
	}
}
