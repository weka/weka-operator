package lifecycle

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type SecretApplicationError struct {
	ConditionExecutionError
	Cluster *wekav1alpha1.WekaCluster
}

func (e SecretApplicationError) Error() string {
	return fmt.Sprintf("secret application error: %v cluster: %s", e.Err, e.Cluster.Name)
}

func (state *ClusterState) ApplyClusterSecrets(wekaClusterService services.WekaClusterService, client client.Client) StepFunc {
	return func(ctx context.Context) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "ApplyClusterSecrets")
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

		logger.SetPhase("CONFIGURING_CLUSTER_CREDENTIALS")
		if err := wekaClusterService.ApplyClusterCredentials(ctx, containers); err != nil {
			return &SecretApplicationError{
				ConditionExecutionError: ConditionExecutionError{Err: err, Condition: condition.CondClusterSecretsApplied},
				Cluster:                 wekaCluster,
			}
		}

		wekaCluster.Status.Status = "Ready"
		wekaCluster.Status.TraceId = ""
		wekaCluster.Status.SpanID = ""
		if err := client.Status().Update(ctx, wekaCluster); err != nil {
			return &StatusUpdateError{
				Err:     err,
				Cluster: wekaCluster,
			}
		}

		return nil
	}
}
