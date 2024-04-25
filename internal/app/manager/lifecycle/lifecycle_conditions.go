package lifecycle

import (
	"context"

	"github.com/kr/pretty"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"k8s.io/apimachinery/pkg/api/meta"
)

func WhenConditionIsNotTrue(condition string, inner IStartingSubState) IStartingSubState {
	return &WhenConditionIsNotTrueState{
		condition: condition,
		inner:     inner,
	}
}

// -----------------------------------------------------------------------------

type WhenConditionIsNotTrueState struct {
	condition string
	inner     IStartingSubState
}

func (s *WhenConditionIsNotTrueState) Handle(ctx context.Context) error {
	ctx, logger, done := instrumentation.GetLogSpan(ctx, "WhenConditionIsNotTrueState.Handle")
	defer done()

	condition := meta.FindStatusCondition(s.inner.GetCluster().Status.Conditions, s.condition)
	if condition == nil {
		return pretty.Errorf("Condition %s not found", s.condition)
	}
	logger.SetValues("condition", condition)

	cluster := s.inner.GetCluster()
	if !meta.IsStatusConditionTrue(cluster.Status.Conditions, s.condition) {
		return s.inner.Handle(ctx)
	}

	return nil
}

func (s *WhenConditionIsNotTrueState) GetCluster() *wekav1alpha1.WekaCluster {
	return s.inner.GetCluster()
}
