package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"go.opentelemetry.io/otel/codes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func PodsReady(statusClient StatusClient) StepFunc {
	return func(ctx context.Context, state *ReconciliationState) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "PodsReady")
		defer end()

		wekaCluster := state.Cluster
		containers := state.Containers

		logger.Debug("Checking if all containers are ready")
		if ready, err := isContainersReady(ctx, containers); !ready {
			logger.SetPhase("CONTAINERS_NOT_READY")
			if err != nil {
				logger.Error(err, "containers are not ready")
			}
			return &RetryableError{Err: err, RetryAfter: 3 * time.Second}
		}
		logger.SetPhase("CONTAINERS_ARE_READY")
		_ = statusClient.SetCondition(ctx, wekaCluster, condition.CondPodsReady, metav1.ConditionTrue, "Init", "All weka containers are ready for clusterization")
		return nil
	}
}

func isContainersReady(ctx context.Context, containers []*wekav1alpha1.WekaContainer) (bool, error) {
	_, logger, end := instrumentation.GetLogSpan(ctx, "isContainersReady")
	defer end()

	for _, container := range containers {
		if container.GetDeletionTimestamp() != nil {
			logger.Debug("Container is being deleted, rejecting cluster create", "container_name", container.Name)
			return false, errors.New("Container " + container.Name + " is being deleted, rejecting cluster create")
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
