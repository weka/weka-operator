package wekacontainer

import (
	"context"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
)

func (r *containerReconcilerLoop) handlePodTermination(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	node := r.node
	pod := r.pod
	container := r.container
	upgradeRunning := false

	// TODO: do we actually use instructions on non-weka containers in weka_runtime? Consider when breaking out into steps
	// Consider also generating a python mapping along with version script so we can use stuff like IsWekaContainer on python side
	if r.container.IsWekaContainer() && !r.container.IsDriversBuilder() {
		if err := r.updateContainerStatusIfNotEquals(ctx, weka.PodTerminating); err != nil {
			return err
		}
	}

	skipExec := false
	if r.node != nil {
		skipExec = strings.Contains(r.node.Status.NodeInfo.ContainerRuntimeVersion, "cri-o")
	}

	if r.node == nil {
		return nil
	}

	if container.Spec.Image != container.Status.LastAppliedImage && container.Status.LastAppliedImage != "" {
		var wekaPodContainer v1.Container
		wekaPodContainer, err := resources.GetWekaPodContainer(pod)
		if err != nil {
			return err
		}

		if wekaPodContainer.Image != container.Spec.Image {
			upgradeRunning = true
		}
	}

	if r.container.Spec.GetOverrides().PodDeleteForceReplace {
		_ = r.writeAllowForceStopInstruction(ctx, pod, skipExec)
		return r.runWekaLocalStop(ctx, pod, true)
	}

	if r.container.Spec.GetOverrides().UpgradeForceReplace {
		if upgradeRunning {
			_ = r.writeAllowForceStopInstruction(ctx, pod, skipExec)
			return r.runWekaLocalStop(ctx, pod, true)
		}
	}

	if container.IsBackend() && config.Config.EvictContainerOnDeletion && !(container.IsComputeContainer() && container.Spec.GetOverrides().UpgradePreventEviction) && !(container.IsS3Container()) {
		// unless overrides were used, we are not allowing container to stop on-pod-deletion
		// unless this was a force delete, or a force-upgrade scenario, we are not allowing container to stop on-pod-deletion and unless going deactivate flow
		logger.Info("Evicting container on pod deletion")
		err := r.ensureStateDeleting(ctx)
		if err != nil {
			return err
		}
		return lifecycle.NewWaitError(errors.New("evicting container on pod deletion"))
	}

	if container.HasFrontend() {
		// node drain or cordon detected
		if node.Spec.Unschedulable {
			logger.Info("Node is unschedulable, checking active mounts")

			ok, err := r.noActiveMountsRestriction(ctx)
			if err != nil {
				return err
			}
			if !ok {
				err := errors.New("Node is unschedulable and has active mounts")
				return err
			}
		}

		// upgrade detected
		if upgradeRunning {
			logger.Info("Upgrade detected")
			err := r.runFrontendUpgradePrepare(ctx)
			if err != nil && errors.Is(err, &NoWekaFsDriverFound{}) {
				logger.Info("No wekafs driver found, skip prepare-upgrade")
			} else if err != nil {
				return err
			}
		}
	}

	err := r.writeAllowStopInstruction(ctx, pod, skipExec)
	if err != nil {
		logger.Error(err, "Error writing allow stop instruction")
		return err
	}

	// stop weka local if container has agent
	if r.container.HasAgent() {
		// check instruction in pod
		forceStop, err := r.checkAllowForceStopInstruction(ctx, pod)
		if err != nil {
			return err
		}

		// TODO: changing api to get IsComputeContainer is too much, we should have out-of-api helper functions
		if r.container.Status.Timestamps == nil {
			r.container.Status.Timestamps = make(map[string]metav1.Time)
		}
		if container.IsDriveContainer() || container.IsComputeContainer() {
			if since, ok := r.container.Status.Timestamps[string(weka.TimestampStopAttempt)]; !ok {
				r.container.Status.Timestamps[string(weka.TimestampStopAttempt)] = metav1.Time{Time: time.Now()}
				if err := r.Status().Update(ctx, r.container); err != nil {
					return err
				}
			} else {
				// Get timeout from cluster overrides
				deactivationTimeout := 5 * time.Minute // default value
				cluster, err := r.getCluster(ctx)
				if err != nil {
					return err
				} else if cluster.Spec.GetOverrides().PodTerminationDeactivationTimeout != nil {
					timeoutDuration := cluster.Spec.GetOverrides().PodTerminationDeactivationTimeout.Duration
					if timeoutDuration == 0 {
						// 0 means never deactivate - set to a very large duration
						deactivationTimeout = time.Duration(0)
					} else {
						deactivationTimeout = timeoutDuration
					}
				}

				// Only deactivate if timeout is not 0 (disabled) and has exceeded
				shouldDeactivate := deactivationTimeout > 0 && time.Since(since.Time) > deactivationTimeout
				if shouldDeactivate && !(container.Spec.GetOverrides().MigrateOutFromPvc && container.Spec.PVC != nil) {
					// lets start deactivate flow, we are doing it by deleting weka container
					if err := r.ensureStateDeleting(ctx); err != nil {
						return err
					} else {
						return lifecycle.NewWaitError(errors.New("deleting weka container"))
					}
				}
			}
		}

		logger.Debug("Stopping weka local", "force", forceStop)

		if forceStop {
			logger.Info("Force stop instruction found")
			err = r.runWekaLocalStop(ctx, pod, true)
		} else {
			err = r.runWekaLocalStop(ctx, pod, false)
		}

		if err != nil {
			logger.Error(err, "Error stopping weka local")
			return err
		}
	}
	return nil
}
