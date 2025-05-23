package wekacontainer

import (
	"github.com/weka/go-steps-engine/lifecycle"
)

// PausedStateFlow returns the steps for a container in the paused state
func PausedStateFlow(r *containerReconcilerLoop) []lifecycle.Step {
	metricsSteps := MetricsSteps(r)

	steps := []lifecycle.Step{
		&lifecycle.SingleStep{
			Run:             r.handleStatePaused,
			FinishOnSuccess: true,
		},
	}

	return append(metricsSteps, steps...)
}
