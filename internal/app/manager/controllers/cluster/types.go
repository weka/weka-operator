package cluster

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ClusterState struct {
	lifecycle.ReconciliationState[*wekav1alpha1.WekaCluster]
}

type ReconciliationSteps struct {
	Reconciler lifecycle.Reconciler
	State      *ReconciliationState
	Steps      []Step
}

type Step struct {
	// Name of the step.  This is usually a condition
	Condition string

	// Predicates must all be true for the step to be executed
	Predicates []lifecycle.PredicateFunc

	// Should the step be run if the condition is already true
	// Preconditions will also be evaluated and must be true
	SkipOwnConditionCheck bool

	// The function to execute
	Reconcile lifecycle.StepFunc
}

type StatusClient interface {
	SetCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster, condType string, status metav1.ConditionStatus, reason string, message string) error
	UpdateStatus(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error
}

type ReconciliationState struct {
	Cluster    *wekav1alpha1.WekaCluster
	Containers *[]*wekav1alpha1.WekaContainer
}
