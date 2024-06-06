package cluster

import (
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
)

func (state *ClusterState) StandardReconciliationSteps(clusterStatusInit string, wekaFinalizer string) *lifecycle.ReconciliationSteps[*wekav1alpha1.WekaCluster] {
	steps := &lifecycle.ReconciliationSteps[*wekav1alpha1.WekaCluster]{
		Reconciler: state.Client,
		State:      &state.ReconciliationState,
		Steps: []lifecycle.Step[*wekav1alpha1.WekaCluster]{
			{
				Condition:             "InitState",
				SkipOwnConditionCheck: true,
				Reconcile:             state.InitState(clusterStatusInit, wekaFinalizer),
			},
			{
				Condition: "HandleDeletion",
				Predicates: []lifecycle.PredicateFunc[*wekav1alpha1.WekaCluster]{
					func(state *lifecycle.ReconciliationState[*wekav1alpha1.WekaCluster]) bool {
						return state.Subject.GetDeletionTimestamp() != nil
					},
				},
				Reconcile: state.HandleDeletion(wekaFinalizer),
			},
			{
				Condition:             "ClusterSecretsCreated",
				Predicates:            []lifecycle.PredicateFunc[*wekav1alpha1.WekaCluster]{}, // default value
				SkipOwnConditionCheck: false,                                                  // default value
				Reconcile:             state.ClusterSecretsCreated(),
			},
			{
				Condition:             condition.CondPodsCreated,
				SkipOwnConditionCheck: true,
				Reconcile:             state.PodsCreated(),
			},
			{
				Condition: condition.CondPodsReady,
				Reconcile: state.PodsReady(),
				Predicates: []lifecycle.PredicateFunc[*wekav1alpha1.WekaCluster]{
					lifecycle.IsTrue[*wekav1alpha1.WekaCluster](condition.CondPodsCreated),
				},
			},
			{
				Condition: condition.CondClusterCreated,
				Reconcile: state.ClusterCreated(),
			},
			{
				Condition: condition.CondJoinedCluster,
				Reconcile: state.ContainersJoinedCluster(),
			},

			{
				Condition: condition.CondDrivesAdded,
				Reconcile: state.DrivesAdded(),
			},
			{
				Condition: condition.CondIoStarted,
				Reconcile: state.StartIo(),
			},
			{
				Condition: condition.CondClusterSecretsApplied,
				Predicates: []lifecycle.PredicateFunc[*wekav1alpha1.WekaCluster]{
					lifecycle.IsTrue[*wekav1alpha1.WekaCluster](condition.CondIoStarted),
				},
				Reconcile: state.ApplyClusterSecrets(),
			},
			{
				Condition: condition.CondDefaultFsCreated,
				Reconcile: state.DefaultFsCreated(),
			},
			{
				Condition: condition.CondS3ClusterCreated,
				Reconcile: state.S3ClusterCreated(),
			},
			{
				Condition: condition.CondClusterClientSecretsCreated,
				Reconcile: state.ClusterClientSecretsCreated(),
			},
			{
				Condition: condition.CondClusterClientSecretsApplied,
				Predicates: []lifecycle.PredicateFunc[*wekav1alpha1.WekaCluster]{
					lifecycle.IsTrue[*wekav1alpha1.WekaCluster](condition.CondClusterClientSecretsCreated),
				},
				Reconcile: state.ClusterClientSecretsApplied(),
			},

			{
				Condition: condition.CondClusterCSISecretsCreated,
				Reconcile: state.ClusterCSISecretsCreated(),
			},
			{
				Condition: condition.CondClusterCSISecretsApplied,
				Reconcile: state.ClusterCSISecretsApplied(),
			},
		},
	}

	return steps
}
