package lifecycle

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/internal/app/manager/services"
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

func (state *ClusterState) ClusterSecretsCreated(secretsService services.SecretsService) StepFunc {
	return func(ctx context.Context) error {
		ctx, _, end := instrumentation.GetLogSpan(ctx, "ClusterSecretsCreated")
		defer end()

		if state.Subject == nil {
			return &errors.ArgumentError{ArgName: "Cluster", Message: "Cluster is nil"}
		}
		if state.Conditions == nil {
			return &errors.ArgumentError{ArgName: "Cluster.Status.Conditions", Message: "Cluster.Status.Conditions is nil"}
		}
		// generate login credentials
		if err := secretsService.EnsureLoginCredentials(ctx, state.Subject); err != nil {
			return &SecretCreationError{Err: err, Cluster: state.Subject}
		}

		return nil
	}
}
