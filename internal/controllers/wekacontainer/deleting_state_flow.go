package wekacontainer

import (
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
)

// DeletingStateFlow returns the steps for a container in the deleting state
func DeletingStateFlow(r *containerReconcilerLoop) []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SingleStep{
			Run: r.GetNode,
		},
		&lifecycle.SingleStep{
			Run: r.refreshPod,
		},
		&lifecycle.SingleStep{
			Run: r.handleStateDeleting,
			Predicates: lifecycle.Predicates{
				r.container.IsDeletingState,
			},
		},
		// this will allow go back into deactivate flow if we detected that container joined the cluster
		// at this point we would be stuck on weka local stop if container just-joined cluster, while we decided to delete it
		&lifecycle.SingleStep{
			Name: "ensurePodOnDeletion",
			Run:  lifecycle.ForceNoError(r.ensurePod),
			Predicates: lifecycle.Predicates{
				r.PodNotSet,
				r.ShouldDeactivate,
				r.container.IsMarkedForDeletion,
				lifecycle.IsNotTrueCondition(condition.CondContainerRemoved, &r.container.Status.Conditions),
				lifecycle.IsNotFunc(r.container.IsS3Container), // no need to recover S3 container on deactivate
			},
		},
		&lifecycle.SingleStep{
			Condition:  condition.CondRemovedFromS3Cluster,
			CondReason: "Deletion",
			Run:        r.RemoveFromS3Cluster,
			Predicates: lifecycle.Predicates{
				r.ShouldDeactivate,
				r.container.IsS3Container,
			},
		},
		&lifecycle.SingleStep{
			Condition:  condition.CondRemovedFromNFS,
			CondReason: "Deletion",
			Run:        r.RemoveFromNfs,
			Predicates: lifecycle.Predicates{
				r.ShouldDeactivate,
				r.container.IsNfsContainer,
			},
		},
		//{
		//  Condition:  condition.CondContainerDrivesDeactivated,
		//  CondReason: "Deletion",
		//  Run:        loop.DeactivateDrives,
		//  Predicates: lifecycle.Predicates{
		//      loop.ShouldDeactivate,
		//      container.IsDriveContainer,
		//  },
		//  ,
		//},
		&lifecycle.SingleStep{
			Condition:  condition.CondContainerDeactivated,
			CondReason: "Deletion",
			Run:        r.DeactivateWekaContainer,
			Predicates: lifecycle.Predicates{
				r.ShouldDeactivate,
			},
		},
		&lifecycle.SingleStep{
			Run:        r.RemoveDeactivatedContainersDrives,
			Condition:  condition.CondContainerDrivesRemoved,
			CondReason: "Deletion",
			Predicates: lifecycle.Predicates{
				r.ShouldDeactivate,
				r.container.IsDriveContainer,
			},
		},
		&lifecycle.SingleStep{
			Run:        r.RemoveDeactivatedContainers,
			Condition:  condition.CondContainerRemoved,
			CondReason: "Deletion",
			Predicates: lifecycle.Predicates{
				r.ShouldDeactivate,
			},
		},
		&lifecycle.SingleStep{
			Name: "reconcileClusterStatusOnDeletion",
			Run:  lifecycle.ForceNoError(r.reconcileClusterStatus),
			Predicates: lifecycle.Predicates{
				r.container.ShouldJoinCluster,
				r.container.IsMarkedForDeletion,
				func() bool {
					return r.container.Status.ClusterContainerID == nil
				},
			},
		},
		&lifecycle.SingleStep{
			Run: r.stopForceAndEnsureNoPod, // we want to force stop drives to release
			Predicates: lifecycle.Predicates{
				r.container.IsMarkedForDeletion,
				lifecycle.Or(
					r.ShouldDeactivate, // if we were deactivating - we should also force stop, as we are safe at this point
					r.container.IsDestroyingState,
					func() bool {
						return r.container.Spec.GetOverrides().SkipDeactivate
					},
				),
				r.container.IsBackend, // if we needed to deactivate - we would not reach this point without deactivating
				// is it safe to force stop
			},
		},
		&lifecycle.SingleStep{
			Run: r.waitForMountsOrDrain,
			// we do not try to align with whether we did stop - if we did stop for a some reason - good, graceful will succeed after it, if not - this is a protection
			Predicates: lifecycle.Predicates{
				r.container.IsMarkedForDeletion,
				r.container.IsClientContainer,
				lifecycle.IsNotFunc(r.PodNotSet),
				func() bool {
					return !r.container.Spec.GetOverrides().SkipActiveMountsCheck
				},
			},
		},
		&lifecycle.SingleStep{
			Run: r.stopForceAndEnsureNoPod, // we do not rely on graceful stop on clients until we test multiple weka versions with it under various failures
			Predicates: lifecycle.Predicates{
				r.container.IsMarkedForDeletion,
				r.container.IsClientContainer,
				lifecycle.IsNotFunc(r.PodNotSet),
			},
		},
		&lifecycle.SingleStep{
			Run: r.stopAndEnsureNoPod,
			// we do not try to align with whether we did stop - if we did stop for a some reason - good, graceful will succeed after it, if not - this is a protection
			Predicates: lifecycle.Predicates{
				r.container.IsMarkedForDeletion,
				r.container.IsWekaContainer,
			},
		},
		&lifecycle.SingleStep{
			Condition:  condition.CondContainerDrivesResigned,
			CondReason: "Deletion",
			Run:        r.ResignDrives,
			Predicates: lifecycle.Predicates{
				r.container.IsMarkedForDeletion,
				lifecycle.IsNotFunc(r.CanSkipDrivesForceResign),
				r.container.IsDriveContainer,
			},
		},
		&lifecycle.SingleStep{
			Run: r.HandleDeletion,
			Predicates: lifecycle.Predicates{
				r.container.IsMarkedForDeletion,
			},
			FinishOnSuccess: true,
		},
	}
}
