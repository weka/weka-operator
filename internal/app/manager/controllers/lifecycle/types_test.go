//go:generate go run go.uber.org/mock/mockgen@latest -destination=mocks/mock_reconciler.go -package=mocks github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle Reconciler
//go:generate go run go.uber.org/mock/mockgen@latest -destination=mocks/mock_client.go -package=mocks sigs.k8s.io/controller-runtime/pkg/client StatusWriter
package lifecycle

import (
	"context"
	"fmt"
	"testing"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle/mocks"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReconciler(t *testing.T) {
	t.Run("SkipOwnConditionCheck", SkipOwnConditionCheck)
}

func SkipOwnConditionCheck(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReconciler := mocks.NewMockReconciler(ctrl)
	mockStatus := mocks.NewMockStatusWriter(ctrl)
	mockReconciler.EXPECT().Status().Return(mockStatus).AnyTimes()

	tests := []struct {
		skip               bool
		status             metav1.ConditionStatus
		shouldSetCondition int
	}{
		{
			skip:               true,
			status:             metav1.ConditionTrue,
			shouldSetCondition: 1,
		},
		{
			skip:               true,
			status:             metav1.ConditionFalse,
			shouldSetCondition: 1,
		},
		{
			skip:               true,
			status:             metav1.ConditionUnknown,
			shouldSetCondition: 1,
		},
		{
			skip:               false,
			status:             metav1.ConditionTrue,
			shouldSetCondition: 0,
		},
		{
			skip:               false,
			status:             metav1.ConditionFalse,
			shouldSetCondition: 1,
		},
		{
			skip:               false,
			status:             metav1.ConditionUnknown,
			shouldSetCondition: 1,
		},
	}

	for _, test := range tests {
		name := fmt.Sprintf("skip=%v status=%s", test.skip, test.status)
		t.Run(name, func(t *testing.T) {
			state := &ReconciliationState[*wekav1alpha1.WekaCluster]{
				Subject: &wekav1alpha1.WekaCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
				},
				Conditions: &[]metav1.Condition{},
			}
			steps := ReconciliationSteps[*wekav1alpha1.WekaCluster]{
				Reconciler: mockReconciler,
				State:      state,
				Steps: []Step{
					{
						Condition:             "test",
						SkipOwnConditionCheck: test.skip,
						Reconcile: func(ctx context.Context) error {
							return nil
						},
					},
				},
			}

			meta.SetStatusCondition(state.Conditions, metav1.Condition{
				Type:    "test",
				Status:  test.status,
				Reason:  "Init",
				Message: "Test message",
			})

			initialStatus := meta.FindStatusCondition(*state.Conditions, "test")
			if initialStatus == nil {
				t.Fatalf("initial status is nil")
			}
			if initialStatus.Status != test.status {
				t.Fatalf("expected initial status to be %s, got %s", test.status, initialStatus.Status)
			}

			mockStatus.EXPECT().Update(gomock.Any(), state.Subject).Times(test.shouldSetCondition)

			if err := steps.Reconcile(context.Background()); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
