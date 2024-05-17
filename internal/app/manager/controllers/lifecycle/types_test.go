//go:generate go run go.uber.org/mock/mockgen@latest -destination=mocks/mock_reconciler.go -package=mocks github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle Reconciler
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
			steps := ReconciliationSteps{
				Reconciler: mockReconciler,
				Cluster:    &wekav1alpha1.WekaCluster{},
				Steps: []Step{
					{
						Condition:             "test",
						SkipOwnConditionCheck: test.skip,
						Reconcile: func(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error {
							return nil
						},
					},
				},
			}

			mockReconciler.EXPECT().SetCondition(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(test.shouldSetCondition)

			meta.SetStatusCondition(&steps.Cluster.Status.Conditions, metav1.Condition{
				Type:    "test",
				Status:  test.status,
				Reason:  "Init",
				Message: "Test message",
			})

			if err := steps.Reconcile(context.Background()); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
