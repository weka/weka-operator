package lifecycle

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
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

func ClusterSecretsCreated(secretsService services.SecretsService, statusClient StatusClient) StepFunc {
	return func(ctx context.Context, state *ReconciliationState) error {
		ctx, _, end := instrumentation.GetLogSpan(ctx, "ClusterSecretsCreated")
		defer end()

		if state.Cluster == nil {
			return &errors.ArgumentError{ArgName: "Cluster", Message: "Cluster is nil"}
		}
		if state.Cluster.Status.Conditions == nil {
			return &errors.ArgumentError{ArgName: "Cluster.Status.Conditions", Message: "Cluster.Status.Conditions is nil"}
		}
		// generate login credentials
		if err := secretsService.EnsureLoginCredentials(ctx, state.Cluster); err != nil {
			return &SecretCreationError{Err: err, Cluster: state.Cluster}
		}

		_ = statusClient.SetCondition(ctx, state.Cluster, condition.CondClusterSecretsCreated, metav1.ConditionTrue, "Init", "Cluster secrets are created")

		return nil
	}
}
