package container

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
)

type ReconciliationState struct{}

type StepFunc func(ctx context.Context, state *ReconciliationState) error

type Step struct {
	Condition     string
	Preconditions []lifecycle.PredicateFunc
	Reconcile     StepFunc
}
