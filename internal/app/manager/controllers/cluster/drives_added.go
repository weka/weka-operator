package cluster

import (
	"context"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"go.opentelemetry.io/otel/codes"
	"k8s.io/apimachinery/pkg/api/meta"
)

func (state *ClusterState) DrivesAdded() lifecycle.StepFunc {
	return func(ctx context.Context) error {
		_, logger, end := instrumentation.GetLogSpan(ctx, "DrivesAdded")
		defer end()

		if state.Subject == nil {
			return &errors.ArgumentError{ArgName: "Cluster", Message: "Cluster is nil"}
		}

		if state.Containers == nil {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is nil"}
		}
		if len(state.Containers) == 0 {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is empty"}
		}
		containers := state.Containers

		// Ensure all containers are up in the cluster
		logger.Debug("Ensuring all drives are up in the cluster")
		for _, container := range containers {
			if container.Spec.Mode != "drive" {
				continue
			}
			if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondDrivesAdded) {
				logger.Info("Containers did not add drives yet", "container", container.Name)
				logger.InfoWithStatus(codes.Unset, "Containers did not add drives yet")
				return &RetryableError{Err: nil, RetryAfter: time.Second * 3}
			}
		}

		return nil
	}
}
