//go:generate go run go.uber.org/mock/mockgen@v0.4.0 -destination=mocks/mock_reconciler.go -package=mocks github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle Reconciler
//go:generate go run go.uber.org/mock/mockgen@v0.4.0 -destination=mocks/mock_client.go -package=mocks sigs.k8s.io/controller-runtime/pkg/client StatusWriter
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle/mocks"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	werrors "github.com/weka/weka-operator/internal/pkg/errors"

	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReconciler(t *testing.T) {
	t.Run("SkipOwnConditionCheck", SkipOwnConditionCheck)
	t.Run("Preconditions", Preconditions)
	t.Run("Reconciliation Error", WrapsErrors)
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
				Steps: []Step[*wekav1alpha1.WekaCluster]{
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

func Preconditions(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReconciler := mocks.NewMockReconciler(ctrl)
	mockStatus := mocks.NewMockStatusWriter(ctrl)
	mockReconciler.EXPECT().Status().Return(mockStatus).AnyTimes()

	tests := []struct {
		name               string
		preconds           []PredicateFunc[*wekav1alpha1.WekaCluster]
		shouldSetCondition int
	}{
		{
			name:               "no preconditions",
			preconds:           []PredicateFunc[*wekav1alpha1.WekaCluster]{},
			shouldSetCondition: 1,
		},
		{
			name: "all preconditions pass",
			preconds: []PredicateFunc[*wekav1alpha1.WekaCluster]{
				func(state *ReconciliationState[*wekav1alpha1.WekaCluster]) bool {
					return true
				},
			},
			shouldSetCondition: 1,
		},
		{
			name: "all preconditions fail",
			preconds: []PredicateFunc[*wekav1alpha1.WekaCluster]{
				func(state *ReconciliationState[*wekav1alpha1.WekaCluster]) bool {
					return false
				},
			},
			shouldSetCondition: 0,
		},
		{
			name: "some preconditions fail",
			preconds: []PredicateFunc[*wekav1alpha1.WekaCluster]{
				func(state *ReconciliationState[*wekav1alpha1.WekaCluster]) bool {
					return true
				},
				func(state *ReconciliationState[*wekav1alpha1.WekaCluster]) bool {
					return false
				},
			},
			shouldSetCondition: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
				Steps: []Step[*wekav1alpha1.WekaCluster]{
					{
						Condition: "test",
						Reconcile: func(ctx context.Context) error {
							return nil
						},
						Predicates: test.preconds,
					},
				},
			}

			mockStatus.EXPECT().Update(gomock.Any(), state.Subject).Times(test.shouldSetCondition)
			if err := steps.Reconcile(context.Background()); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func WrapsErrors(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReconciler := mocks.NewMockReconciler(ctrl)
	mockStatus := mocks.NewMockStatusWriter(ctrl)
	mockReconciler.EXPECT().Status().Return(mockStatus).AnyTimes()

	subject := &wekav1alpha1.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
	}
	testError := fmt.Errorf("test error")
	tests := []struct {
		name               string
		errs               []error
		result             error
		shouldSetCondition int
	}{
		{
			name:               "no error",
			errs:               []error{nil},
			result:             nil,
			shouldSetCondition: 1,
		},
		{
			name: "single error",
			errs: []error{testError},
			result: &ReconciliationError{
				WrappedError: werrors.WrappedError{
					Err:  testError,
					Span: "ReconciliationSteps",
				},
				Subject: subject,
				Step:    "test",
			},
			shouldSetCondition: 1,
		},
		{
			name: "multiple steps",
			errs: []error{nil, testError},
			result: &ReconciliationError{
				WrappedError: werrors.WrappedError{
					Err:  testError,
					Span: "ReconciliationSteps",
				},
				Subject: subject,
				Step:    "test",
			},
			shouldSetCondition: 2,
		},
	}

	mockStatus.EXPECT().Update(gomock.Any(), gomock.Any()).AnyTimes()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &ReconciliationState[*wekav1alpha1.WekaCluster]{
				Conditions: &[]metav1.Condition{},
				Subject:    subject,
			}
			steps := ReconciliationSteps[*wekav1alpha1.WekaCluster]{
				Reconciler: mockReconciler,
				State:      state,
				Steps:      []Step[*wekav1alpha1.WekaCluster]{},
			}

			for _, err := range test.errs {
				steps.Steps = append(steps.Steps, Step[*wekav1alpha1.WekaCluster]{
					Condition:             "test",
					SkipOwnConditionCheck: true,
					Reconcile:             func(ctx context.Context) error { return err },
				})
			}

			if len(steps.Steps) != len(test.errs) {
				t.Fatalf("expected %d steps, got %d", len(test.errs), len(steps.Steps))
			}

			err := steps.Reconcile(context.Background())
			if err != nil || test.result != nil {
				var wrappedError *ReconciliationError
				if !errors.As(err, &wrappedError) {
					t.Fatalf("expected error '%v', got '%v'", test.result, err)
				}
			}
		})
	}
}
