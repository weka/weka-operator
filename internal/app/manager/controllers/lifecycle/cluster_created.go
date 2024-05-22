package lifecycle

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ClusterCreationError struct {
	Err     error
	Cluster *wekav1alpha1.WekaCluster
}

func (e ClusterCreationError) Error() string {
	return fmt.Sprintf("cluster creation error: %v, cluster: %s", e.Err, e.Cluster.Name)
}

func ClusterCreated(wekaClusterService services.WekaClusterService, statusClient StatusClient) StepFunc {
	return func(ctx context.Context, state *ReconciliationState) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "ClusterCreated")
		defer end()

		wekaCluster := state.Cluster
		if wekaCluster == nil {
			return &errors.ArgumentError{ArgName: "Cluster", Message: "Cluster is nil"}
		}
		containers := state.Containers
		if containers == nil {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is nil"}
		}
		if len(*containers) == 0 {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is empty"}
		}

		logger.SetPhase("CLUSTERIZING")
		err := wekaClusterService.Create(ctx, *containers)
		if err != nil {
			_ = statusClient.SetCondition(ctx, wekaCluster, condition.CondClusterCreated, metav1.ConditionFalse, "Error", err.Error())
			return &ClusterCreationError{Err: err, Cluster: wekaCluster}
		}
		_ = statusClient.SetCondition(ctx, wekaCluster, condition.CondClusterCreated, metav1.ConditionTrue, "Init", "Cluster is formed")
		logger.SetPhase("CLUSTER_FORMED")
		return nil
	}
}
