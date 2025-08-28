package operations

import (
	"context"

	"github.com/weka/go-steps-engine/lifecycle"
)

type Operation interface {
	AsStep() lifecycle.Step
	GetSteps() []lifecycle.Step
	GetJsonResult() string
}

func AsRunFunc(op Operation) lifecycle.StepFunc {
	return func(ctx context.Context) error {
		steps := op.GetSteps()
		stepsEngine := lifecycle.StepsEngine{
			Steps: steps,
		}
		return stepsEngine.Run(ctx)
	}
}

func ExecuteOperation(ctx context.Context, op Operation) error {
	step := op.AsStep()
	stepsEngine := lifecycle.StepsEngine{
		Steps: []lifecycle.Step{step},
	}
	return stepsEngine.Run(ctx)
}
