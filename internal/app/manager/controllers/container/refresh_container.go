package container

import "context"

func RefreshContainer() StepFunc {
	return func(ctx context.Context, state *ReconciliationState) error {
		return nil
	}
}
