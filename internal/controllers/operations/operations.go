package operations

import (
	"context"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
)

type Operation interface {
	AsStep() lifecycle.Step
	GetSteps() []lifecycle.Step
	GetJsonResult() string
}

func AsRunFunc(op Operation) lifecycle.StepFunc {
	return func(ctx context.Context) error {
		steps := op.GetSteps()
		reconSteps := lifecycle.ReconciliationSteps{
			Steps: steps,
		}
		return reconSteps.Run(ctx)
	}
}

func ExecuteOperation(ctx context.Context, op Operation) error {
	step := op.AsStep()
	reconSteps := lifecycle.ReconciliationSteps{
		Steps: []lifecycle.Step{step},
	}
	return reconSteps.Run(ctx)
}
