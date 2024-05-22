package lifecycle

import (
	"context"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/services"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

func (state *ClusterState) PodsCreated(crdManager services.CrdManager) StepFunc {
	return func(ctx context.Context) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "PodsCreated")
		defer end()

		containers, err := crdManager.EnsureWekaContainers(ctx, state.Subject)
		if err != nil {
			return &RetryableError{Err: err, RetryAfter: 3 * time.Second}
		}
		state.Containers = containers

		logger.SetPhase("ENSURING_CLUSTER_CONTAINERS")
		logger.SetPhase("PODS_ALREADY_EXIST")
		return nil
	}
}
