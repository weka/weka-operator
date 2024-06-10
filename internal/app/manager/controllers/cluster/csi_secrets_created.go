package cluster

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

type CSISecretsCreatedError struct {
	errors.WrappedError
	Cluster *wekav1alpha1.WekaCluster
}

func (c *ClusterState) ClusterCSISecretsCreated(secretsService services.SecretsService, wekaClusterService services.WekaClusterService) lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, _, done := instrumentation.GetLogSpan(ctx, "ClusterCSISecretsCreated")
		defer done()

		cluster := c.Subject
		if cluster == nil {
			return &lifecycle.StateError{Property: "Subject", Message: "Subject is nil"}
		}

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
