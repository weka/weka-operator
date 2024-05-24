package lifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/thoas/go-funk"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type StepFunc func(ctx context.Context) error

type Reconciler interface {
	client.Client
}

type ClusterState struct {
	ReconciliationState[*wekav1alpha1.WekaCluster]
}

type ReconciliationSteps struct {
	Reconciler Reconciler
	State      *ReconciliationState[*wekav1alpha1.WekaCluster]
	Steps      []Step
}

type StatusUpdateError struct {
	Err     error
	Cluster *wekav1alpha1.WekaCluster
}

func (e StatusUpdateError) Error() string {
	return fmt.Sprintf("error updating status for cluster %s: %v", e.Cluster.Name, e.Err)
}

type ConditionExecutionError struct {
	Err       error
	Condition string
}

func (e ConditionExecutionError) Error() string {
	return fmt.Sprintf("error executing condition %s: %v", e.Condition, e.Err)
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

type ReconciliationState[Subject client.Object] struct {
	// Cluster    *wekav1alpha1.WekaCluster
	Subject    Subject
	Conditions *[]metav1.Condition
	Containers []*wekav1alpha1.WekaContainer
}

// -- PreconditionFuncs
type PredicateFunc func(conditions []metav1.Condition) bool

func IsNotTrue(condition string) PredicateFunc {
	return func(conditions []metav1.Condition) bool {
		return !meta.IsStatusConditionTrue(conditions, condition)
	}
}

func IsTrue(condition string) PredicateFunc {
	return func(conditions []metav1.Condition) bool {
		return meta.IsStatusConditionTrue(conditions, condition)
	}
}

// Errors ----------------------------------------------------------------------

type ReconciliationError struct {
	Err     error
	Subject metav1.Object
	Step    Step
}

func (e ReconciliationError) Error() string {
	return fmt.Sprintf("error reconciling cluster %s during phase %s: %v",
		e.Subject.GetName(),
		e.Step.Condition,
		e.Err)
}

type ConditionUpdateError struct {
	Err       error
	Subject   metav1.Object
	Condition metav1.Condition
}

func (e ConditionUpdateError) Error() string {
	return fmt.Sprintf("error updating condition %s for object %s: %v", e.Condition.Type, e.Subject.GetName(), e.Err)
}

type StateError struct {
	Property string
	Message  string
}

func (e StateError) Error() string {
	return fmt.Sprintf("invalid state: %s - %s", e.Property, e.Message)
}

type RetryableError struct {
	Err        error
	RetryAfter time.Duration
}

func (e RetryableError) Error() string {
	return fmt.Sprintf("retryable error: %v, retry after: %s", e.Err, e.RetryAfter)
}

// -- ReconciliationSteps -------------------------------------------------------

func (r *ReconciliationSteps) Reconcile(ctx context.Context) error {
	// cluster := r.State.Cluster
	if r.State == nil {
		return &StateError{Property: "State", Message: "State is nil"}
	}
	if r.State.Conditions == nil {
		return &StateError{Property: "Conditions", Message: "Conditions is nil"}
	}

	for _, step := range r.Steps {

		// Check if step is already done or if the condition should be able to run again
		if !step.SkipOwnConditionCheck {
			if meta.IsStatusConditionTrue(*r.State.Conditions, step.Condition) {
				continue
			}
		}

		// Check preconditions
		failedPreconditions := funk.Filter(step.Predicates, func(precondition PredicateFunc) bool {
			return !precondition(*r.State.Conditions)
		}).([]PredicateFunc)
		if len(failedPreconditions) > 0 {
			continue
		}

		if err := step.Reconcile(ctx); err != nil {
			if err := r.setConditions(ctx, metav1.Condition{
				Type: step.Condition, Status: metav1.ConditionFalse,
				Reason:  "Error",
				Message: err.Error(),
			}); err != nil {
				return &ReconciliationError{Err: err, Subject: r.State.Subject, Step: step}
			}
			return &ReconciliationError{Err: err, Subject: r.State.Subject, Step: step}
		}

		// Update condition
		err := r.setConditions(ctx, metav1.Condition{
			Type:    step.Condition,
			Status:  metav1.ConditionTrue,
			Reason:  "Init",
			Message: "Condition is true",
		})
		if err != nil {
			return &ReconciliationError{Err: err, Subject: r.State.Subject, Step: step}
		}
	}
	return nil
}

func (r *ReconciliationSteps) setConditions(ctx context.Context, condition metav1.Condition) error {
	meta.SetStatusCondition(r.State.Conditions, condition)
	if err := r.Reconciler.Status().Update(ctx, r.State.Subject); err != nil {
		return &ConditionUpdateError{Err: err, Subject: r.State.Subject, Condition: condition}
	}

	return nil
}
