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

type StatusClient interface {
	SetCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster, condType string, status metav1.ConditionStatus, reason string, message string) error
	UpdateStatus(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error
}
