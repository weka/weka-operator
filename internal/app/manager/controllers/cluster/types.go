package cluster

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ClusterState struct {
	lifecycle.ReconciliationState[*wekav1alpha1.WekaCluster]

	Client             client.Client
	CrdManager         services.CrdManager
	ExecService        services.ExecService
	Manager            ctrl.Manager
	Recorder           record.EventRecorder
	SecretsService     services.SecretsService
	WekaClusterService services.WekaClusterService
}

func (state *ClusterState) NewWekaClusterService() (services.WekaClusterService, error) {
	if state.Subject == nil {
		return nil, &lifecycle.StateError{Property: "Subject", Message: "Subject is nil"}
	}
	return services.NewWekaClusterService(state.Manager, state.Subject), nil
}

type StatusClient interface {
	SetCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster, condType string, status metav1.ConditionStatus, reason string, message string) error
	UpdateStatus(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error
}
