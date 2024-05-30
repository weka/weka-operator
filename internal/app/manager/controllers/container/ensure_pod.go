package container

import (
	"context"
	e "errors"
	"fmt"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type PodCreationError struct {
	errors.WrappedError
	Container *wekav1alpha1.WekaContainer
}

func (e *PodCreationError) Error() string {
	return fmt.Sprintf("Error creating pod for container %s: %s", e.Container.Name, e.WrappedError.Error())
}

type PodCleanupError struct {
	errors.WrappedError
	Container *wekav1alpha1.WekaContainer
}

func (e *PodCleanupError) Error() string {
	return fmt.Sprintf("Error cleaning up pod for container %s: %s", e.Container.Name, e.WrappedError.Error())
}

type PodCleanupScheduled struct {
	Container *wekav1alpha1.WekaContainer
}

func (e *PodCleanupScheduled) Error() string {
	return fmt.Sprintf("Pod cleanup scheduled for container %s", e.Container.Name)
}

func (state *ContainerState) EnsurePod(c client.Client, crdManager services.CrdManager, kubeService services.KubeService, scheme *runtime.Scheme) lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsurePod")
		defer end()

		container := state.Subject
		if container == nil {
			return &errors.ArgumentError{ArgName: "container", Message: "container is nil"}
		}

		desiredPod, err := resources.NewContainerFactory(container).Create(ctx)
		if err != nil {
			return &PodCreationError{
				WrappedError: errors.WrappedError{Err: err},
				Container:    container,
			}
		}

		if err := ctrl.SetControllerReference(container, desiredPod, scheme); err != nil {
			return &PodCreationError{
				WrappedError: errors.WrappedError{Err: err},
				Container:    container,
			}
		}

		actualPod, err := crdManager.RefreshPod(ctx, container)
		state.Pod = actualPod
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.SetPhase("CREATING_POD")
				if err := c.Create(ctx, desiredPod); err != nil {
					return &PodCreationError{
						WrappedError: errors.WrappedError{Err: err},
						Container:    container,
					}
				}
				logger.SetPhase("POD_CREATED")
				return &lifecycle.RetryableError{Err: nil, RetryAfter: time.Second * 1}
			} else {
				logger.SetPhase("POD_REFRESH_ERROR")
				return &PodCreationError{
					WrappedError: errors.WrappedError{Err: err},
					Container:    container,
				}
			}
		}

		if actualPod == nil {
			return &PodCreationError{
				WrappedError: errors.WrappedError{Err: e.New("actual pod is nil")},
				Container:    container,
			}
		}

		logger.SetPhase("POD_ALREADY_EXISTS")
		if actualPod.Status.Phase == v1.PodPending {
			// Do we actually have a node that is assigned to it?
			if err := cleanupIfNeeded(ctx, c, kubeService, container, actualPod); err != nil {
				return err
			}
		}

		if actualPod.Status.Phase != v1.PodRunning {
			logger.SetPhase("POD_NOT_RUNNING")
			return &lifecycle.RetryableError{Err: nil, RetryAfter: time.Second * 3}
		}

		return nil
	}
}

func cleanupIfNeeded(ctx context.Context, c client.Client, kubeService services.KubeService, container *wekav1alpha1.WekaContainer, pod *v1.Pod) error {
	unschedulable := false
	unschedulableSince := time.Time{}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == v1.PodScheduled && condition.Status == v1.ConditionFalse && condition.Reason == "Unschedulable" {
			unschedulable = true
			unschedulableSince = condition.LastTransitionTime.Time
		}
	}

	if !unschedulable {
		return nil // cleanin up only unschedulable
	}

	if pod.Spec.NodeName != "" {
		return nil // cleaning only such that scheduled by node affinity
	}

	ctx, _, end := instrumentation.GetLogSpan(ctx, "CleanupIfNeeded")
	defer end()

	_, err := kubeService.GetNode(ctx, pod.Spec.NodeName)
	if !apierrors.IsNotFound(err) {
		return nil // node still exists, handling only not found node
	}

	// We are safe to delete clients after a configurable while
	// TODO: Make configurable, for now we delete after 5 minutes since downtime
	// relying onlastTransitionTime of Unschedulable condition
	rescheduleAfter := 5 * time.Minute
	if container.IsBackend() {
		rescheduleAfter = 3 * time.Second // TODO: Change, this is dev mode
	}
	if time.Since(unschedulableSince) > rescheduleAfter {
		if err := c.Delete(ctx, container); err != nil {
			return &PodCleanupError{
				WrappedError: errors.WrappedError{Err: err},
				Container:    container,
			}
		}
		return &PodCleanupScheduled{Container: container}
	}
	return nil
}
