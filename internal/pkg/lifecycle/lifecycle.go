package lifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/pkg/util"
	"go.opentelemetry.io/otel/codes"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type StepFunc func(ctx context.Context) error

type ReconciliationSteps struct {
	Client           client.Client
	ConditionsObject client.Object
	Conditions       *[]metav1.Condition
	Steps            []Step
}

type Step struct {
	// Name of the step
	Name string

	// Condition that must be false for the step to be executed, set to True if the step is done succesfully
	Condition   string
	CondReason  string
	CondMessage string

	// Predicates must all be true for the step to be executed
	Predicates []PredicateFunc

	// Should the step be run if the condition is already true
	// Preconditions will also be evaluated and must be true
	SkipOwnConditionCheck bool

	// Continue on predicates false
	ContinueOnPredicatesFalse bool

	// Finish execution succesfully if operation ran and completed
	FinishOnSuccess bool

	// The function to execute
	Run StepFunc
}

type ReconciliationState[Subject client.Object] struct {
	// Cluster    *wekav1alpha1.WekaCluster
}

// -- PreconditionFuncs
type PredicateFunc func() bool
type Predicates []PredicateFunc

func IsNotTrueCondition(condition string, currentConditions *[]metav1.Condition) PredicateFunc {
	return func() bool {
		return !meta.IsStatusConditionTrue(*currentConditions, condition)
	}
}

func IsTrueCondition(condition string, currentConditions *[]metav1.Condition) PredicateFunc {
	return func() bool {
		return meta.IsStatusConditionTrue(*currentConditions, condition)
	}
}

// Errors ----------------------------------------------------------------------
type AbortedByPredicate struct {
	error
}

type WaitError struct {
	Err error
}

func (w WaitError) Error() string {
	return "wait-error:" + w.Err.Error()
}

func NewWaitError(err error) error {
	return &WaitError{Err: err}
}

type ReconciliationError struct {
	Err     error
	Subject metav1.Object
	Step    Step
}

