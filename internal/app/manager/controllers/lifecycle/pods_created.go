package lifecycle

import (
	"context"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/services"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func PodsCreated(crdManager services.CrdManager, statusClient StatusClient) StepFunc {
	return func(ctx context.Context, state *ReconciliationState) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "PodsCreated")
		defer end()

		containers, err := crdManager.EnsureWekaContainers(ctx, state.Cluster)
		if err != nil {
			return &RetryableError{Err: err, RetryAfter: 3 * time.Second}
		}
		if containers == nil {
			return &RetryableError{Err: err, RetryAfter: 3 * time.Second}
		}
		if len(containers) == 0 {
			return &RetryableError{Err: err, RetryAfter: 3 * time.Second}
		}
		state.Containers = &containers

		logger.SetPhase("ENSURING_CLUSTER_CONTAINERS")
		_ = statusClient.SetCondition(ctx, state.Cluster, condition.CondPodsCreated, metav1.ConditionTrue, "Init", "All pods are created")
		logger.SetPhase("PODS_ALREADY_EXIST")
		return nil
	}
}
