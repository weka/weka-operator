package wekacontainer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services"
)

const (
	// minPodRunningDurationBeforeEnvoyDeletion is the minimum time a pod must be running
	// before we consider deleting the envoy container due to missing process
	minPodRunningDurationBeforeEnvoyDeletion = 3 * time.Minute
)

// deleteEnvoyIfProcessNotExists checks if envoy process exists when container is in Error state.
// If envoy process doesn't exist AND pod has been running for at least 3 minutes,
// it deletes the WekaContainer to trigger recreation.
func (r *containerReconcilerLoop) deleteEnvoyIfProcessNotExists(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container
	pod := r.pod

	// Check if pod has been running long enough
	if pod.Status.Phase != v1.PodRunning {
		logger.Debug("Pod not running, skipping envoy process check")
		return nil
	}

	// Find the weka-container status to get start time
	var containerStartTime *time.Time
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == "weka-container" && cs.State.Running != nil {
			containerStartTime = &cs.State.Running.StartedAt.Time
			break
		}
	}

	if containerStartTime == nil {
		logger.Debug("Container not running or start time unknown, skipping envoy process check")
		return nil
	}

	podRunningDuration := time.Since(*containerStartTime)
	if podRunningDuration < minPodRunningDurationBeforeEnvoyDeletion {
		logger.Debug("Pod hasn't been running long enough",
			"running_duration", podRunningDuration,
			"required_duration", minPodRunningDurationBeforeEnvoyDeletion)
		return nil
	}

	timeout := time.Second * 10
	executor, err := r.ExecService.GetExecutorWithTimeout(ctx, container, &timeout)
	if err != nil {
		logger.Error(err, "Error creating executor for envoy process check")
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	// Check if envoy process exists
	// Use pgrep to check for envoy process (more reliable than ps | grep)
	stdout, stderr, err := executor.ExecNamed(ctx, "CheckEnvoyProcess", []string{"bash", "-ce", "pgrep -f 'envoy' || true"})
	if err != nil {
		logger.Error(err, "Error checking envoy process", "stderr", stderr.String())
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	processOutput := strings.TrimSpace(stdout.String())

	if processOutput == "" {
		// Envoy process not found - need to delete the container
		logger.Info("Envoy process not found in Error state after sufficient runtime",
			"pod_running_duration", podRunningDuration)

		// Throttle envoy deletions: only allow one deletion per cluster per minute
		// This prevents cascading failures if multiple envoy containers have issues
		if config.Config.RecreateUnhealthyEnvoyThrottlingEnabled {
			throttler := r.ThrottlingMap.WithPartition("cluster/" + container.Status.ClusterID + "/envoy-deletion")
			if !throttler.ShouldRun("deleteEnvoyIfProcessNotExists", &throttling.ThrottlingSettings{
				Interval:                    time.Minute,
				DisableRandomPreSetInterval: true,
			}) {
				logger.Info("Throttling envoy deletion, another envoy was deleted recently")
				return lifecycle.NewWaitErrorWithDuration(
					errors.New("throttling envoy container deletion (one per minute per cluster)"),
					time.Second*15,
				)
			}
		}

		_ = r.RecordEvent(v1.EventTypeWarning, "EnvoyProcessNotFound",
			fmt.Sprintf("Envoy process not found while container is in Error state (pod running for %v), deleting container for recreation",
				podRunningDuration.Round(time.Second)))

		err = services.SetContainerStateDeleting(ctx, container, r.Client)
		if err != nil {
			return fmt.Errorf("failed to set container state to deleting: %w", err)
		}

		return lifecycle.NewWaitError(fmt.Errorf("envoy process not found, container marked for deletion"))
	}

	logger.Debug("Envoy process found", "pids", processOutput)
	return nil
}
