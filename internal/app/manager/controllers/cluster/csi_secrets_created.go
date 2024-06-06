package cluster

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

type CSISecretsCreatedError struct {
	errors.WrappedError
	Cluster *wekav1alpha1.WekaCluster
}

func (c *ClusterState) ClusterCSISecretsCreated() lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, _, done := instrumentation.GetLogSpan(ctx, "ClusterCSISecretsCreated")
		defer done()

		cluster := c.Subject
		if cluster == nil {
			return &lifecycle.StateError{Property: "Subject", Message: "Subject is nil"}
		}

		wekaClusterService := c.WekaClusterService
		if wekaClusterService == nil {
			return &lifecycle.StateError{Property: "WekaClusterService", Message: "WekaClusterService is nil"}
		}

		secretsService := c.SecretsService
		err := secretsService.EnsureCSILoginCredentials(ctx, wekaClusterService)
		if err != nil {
			return &CSISecretsCreatedError{
				WrappedError: errors.WrappedError{
					Err:  err,
					Span: instrumentation.GetLogName(ctx),
				},
				Cluster: cluster,
			}
		}

		return nil
	}
}
