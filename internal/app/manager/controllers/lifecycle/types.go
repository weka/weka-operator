package lifecycle

import (
	"context"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type (
	StepFunc func(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error
)

type Step struct {
	Condition     string
	Preconditions []PreconditionFunc
	Reconcile     StepFunc
}

type StatusClient interface {
	SetCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster, condType string, status metav1.ConditionStatus, reason string, message string) error
}

// -- PreconditionFuncs
type PreconditionFunc func(conditions []metav1.Condition) bool

func IsNotTrue(condition string) PreconditionFunc {
	return func(conditions []metav1.Condition) bool {
		return !meta.IsStatusConditionTrue(conditions, condition)
	}
}
