package wekacontainer

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/pkg/util"
)

func (r *containerReconcilerLoop) upgradeConditionsPass(ctx context.Context) (bool, error) {
	// Necessary conditions for FE upgrade:
	// 1. all FE containers on single host should have same image
	// 2. check active mounts == 0
	if !r.container.HasFrontend() {
		return true, nil
	}

	if r.container.Status.LastAppliedImage == "" {
		// first time, no image to compare
		return true, nil
	}

	// skip all checks if hot upgrade is allowed
	if r.container.Spec.AllowHotUpgrade {
		return true, nil
	}

	ok, err := r.noActiveMountsRestriction(ctx)
	if !ok || err != nil {
		return false, err
	}

	nodeName := r.container.GetNodeAffinity()
	// get all frontend pods on same node
	pods, err := r.getFrontendPodsOnNode(ctx, string(nodeName))
	if err != nil {
		return false, err
	}

	// check if all pods have same image
	for _, pod := range pods {
		for _, podContainer := range pod.Spec.Containers {
			if r.pod != nil && pod.UID == r.pod.UID {
				// skip self
				continue
			}
			if podContainer.Name == "weka-container" {
				if podContainer.Image != r.container.Spec.Image {
					err := fmt.Errorf("pod %s on same node %s has different image %s", pod.Name, nodeName, podContainer.Image)
					return false, err
				}
			}
		}
	}
	return true, nil
}

func (r *containerReconcilerLoop) handleImageUpdate(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "handleImageUpdate")
	defer end()

	container := r.container
	pod := r.pod

	upgradeType := container.Spec.UpgradePolicyType
	if upgradeType == "" {
		upgradeType = weka.UpgradePolicyTypeManual
	}

	var wekaPodContainer v1.Container
	wekaPodContainer, err := r.getWekaPodContainer(pod)
	if err != nil {
		return err
	}

	if container.Spec.Image != container.Status.LastAppliedImage {
		if container.Spec.GetOverrides().UpgradeForceReplace {
			if pod.GetDeletionTimestamp() != nil {
				logger.Info("Pod is being deleted, waiting")
				return errors.New("Pod is being deleted, waiting")
			}
			if wekaPodContainer.Image == container.Spec.Image {
				return nil
			}
			err := r.writeAllowForceStopInstruction(ctx, pod, false)
			if err != nil {
				logger.Error(err, "Error writing allow force stop instruction")
				return err
			}
			return r.deletePod(ctx, pod)
		}

		canUpgrade, err := r.upgradeConditionsPass(ctx)
		if err != nil || !canUpgrade {
			err := fmt.Errorf("cannot upgrade: %w", err)

			// if we are in all-at-once upgrade mode, check if we already
			// have CondContainerImageUpdated set to false with the same reason
			// In this case, consider this as expected error
			if container.Spec.UpgradePolicyType == weka.UpgradePolicyTypeAllAtOnce {
				cond := meta.FindStatusCondition(container.Status.Conditions, condition.CondContainerImageUpdated)
				if cond != nil && cond.Status == metav1.ConditionFalse && cond.Message == err.Error() {
					return lifecycle.NewExpectedError(err)
				}
			}

			return err
		}

		if wekaPodContainer.Image != container.Spec.Image {
			if container.HasFrontend() {
				err = r.runFrontendUpgradePrepare(ctx)
				if err != nil && errors.Is(err, &NoWekaFsDriverFound{}) {
					logger.Info("No wekafs driver found, force terminating pod")
					err := r.writeAllowForceStopInstruction(ctx, pod, false)
					if err != nil {
						logger.Error(err, "Error writing allow force stop instruction")
						return err
					}
					return r.deletePod(ctx, pod)
				}
				if err != nil {
					return err
				}
			}
			if container.Spec.Mode == weka.WekaContainerModeClient && container.Spec.UpgradePolicyType == weka.UpgradePolicyTypeManual {
				// leaving client delete operation to user and we will apply lastappliedimage if pod got restarted
				return nil
			}

			logger.Info("Deleting pod to apply new image")
			// delete pod
			err = r.deletePod(ctx, pod)
			if err != nil {
				return err
			}

			return nil
		}

		if pod.GetDeletionTimestamp() != nil {
			logger.Info("Pod is being deleted, waiting")
			return errors.New("Pod is being deleted, waiting")
		}
	}
	return nil
}

func (r *containerReconcilerLoop) runFrontendUpgradePrepare(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "runFrontendUpgradePrepare")
	defer end()

	container := r.container
	pod := r.pod

	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		return err
	}

	if !container.Spec.AllowHotUpgrade {
		logger.Debug("Hot upgrade is not enabled, issuing weka local stop")
		err := r.runWekaLocalStop(ctx, pod, false)
		if err != nil {
			return err
		}
	} else {
		logger.Info("Running prepare-upgrade")
		err = r.runDriverPrepareUpgrade(ctx, executor)
		if err != nil {
			return err
		}
		logger.Debug("Hot upgrade is enabled, skipping driver removal")
	}
	return nil
}

func (r *containerReconcilerLoop) runDriverPrepareUpgrade(ctx context.Context, executor util.Exec) error {
	_, stderr, err := executor.ExecNamed(ctx, "PrepareForUpgrade", []string{"bash", "-ce", `
if lsmod | grep wekafsio; then
	if [ -e /proc/wekafs/interface ]; then
		if ! echo prepare-upgrade > /proc/wekafs/interface; then
			echo "Failed to run prepare-upgrade command attempting rmmod instead"
			rmmod wekafsio
		fi 
	fi
fi
`})
	if err != nil && strings.Contains(stderr.String(), "No such file or directory") {
		err = &NoWekaFsDriverFound{}
		return err
	}
	if err != nil {
		err = fmt.Errorf("error running prepare-upgrade: %w, stderr: %s", err, stderr.String())
		return err
	}
	return nil
}
