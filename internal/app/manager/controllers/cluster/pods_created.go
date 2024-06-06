package cluster

import (
	"context"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

func (state *ClusterState) PodsCreated() lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "PodsCreated")
		defer end()

		crdManager := state.CrdManager
		containers, err := crdManager.EnsureWekaContainers(ctx, state.Subject)
		if err != nil {
			return &errors.RetryableError{Err: err, RetryAfter: 3 * time.Second}
		}
		if containers == nil {
			return &errors.RetryableError{Err: err, RetryAfter: 3 * time.Second}
		}
		if len(containers) == 0 {
			return &errors.RetryableError{Err: err, RetryAfter: 3 * time.Second}
		}
		state.Containers = containers

		logger.SetPhase("ENSURING_CLUSTER_CONTAINERS")
		logger.SetPhase("PODS_ALREADY_EXIST")
		return nil
	}
}
