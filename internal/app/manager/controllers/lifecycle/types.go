package lifecycle

import (
	"context"
	"fmt"
	"time"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type StepFunc func(ctx context.Context, state *ReconciliationState) error

type RetryableError struct {
	Err        error
	RetryAfter time.Duration
}

func (e RetryableError) Error() string {
	return fmt.Sprintf("retryable error: %v, retry after: %s", e.Err, e.RetryAfter)
}

type Step struct {
	Condition     string
	Preconditions []PreconditionFunc
	Reconcile     StepFunc
}

type StatusClient interface {
	SetCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster, condType string, status metav1.ConditionStatus, reason string, message string) error
}

type ReconciliationState struct {
	Cluster    *wekav1alpha1.WekaCluster
	Containers []*wekav1alpha1.WekaContainer
}

// -- PreconditionFuncs
type PreconditionFunc func(conditions []metav1.Condition) bool

func IsNotTrue(condition string) PreconditionFunc {
	return func(conditions []metav1.Condition) bool {
		return !meta.IsStatusConditionTrue(conditions, condition)
	}
}
