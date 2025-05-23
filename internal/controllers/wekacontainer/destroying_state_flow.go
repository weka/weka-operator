package wekacontainer

import (
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
)

// DestroyingStateFlow returns the steps for a container in the destroying state
func DestroyingStateFlow(r *containerReconcilerLoop) []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SingleStep{
			Run: r.GetNode,
		},
		&lifecycle.SingleStep{
			Run: r.refreshPod,
		},
		// if cluster marked container state as destroying, update status and put deletion timestamp
		&lifecycle.SingleStep{
			Run: r.handleStateDestroying,
			Predicates: lifecycle.Predicates{
				r.container.IsDestroyingState,
			},
		},
		&lifecycle.SingleStep{
			Run: r.stopForceAndEnsureNoPod,
			Predicates: lifecycle.Predicates{
				r.container.IsMarkedForDeletion,
				r.container.IsBackend,
			},
		},
		&lifecycle.SingleStep{
			Run: r.waitForMountsOrDrain,
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
			Run: r.stopForceAndEnsureNoPod,
			Predicates: lifecycle.Predicates{
				r.container.IsMarkedForDeletion,
				r.container.IsClientContainer,
				lifecycle.IsNotFunc(r.PodNotSet),
			},
		},
		//TODO: Should we wait for mounts to go away on client before stopping on delete?
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
			CondReason: "Destroying",
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
