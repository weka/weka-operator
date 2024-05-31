package container

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
)

type StepFunc func(ctx context.Context, state *ReconciliationState) error

type ReconciliationState struct{}

type Step struct {
	Condition     string
	Preconditions []lifecycle.PredicateFunc
	Reconcile     StepFunc
}
