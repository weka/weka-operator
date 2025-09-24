package wekacontainer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// DeletingStateFlow returns the steps for a container in the deleting state
func DeletingStateFlow(r *containerReconcilerLoop) []lifecycle.Step {
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
				lifecycle.BoolValue(config.Config.CsiInstallationEnabled),
			},
		},
		// if cluster marked container state as deleting, update status and put deletion timestamp
		&lifecycle.SimpleStep{
			Run: r.handleStateDeleting,
		},
		// this will allow go back into deactivate flow if we detected that container joined the cluster
		// at this point we would be stuck on weka local stop if container just-joined cluster, while we decided to delete it
		&lifecycle.SimpleStep{
			Name: "ensurePodOnDeletion",
			Run:  r.ensurePod,
			Predicates: lifecycle.Predicates{
				r.PodNotSet,
				r.ShouldDeactivate,
				lifecycle.IsNotTrueCondition(condition.CondContainerDeactivated, &r.container.Status.Conditions),
				lifecycle.IsNotFunc(r.container.IsS3Container), // no need to recover S3 container on deactivate
			},
			ContinueOnError: true,
		},
		&lifecycle.SimpleStep{
			Name: "ReconcileWekaLocalStatusOnDeletion",
			Run:  r.reconcileWekaLocalStatus,
			Predicates: lifecycle.Predicates{
				r.container.IsWekaContainer,
				r.ShouldDeactivate,
				r.PodIsSet,
				lifecycle.IsNotTrueCondition(condition.CondContainerDeactivated, &r.container.Status.Conditions),
			},
		},
	}

	csiSteps := CsiSteps(r)
	metricsSteps := MetricsSteps(r)

	steps2 := []lifecycle.Step{
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name:   condition.CondRemovedFromS3Cluster,
				Reason: "Deletion",
			},
			Run: r.RemoveFromS3Cluster,
			Predicates: lifecycle.Predicates{
				r.ShouldDeactivate,
				r.container.IsS3Container,
			},
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name:   condition.CondRemovedFromNFS,
				Reason: "Deletion",
			},
			Run: r.RemoveFromNfs,
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
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name:   condition.CondContainerDeactivated,
				Reason: "Deletion",
			},
			Run: r.DeactivateWekaContainer,
			Predicates: lifecycle.Predicates{
				r.ShouldDeactivate,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.RemoveDeactivatedContainersDrives,
			State: &lifecycle.State{
				Name:   condition.CondContainerDrivesRemoved,
				Reason: "Deletion",
			},
			Predicates: lifecycle.Predicates{
				r.ShouldDeactivate,
				r.container.IsDriveContainer,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.RemoveDeactivatedContainers,
			State: &lifecycle.State{
				Name:   condition.CondContainerRemoved,
				Reason: "Deletion",
			},
			Predicates: lifecycle.Predicates{
				r.ShouldDeactivate,
			},
		},
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
			Run: r.stopForceAndEnsureNoPod, // we want to force stop drives to release
			Predicates: lifecycle.Predicates{
				lifecycle.Or(
					r.ShouldDeactivate, // if we were deactivating - we should also force stop, as we are safe at this point
					func() bool {
						return r.container.Spec.GetOverrides().SkipDeactivate
					},
				),
				r.container.IsBackend, // if we needed to deactivate - we would not reach this point without deactivating
				// is it safe to force stop
			},
		},
		&lifecycle.SimpleStep{
			Run: r.waitForMountsOrDrain,
			// we do not try to align with whether we did stop - if we did stop for a some reason - good, graceful will succeed after it, if not - this is a protection
			Predicates: lifecycle.Predicates{
				r.container.IsClientContainer,
				lifecycle.IsNotFunc(r.PodNotSet),
				func() bool {
					return !r.container.Spec.GetOverrides().SkipActiveMountsCheck
				},
			},
		},
		&lifecycle.SimpleStep{
			Run: r.stopForceAndEnsureNoPod, // we do not rely on graceful stop on clients until we test multiple weka versions with it under various failures
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
			State: &lifecycle.State{
				Name:   condition.CondContainerDrivesResigned,
				Reason: "Deletion",
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

	steps := append(steps1, csiSteps...)
	steps = append(steps, metricsSteps...)
	steps = append(steps, steps2...)
	return steps
}

func (r *containerReconcilerLoop) handleStateDeleting(ctx context.Context) error {
	statusUpdated := false

	if r.container.IsClientContainer() {
		activeMounts, _ := r.getCachedActiveMounts(ctx)
		if activeMounts != nil && *activeMounts > 0 {
			if err := r.updateContainerStatusIfNotEquals(ctx, weka.Draining); err != nil {
				return err
			}
			statusUpdated = true
		}
	}

	if !statusUpdated {
		if err := r.updateContainerStatusIfNotEquals(ctx, weka.Deleting); err != nil {
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

func (r *containerReconcilerLoop) RemoveFromS3Cluster(ctx context.Context) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	err := r.stopAndEnsureNoPod(ctx)
	if err != nil {
		return err
	}

	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		return nil
	}

	containers, err := r.getClusterContainers(ctx)
	if err != nil {
		return err
	}
	executeInContainer := discovery.SelectActiveContainer(containers)

	wekaService := services.NewWekaService(r.ExecService, executeInContainer)
	return wekaService.RemoveFromS3Cluster(ctx, *containerId)
}

func (r *containerReconcilerLoop) RemoveFromNfs(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		return errors.New("Container ID is not set")
	}

	executeInContainer := r.container

	if !NodeIsReady(r.node) || !CanExecInPod(r.pod) {
		containers, err := r.getClusterContainers(ctx)
		if err != nil {
			return err
		}
		executeInContainer = discovery.SelectActiveContainer(containers)
	}

	if executeInContainer == nil {
		return errors.New("No active container found")
	}

	logger.Info("Removing container from NFS", "container_id", *containerId)

	wekaService := services.NewWekaService(r.ExecService, executeInContainer)
	return wekaService.RemoveNfsInterfaceGroups(ctx, *containerId)
}

func (r *containerReconcilerLoop) DeactivateWekaContainer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		return errors.New("Container ID is not set")
	}

	if r.container.IsS3Container() {
		err := r.stopAndEnsureNoPod(ctx)
		if err != nil {
			return err
		}
	}

	containers, err := r.getClusterContainers(ctx)
	if err != nil {
		return err
	}

	execInContainer := discovery.SelectActiveContainer(containers)
	if execInContainer == nil {
		return errors.New("No active container found")
	}

	timeout := 30 * time.Second
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, execInContainer, &timeout)

	wekaContainer, err := wekaService.GetWekaContainer(ctx, *containerId)
	if err != nil {
		return err
	}

	logger.Info("Container status", "container_id", *containerId, "status", wekaContainer.Status)

	switch wekaContainer.Status {
	case "INACTIVE":
		// nothing to do
		return nil
	case "DEACTIVATING":
		return lifecycle.NewWaitErrorWithDuration(
			errors.New("container is deactivating"),
			time.Second*15,
		)
	default:
		logger.Info("Deactivating container", "container_id", *containerId)

		err := wekaService.DeactivateContainer(ctx, *containerId)
		if err != nil {
			return errors.Wrapf(err, "failed to deactivate container via %s", execInContainer.Name)
		}

		return lifecycle.NewWaitErrorWithDuration(
			errors.New("container deactivation started"),
			time.Second*15,
		)
	}
}

