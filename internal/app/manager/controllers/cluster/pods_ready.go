package cluster

import (
	"context"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	"github.com/kr/pretty"
	"go.opentelemetry.io/otel/codes"
)

func (state *ClusterState) PodsReady() lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "PodsReady")
		defer end()

		containers := state.Containers
		if containers == nil {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is nil"}
		}
		if len(containers) == 0 {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is empty"}
		}

		logger.Debug("Checking if all containers are ready")
		if ready, err := isContainersReady(ctx, containers); !ready {
			logger.SetPhase("CONTAINERS_NOT_READY")
			if err != nil {
				logger.Error(err, "containers are not ready")
			}
			return &errors.RetryableError{Err: err, RetryAfter: 3 * time.Second}
		}
		logger.SetPhase("CONTAINERS_ARE_READY")
		return nil
	}
}

func isContainersReady(ctx context.Context, containers []*wekav1alpha1.WekaContainer) (bool, error) {
	_, logger, end := instrumentation.GetLogSpan(ctx, "isContainersReady")
	defer end()

	for _, container := range containers {
		if container.GetDeletionTimestamp() != nil {
			err := pretty.Errorf("Container %s is being deleted, rejecting cluster create", container.Name)
			logger.Error(err, "Container is being deleted")
			return false, err
		}
		if container.Status.ManagementIP == "" {
			logger.Debug("Container is not ready yet or has no valid management IP", "container_name", container.Name)
			return false, nil
		}

		if container.Status.Status != "Running" {
			logger.Debug("Container is not running yet", "container_name", container.Name)
			return false, nil
		}
	}
	logger.InfoWithStatus(codes.Ok, "Containers are ready")
	return true, nil
}
