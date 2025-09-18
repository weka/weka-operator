package wekacontainer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/controllers/resources"
)

func NodeAgentFlow(r *containerReconcilerLoop) []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{
			Run: r.GetNode,
		},
		&lifecycle.SimpleStep{
			Run: r.refreshPod,
		},
		&lifecycle.SimpleStep{
			Run: r.handleNodeAgentDeletion,
			Predicates: lifecycle.Predicates{
				func() bool {
					return r.container.DeletionTimestamp != nil
				},
			},
			FinishOnSuccess: true,
		},
		&lifecycle.SimpleStep{
			Run: r.initState,
		},
		&lifecycle.SimpleStep{
			Run: r.ensureFinalizer,
		},
		&lifecycle.SimpleStep{
			Run: r.updatePodMetadataOnChange,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.PodNotSet),
				r.podMetadataChanged,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.CleanupNodeAgentIfUnneeded,
			Predicates: lifecycle.Predicates{
				r.container.IsNodeAgentContainer,
			},
		},
		// update spec image if it was changed (can be fetched from env)
		// e.g. on operator re-deploy with different image
		&lifecycle.SimpleStep{
			Run: r.updateNodeAgentSpecImageIfChanged,
			Predicates: lifecycle.Predicates{
				r.nodeAgentSpecImageIsNotAligned,
			},
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name:   condition.CondContainerImageUpdated,
				Reason: "ImageUpdate",
			},
			Run: r.handleNodeAgentImageUpdate,
			Predicates: lifecycle.Predicates{
				func() bool {
					return r.container.Status.LastAppliedImage != ""
				},
				r.IsNotAlignedImage,
				lifecycle.IsNotFunc(r.PodNotSet),
			},
			SkipStepStateCheck: true,
		},
		&lifecycle.SimpleStep{
			Run: r.ensurePod,
			Predicates: lifecycle.Predicates{
				r.PodNotSet,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.deletePodIfUnschedulable,
			Predicates: lifecycle.Predicates{
				func() bool {
					// do not delete pod if node affinity is set on wekacontainer's spec
					return r.pod.Status.Phase == v1.PodPending && r.container.Spec.NodeAffinity == ""
				},
			},
		},
		&lifecycle.SimpleStep{
			Run: r.ensurePodNotRunningState,
			Predicates: lifecycle.Predicates{
				r.PodNotRunning,
			},
		},
		&lifecycle.SimpleStep{
			Run:   r.enforceNodeAffinity,
			State: &lifecycle.State{Name: condition.CondContainerAffinitySet},
			Predicates: lifecycle.Predicates{
				r.container.MustHaveNodeAffinity,
			},
		},
		&lifecycle.SimpleStep{
			Run: r.setNodeAffinityStatus,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(r.HasStatusNodeAffinity),
			},
		},
		&lifecycle.SimpleStep{
			Run: r.HandleNodeNotReady,
		},
		&lifecycle.SimpleStep{
			Run: r.WaitForPodRunning,
		},
		&lifecycle.SimpleStep{
			Run: r.setPodRunningStatus,
			Predicates: lifecycle.Predicates{
				func() bool {
					return r.container.Status.Status != weka.PodRunning
				},
			},
		},
		&lifecycle.SimpleStep{
			Run: r.applyCurrentImage,
			Predicates: lifecycle.Predicates{
				r.IsNotAlignedImage,
			},
		},
	}
}

func (r *containerReconcilerLoop) handleNodeAgentDeletion(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "handleNodeAgentDeletion")
	defer end()

	if !r.container.IsNodeAgentContainer() {
		return errors.New("container is not a node-agent container")
	}

	if r.pod != nil {
		err := r.deletePod(ctx, r.pod)
		if err != nil {
			return err
		}
		logger.AddEvent("Pod deleted")
	}

	controllerutil.RemoveFinalizer(r.container, resources.WekaFinalizer)
	err := r.Update(ctx, r.container)
	if err != nil {
		return fmt.Errorf("failed to remove finalizer: %w", err)
	}

	return nil
}

func (r *containerReconcilerLoop) updateNodeAgentSpecImageIfChanged(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "updateNodeAgentSpecImageIfChanged")
	defer end()

	desiredImage := resources.GetImageForNodeAgent(r.container)
	if r.container.Spec.Image == desiredImage {
		return nil
	}

	logger.Info("Updating spec image", "from", r.container.Spec.Image, "to", desiredImage)
	r.container.Spec.Image = desiredImage

	return r.Update(ctx, r.container)
}

func (r *containerReconcilerLoop) nodeAgentSpecImageIsNotAligned() bool {
	desiredImage := resources.GetImageForNodeAgent(r.container)
	if r.container.Spec.Image != desiredImage {
		return true
	}

	return false
}

func (r *containerReconcilerLoop) handleNodeAgentImageUpdate(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container
	pod := r.pod

	if pod.GetDeletionTimestamp() != nil {
		err := fmt.Errorf("pod is being deleted, waiting")
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*5)
	}

	if container.Spec.Image == container.Status.LastAppliedImage {
		return nil
	}

	var nodeAgentPodContainer v1.Container
	nodeAgentPodContainer, err := r.getNodeAgentPodContainer(pod)
	if err != nil {
		return err
	}

	if nodeAgentPodContainer.Image == container.Spec.Image {
		return nil
	}

	logger.Info("Updating pod to new image", "from", nodeAgentPodContainer.Image, "to", container.Spec.Image)

	err = r.deletePod(ctx, pod)
	if err != nil {
		return err
	}
	logger.AddEvent("Pod deleted to apply new image")

	return nil
}

func (r *containerReconcilerLoop) getNodeAgentPodContainer(pod *v1.Pod) (v1.Container, error) {
	for _, c := range pod.Spec.Containers {
		if c.Name == "node-agent" {
			return c, nil
		}
	}

	return v1.Container{}, fmt.Errorf("node-agent container not found in pod %s", pod.Name)
}
