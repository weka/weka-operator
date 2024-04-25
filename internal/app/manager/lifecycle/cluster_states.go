package lifecycle

import (
	"context"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"

	ctrl "sigs.k8s.io/controller-runtime"
)

type InitState interface {
	HandleInit(ctx context.Context) error
}

type DeletingState interface {
	HandleDeletion(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error
	DoFinalizerOperationsForwekaCluster(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster) error
}

type ReconciliationPhase interface {
	Handle(ctx context.Context) (ctrl.Result, error)
	String() string
}

type StartingState interface{}

type IStartingSubState interface {
	Handle(ctx context.Context) error
	GetCluster() *wekav1alpha1.WekaCluster
}
