package cluster

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

type SecretApplicationError struct {
	lifecycle.ConditionExecutionError
	Cluster *wekav1alpha1.WekaCluster
}

func (e SecretApplicationError) Error() string {
	return fmt.Sprintf("secret application error: %v cluster: %s", e.Err, e.Cluster.Name)
}

func (state *ClusterState) ApplyClusterSecrets() lifecycle.StepFunc {
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
		wekaClusterService, err := state.NewWekaClusterService()
		if err != nil {
			return &SecretApplicationError{
				ConditionExecutionError: lifecycle.ConditionExecutionError{
					Err:       err,
					Condition: condition.CondClusterSecretsApplied,
				},
				Cluster: wekaCluster,
			}
		}
		if err := wekaClusterService.ApplyClusterCredentials(ctx, containers); err != nil {
			return &SecretApplicationError{
				ConditionExecutionError: lifecycle.ConditionExecutionError{
					Err:       err,
					Condition: condition.CondClusterSecretsApplied,
				},
				Cluster: wekaCluster,
			}
		}

		wekaCluster.Status.Status = "Ready"
		wekaCluster.Status.TraceId = ""
		wekaCluster.Status.SpanID = ""

		client := state.Client
		if err := client.Status().Update(ctx, wekaCluster); err != nil {
			return &lifecycle.StatusUpdateError{
				Err:     err,
				Cluster: wekaCluster,
			}
		}

		return nil
	}
}
