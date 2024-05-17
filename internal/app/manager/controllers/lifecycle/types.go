package lifecycle

import (
	"context"
	"fmt"

	"github.com/thoas/go-funk"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type (
	StepFunc func(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error
)

type Reconciler interface {
	SetCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster,
		condType string, status metav1.ConditionStatus, reason string, message string,
	) error
}

type ReconciliationSteps struct {
	Reconciler Reconciler
	Cluster    *wekav1alpha1.WekaCluster
	Steps      []Step
}

type Step struct {
	// Name of the step.  This is usually a condition
	Condition string

	// Predicates must all be true for the step to be executed
	Predicates []PredicateFunc

	// Should the step be run if the condition is already true
	// Preconditions will also be evaluated and must be true
	SkipOwnConditionCheck bool

	// The function to execute
	Reconcile StepFunc
}

// -- PreconditionFuncs
type PredicateFunc func(conditions []metav1.Condition) bool

func IsNotTrue(condition string) PredicateFunc {
	return func(conditions []metav1.Condition) bool {
		return !meta.IsStatusConditionTrue(conditions, condition)
	}
}

// Errors ----------------------------------------------------------------------

type ReconciliationError struct {
	Err     error
	Cluster *wekav1alpha1.WekaCluster
	Step    Step
}

func (e ReconciliationError) Error() string {
	return fmt.Sprintf("error reconciling cluster %s during phase %s: %v", e.Cluster.Name, e.Step.Condition, e.Err)
}

// -- ReconciliationSteps -------------------------------------------------------

func (r *ReconciliationSteps) Reconcile(ctx context.Context) error {
	cluster := r.Cluster
	for _, step := range r.Steps {

		// Check if step is already done or if the condition should be able to run again
		if !step.SkipOwnConditionCheck {
			if meta.IsStatusConditionTrue(cluster.Status.Conditions, step.Condition) {
				continue
			}
		}

		// Check preconditions
		failedPreconditions := funk.Filter(step.Predicates, func(precondition PredicateFunc) bool {
			return !precondition(cluster.Status.Conditions)
		}).([]PredicateFunc)
		if len(failedPreconditions) > 0 {
			continue
		}

		if err := step.Reconcile(ctx, cluster); err != nil {
			if err := r.Reconciler.SetCondition(
				ctx, cluster, step.Condition, metav1.ConditionFalse, "Error", err.Error(),
			); err != nil {
				return &ReconciliationError{Err: err, Cluster: cluster, Step: step}
			}
			return &ReconciliationError{Err: err, Cluster: cluster, Step: step}
		}

		// Update condition
		err := r.Reconciler.SetCondition(
			ctx, cluster, step.Condition, metav1.ConditionTrue, "Init", "Condition is true",
		)
		if err != nil {
			return &ReconciliationError{Err: err, Cluster: cluster, Step: step}
		}
	}
	return nil
}
