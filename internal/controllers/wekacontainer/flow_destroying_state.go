package wekacontainer

import (
	"context"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"

	"github.com/weka/weka-operator/internal/config"
)

// DestroyingStateFlow returns the steps for a container in the destroying state
func DestroyingStateFlow(r *containerReconcilerLoop) []lifecycle.Step {
	steps1 := []lifecycle.Step{
		&lifecycle.SimpleStep{
			Run: r.GetNode,
		},
		&lifecycle.SimpleStep{
			Run: r.refreshPod,
		},
		&lifecycle.SimpleStep{
			Run: r.GetWekaClient,
			Predicates: lifecycle.Predicates{
				r.WekaContainerManagesCsi,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.FetchTargetCluster,
			Predicates: lifecycle.Predicates{
				func() bool {
					return r.wekaClient != nil && r.wekaClient.Spec.TargetCluster.Name != ""
				},
				lifecycle.BoolValue(config.Config.Csi.Enabled),
			},
		},
		// if cluster marked container state as destroying, update status and put deletion timestamp
		&lifecycle.SimpleStep{
			Run: r.handleStateDestroying,
		},
	}

	metricsSteps := MetricsSteps(r)

	steps2 := []lifecycle.Step{
		&lifecycle.SimpleStep{
			Name: "reconcileClusterStatusOnDeletion",
			Run:  r.reconcileClusterStatus,
			Predicates: lifecycle.Predicates{
				r.container.ShouldJoinCluster,
				func() bool {
					return r.container.Status.ClusterContainerID == nil
				},
			},
			ContinueOnError: true,
		},
		&lifecycle.SimpleStep{
			Run: r.stopForceAndEnsureNoPod,
			Predicates: lifecycle.Predicates{
				r.container.IsBackend,
				lifecycle.IsNotFunc(r.PodNotSet),
			},
		},
		&lifecycle.SimpleStep{
			Run: r.waitForMountsOrDrain,
			Predicates: lifecycle.Predicates{
				r.container.IsClientContainer,
				lifecycle.IsNotFunc(r.PodNotSet),
				func() bool {
					return !r.container.Spec.GetOverrides().SkipActiveMountsCheck
				},
			},
		},
		&lifecycle.SimpleStep{
			Run: r.stopForceAndEnsureNoPod,
			Predicates: lifecycle.Predicates{
				r.container.IsClientContainer,
				lifecycle.IsNotFunc(r.PodNotSet),
			},
		},
		&lifecycle.SimpleStep{
			Run: r.stopAndEnsureNoPod,
			// we do not try to align with whether we did stop - if we did stop for a some reason - good, graceful will succeed after it, if not - this is a protection
			Predicates: lifecycle.Predicates{
				r.container.IsWekaContainer,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.RemoveVirtualDrives,
			Predicates: lifecycle.Predicates{
				r.container.IsDriveContainer,
				r.container.UsesDriveSharing,
			},
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name:    condition.CondContainerDrivesResigned,
				Message: "Drives resigned",
				Reason:  "Destroying",
			},
			Run: r.ResignDrives,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.CanSkipDrivesForceResign),
				r.container.IsDriveContainer,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.HandleDeletion,
		},
	}

	return append(steps1, append(metricsSteps, steps2...)...)
}

func (r *containerReconcilerLoop) handleStateDestroying(ctx context.Context) error {
	statusUpdated := false

	if r.container.IsClientContainer() {
		activeMounts, err := r.getCachedActiveMounts(ctx)
		if err != nil {
			// Must be able to check active mounts before proceeding with destruction
			return err
		}
		if activeMounts != nil && *activeMounts > 0 {
			if err := r.updateContainerStatusIfNotEquals(ctx, weka.Draining); err != nil {
				return err
			}
			statusUpdated = true
		}
	}

	if !statusUpdated {
		if err := r.updateContainerStatusIfNotEquals(ctx, weka.Destroying); err != nil {
			return err
		}
	}

	if !r.container.IsMarkedForDeletion() {
		// self-delete
		err := r.Delete(ctx, r.container)
		if err != nil {
			return err
		}
		return lifecycle.NewWaitError(errors.New("Container is being deleting, refetching"))
	}
	return nil
}
