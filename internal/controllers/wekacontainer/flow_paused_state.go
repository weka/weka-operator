package wekacontainer

import (
	"context"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

// PausedStateFlow returns the steps for a container in the paused state
func PausedStateFlow(r *containerReconcilerLoop) []lifecycle.Step {
	metricsSteps := MetricsSteps(r)

	steps := []lifecycle.Step{
		&lifecycle.SingleStep{
			Run: r.deleteEnvoyIfNoS3Neighbor,
			Predicates: lifecycle.Predicates{
				r.container.IsEnvoy,
			},
		},
		&lifecycle.SingleStep{
			Run: r.handleStatePaused,
		},
	}

	return append(metricsSteps, steps...)
}

func (r *containerReconcilerLoop) handleStatePaused(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	if r.container.Status.Status != weka.Paused {
		err := r.stopForceAndEnsureNoPod(ctx)
		if err != nil {
			return err
		}

		logger.Debug("Updating status", "from", r.container.Status.Status, "to", weka.Paused)
		r.container.Status.Status = weka.Paused

		if err := r.Status().Update(ctx, r.container); err != nil {
			err = errors.Wrap(err, "Failed to update status")
			return err
		}
	}

	logger.Info("Container is paused, skipping reconciliation")

	return nil
}
