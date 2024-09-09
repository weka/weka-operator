package lifecycle

import (
	"context"
	"testing"

	"github.com/weka/weka-operator/internal/controllers/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRun(t *testing.T) {
	ctx := context.Background()
	conditions := []metav1.Condition{
		{
			Type:   condition.CondPodsReady,
			Status: metav1.ConditionTrue,
		},
	}
	steps := []Step{
		{
			Condition: condition.CondPodsReady,
			Run:       func(ctx context.Context) error { return nil },
		},
	}
	reconciliationSteps := &ReconciliationSteps{
		Steps:      steps,
		Conditions: &conditions,
	}

	if conditions[0].Reason != "" {
		t.Errorf("Run() error = %v, want nil", conditions[0].Reason)
	}

	err := reconciliationSteps.Run(ctx)
	if err != nil {
		t.Errorf("Run() error = %v, want nil", err)
	}

	// Condition should not be modified
	if conditions[0].Reason != "" {
		t.Errorf("Run() error = %v, want nil", conditions[0].Reason)
	}
}
