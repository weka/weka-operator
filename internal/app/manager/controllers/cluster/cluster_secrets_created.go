package cluster

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

type SecretCreationError struct {
	Err     error
	Cluster *wekav1alpha1.WekaCluster
}

func (e SecretCreationError) Error() string {
	return fmt.Sprintf("error reconciling secret for cluster %s: %v", e.Cluster.Name, e.Err)
}

func (state *ClusterState) ClusterSecretsCreated() lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, _, end := instrumentation.GetLogSpan(ctx, "ClusterSecretsCreated")
		defer end()

		if state.Subject == nil {
			return &errors.ArgumentError{ArgName: "Cluster", Message: "Cluster is nil"}
		}
		if state.Conditions == nil {
			return &lifecycle.StateError{Property: "Cluster.Status.Conditions", Message: "Cluster.Status.Conditions is nil", Span: instrumentation.GetLogName(ctx)}
		}
		// generate login credentials
		secretsService := state.SecretsService
		if err := secretsService.EnsureLoginCredentials(ctx, state.Subject); err != nil {
			return &SecretCreationError{Err: err, Cluster: state.Subject}
		}

		return nil
	}
}