func (e ReconciliationError) Error() string {
	if e.Subject != nil {
		kind := ""
		// cast subject to metav1.Type
		if e.Subject != nil {
			//cast to metav1.Type
			t, ok := e.Subject.(metav1.Type)
			if ok {
				kind = t.GetKind()
			}
		}
		return fmt.Sprintf("error reconciling object %s:%s during phase %s: %v",
			e.Subject.GetName(), kind,
			e.Step.Name,
			e.Err)
	} else {
		return fmt.Sprintf("error reconciling object during phase %s: %v",
			e.Step.Name,
			e.Err)
	}
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

func (r *ReconciliationSteps) Run(ctx context.Context) error {
	var end func()
	var runLogger *instrumentation.SpanLogger
	if r.ConditionsObject != nil {
		ctx, runLogger, end = instrumentation.GetLogSpan(ctx, "ReconciliationSteps", "object_namespace", r.ConditionsObject.GetNamespace(), "object_name", r.ConditionsObject.GetName())
		defer end()
	} else {
		ctx, runLogger, end = instrumentation.GetLogSpan(ctx, "ReconciliationSteps")
		defer end()
	}

	var stepEnd func()
STEPS:
	for _, step := range r.Steps {
		// setValues does not seem to affect span.
		// TODO: Fix it! but, first need to move to standalone observability lib and fix there if broken
		runLogger.SetValues("last_step", step.Name)
		if stepEnd != nil {
			stepEnd()
			stepEnd = nil
		}
		if step.Name == "" {
			step.Name = util.GetFunctionName(step.Run)
		}
		stepCtx, stepLogger, spanEnd := instrumentation.GetLogSpan(ctx, step.Name)
		stepEnd = spanEnd
		defer spanEnd() // in case we dont handle it will in terms of closing in for loop

		// Check if step is already done or if the condition should be able to run again
		if step.Condition != "" && !step.SkipOwnConditionCheck {
			if meta.IsStatusConditionTrue(*r.Conditions, step.Condition) {
				continue STEPS
			}
		}

		if step.Predicates == nil {
			step.Predicates = Predicates{}
		}
		// Check preconditions
		for _, predicate := range step.Predicates {
			if !predicate() {
				if step.ContinueOnPredicatesFalse {
					stepEnd()
					continue STEPS
				} else {
					stopErr := &AbortedByPredicate{fmt.Errorf("aborted: predicate %v is false for step %s", predicate, step.Name)}
					runLogger.SetValues("stop_err", stopErr.Error())
					return stopErr
				}
			}
		}

		if err := step.Run(stepCtx); err != nil {
			if step.Condition != "" {
				setCondError := r.setConditions(stepCtx, metav1.Condition{
					Type: step.Condition, Status: metav1.ConditionFalse,
					Reason:  "Error",
					Message: err.Error(),
				})
				if setCondError != nil {
					stepLogger.Debug("error setting reconcile error on object", "step", step.Name, "error", setCondError)
					stepLogger.SetError(err, "Error running step")
					stepEnd()
					return setCondError
				}
			}
			//spanLogger.Error(err, "Error running step")
			stepLogger.SetError(err, "Error running step")
			runLogger.SetError(err, "Error running step "+step.Name)
			runLogger.SetValues("stop_err", err.Error())
			stepEnd()
			return &ReconciliationError{Err: err, Subject: r.ConditionsObject, Step: step}
		} else {
			stepLogger.SetStatus(codes.Ok, "Step completed successfully")
			if step.FinishOnSuccess {
				stepEnd()
				return nil
			}
		}

		// Update condition
		if step.Condition != "" {
			reason := step.CondReason
			if reason == "" {
				reason = "Init"
			}
			message := step.CondMessage
			if message == "" {
				message = "Completed successfully"
			}

			err := r.setConditions(stepCtx, metav1.Condition{
				Type:    step.Condition,
				Status:  metav1.ConditionTrue,
				Reason:  reason,
				Message: message,
			})

			if err != nil {
				stepEnd()
				stopErr := &ReconciliationError{Err: err, Subject: r.ConditionsObject, Step: step}
				runLogger.SetValues("stop_err", stopErr.Error())
				runLogger.SetError(err, "Error running step "+step.Name)
				stepLogger.SetError(err, "Error setting condition")
				return stopErr
			}
		}
	}
	return nil
}

func (r *ReconciliationSteps) setConditions(ctx context.Context, condition metav1.Condition) error {
	meta.SetStatusCondition(r.Conditions, condition)
	if err := r.Client.Status().Update(ctx, r.ConditionsObject); err != nil {
		return &ConditionUpdateError{Err: err, Subject: r.ConditionsObject, Condition: condition}
	}

	return nil
}

func (r *ReconciliationSteps) RunAsReconcilerResponse(ctx context.Context) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	err := r.Run(ctx)
	if err != nil {
		// check if the error is WaitError or AbortError, then return without error, but with 3 seconds wait
		var lastUnpacked *ReconciliationError
		var unpackTarget error
		unpackTarget = err
		for {
			unpacked, ok := unpackTarget.(*ReconciliationError)
			if !ok {
				if lastUnpacked == nil {
					break
				}
				if _, ok := lastUnpacked.Err.(*WaitError); ok {
					logger.Info("waiting for conditions to be met", "error", err)
					return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
				}
				if _, ok := lastUnpacked.Err.(*AbortedByPredicate); ok {
					logger.Info("aborted by predicate", "error", err)
					return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
				}
				break
			} else {
				lastUnpacked = unpacked
				unpackTarget = unpacked.Err
			}
		}
		logger.Error(err, "Error processing reconciliation steps")
		return ctrl.Result{
			Requeue:      true,
			RequeueAfter: 10 * time.Second,
		}, nil
	}
	logger.Info("Reconciliation steps completed successfully")
	return ctrl.Result{
		Requeue:      true,
		RequeueAfter: time.Second * 30,
	}, nil // Never fully abort
}

func IsNotNil(obj any) PredicateFunc {
	return func() bool {
		return obj != nil
	}
}

func IsNil(obj any) PredicateFunc {
	return func() bool {
		return obj == nil
	}
}

func IsEmptyString(str string) PredicateFunc {
	return func() bool {
		return str == ""
	}
}

func IsNotFunc(container func() bool) PredicateFunc {
	return func() bool {
		return !container()
	}
}