func (r *containerReconcilerLoop) RemoveDeactivatedContainersDrives(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		err := errors.New("Container ID is not set")
		return err
	}

	containers, err := r.getClusterContainers(ctx)
	if err != nil {
		return err
	}

	execInContainer := discovery.SelectActiveContainer(containers)
	if execInContainer == nil {
		return errors.New("No active container found")
	}

	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	drives, err := wekaService.ListContainerDrives(ctx, *containerId)
	if err != nil {
		return err
	}
	logger.Info("Removing drives for container", "container_id", *containerId, "drives", drives)

	var errs []error
	for _, drive := range drives {
		err := wekaService.RemoveDrive(ctx, drive.Uuid)
		if err != nil {
			errs = append(errs, err)
		} else {
			logger.Info("Drive removed", "drive_uuid", drive.Uuid, "container_id", *containerId)
		}
	}
	if len(errs) > 0 {
		err = fmt.Errorf("failed to remove drives for container %d: %v", *containerId, errs)
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) RemoveDeactivatedContainers(ctx context.Context) error {
	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		return errors.New("Container ID is not set")
	}

	// do not reactivate more then one container per role per minute
	reset := func() {}
	if config.Config.RemovalThrottlingEnabled {
		if !r.container.IsS3Container() {
			throttler := r.ThrottlingMap.WithPartition("cluster/" + r.container.Status.ClusterID + "/" + r.container.Spec.Mode)
			if !throttler.ShouldRun("removeDeactivatedContainers", &throttling.ThrottlingSettings{
				Interval:                    time.Minute,
				DisableRandomPreSetInterval: true,
			}) {
				return lifecycle.NewWaitErrorWithDuration(
					errors.New("throttling removal of containers from weka"),
					time.Second*15,
				)
			}
			reset = func() {
				throttler.Reset("removeDeactivatedContainers")
			}
		}
	}

	err := r.removeDeactivatedContainers(ctx, *containerId)
	if err != nil {
		// in case of error - we do not want to throttle
		reset()
		return err
	}

	return nil
}

func (r *containerReconcilerLoop) removeDeactivatedContainers(ctx context.Context, containerId int) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containers, err := r.getClusterContainers(ctx)
	if err != nil {
		return err
	}

	execInContainer := discovery.SelectActiveContainer(containers)
	if execInContainer == nil {
		return errors.New("No active container found")
	}

	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	logger.Info("Removing container", "container_id", containerId)

	err = wekaService.RemoveContainer(ctx, containerId)
	if err != nil {
		err = errors.Wrap(err, "Failed to remove container")
		return err
	}

	// Verify that the container was actually removed from the cluster
	_, err = wekaService.GetWekaContainer(ctx, containerId)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			logger.Info("Container successfully removed from cluster", "container_id", containerId)
			return nil
		}
		// Other errors might be temporary, so we wait and retry
		logger.Warn("Error checking container removal status", "container_id", containerId, "error", err)
		return lifecycle.NewWaitErrorWithDuration(
			errors.Wrapf(err, "Failed to verify container %d removal", containerId),
			time.Second*15,
		)
	}

	// Container still exists in cluster, wait and retry
	logger.Info("Container still exists in cluster, waiting for removal to complete", "container_id", containerId)
	return lifecycle.NewWaitErrorWithDuration(
		fmt.Errorf("container %d still exists in cluster", containerId),
		time.Second*15,
	)
}
