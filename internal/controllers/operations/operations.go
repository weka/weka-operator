package operations

import (
	"context"
	"encoding/json"

	"github.com/weka/go-steps-engine/lifecycle"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// previousOwnerResult returns the raw JSON result the owner recorded for its previous run, or ""
// when the owner kind carries no such field. WekaManualOperation and WekaPolicy spell it
// differently (Status.Result vs Status.LastResult), which is the only reason this switch exists.
func previousOwnerResult(ownerRef client.Object) string {
	switch owner := ownerRef.(type) {
	case *weka.WekaManualOperation:
		return owner.Status.Result
	case *weka.WekaPolicy:
		return owner.Status.LastResult
	default:
		return ""
	}
}

// decodePreviousOwnerResult decodes the owner's previously recorded JSON result into T.
// Best-effort by design: returns nil on absence or any parse failure, since callers use it to
// carry counters across reconciles and must not fail an operation over an unreadable one.
func decodePreviousOwnerResult[T any](ownerRef client.Object) *T {
	raw := previousOwnerResult(ownerRef)
	if raw == "" {
		return nil
	}
	var prev T
	if err := json.Unmarshal([]byte(raw), &prev); err != nil {
		return nil
	}
	return &prev
}

func ExecuteOperation(ctx context.Context, op Operation) error {
	step := op.AsStep()
	stepsEngine := lifecycle.StepsEngine{
		Steps: []lifecycle.Step{step},
	}
	return stepsEngine.Run(ctx)
}
