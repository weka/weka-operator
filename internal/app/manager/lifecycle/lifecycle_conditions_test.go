//go:generate mockgen -destination=mocks/mock_lifecycle_conditions.go -package=mocks github.com/weka/weka-operator/internal/app/manager/lifecycle IStartingSubState
package lifecycle

import (
	"context"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/weka/weka-operator/internal/app/manager/lifecycle/mocks"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestWhenConditionIsNotTrue(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tests := []struct {
		name      string
		status    metav1.ConditionStatus
		callCount int
	}{
		{
			name:      "condition is false",
			status:    metav1.ConditionFalse,
			callCount: 1,
		},
		{
			name:      "condition is unknown",
			status:    metav1.ConditionUnknown,
			callCount: 1,
		},
		{
			name:      "condition is true",
			status:    metav1.ConditionTrue,
			callCount: 0,
		},
	}

	condition := "test"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &wekav1alpha1.WekaCluster{}

			originalStatus := meta.FindStatusCondition(cluster.Status.Conditions, condition)
			if originalStatus != nil {
				t.Errorf("condition already exists: %v", originalStatus)
			}

			meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
				Type:   condition,
				Status: tt.status,
			})

			status := meta.FindStatusCondition(cluster.Status.Conditions, condition)
			if status.Type != condition {
				t.Errorf("unexpected condition: %v, expected: %v", status.Type, condition)
			}
			if status.Status != tt.status {
				t.Errorf("unexpected status: %v, expected: %v", status, tt.status)
			}

			inner := mocks.NewMockIStartingSubState(ctrl)
			inner.EXPECT().GetCluster().Return(cluster).AnyTimes()

			ctx := context.TODO()
			subject := WhenConditionIsNotTrue(condition, inner)
			inner.EXPECT().Handle(gomock.Any()).Return(nil).Times(tt.callCount)

			if err := subject.Handle(ctx); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
