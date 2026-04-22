package wekaclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-lib/pkg/workers"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (c *clientReconcilerLoop) targetClusterIsPaused() bool {
	if c.targetCluster == nil {
		return false
	}
	p := c.targetCluster.Spec.GetOverrides().Paused
	return p != nil && *p
}

func (c *clientReconcilerLoop) targetClusterIsExplicitlyUnpaused() bool {
	if c.targetCluster == nil {
		return false
	}
	p := c.targetCluster.Spec.GetOverrides().Paused
	return p != nil && !*p
}

func (c *clientReconcilerLoop) hasPausedContainers() bool {
	for _, container := range c.containers {
		if container.IsPaused() {
			return true
		}
	}
	return false
}

func (c *clientReconcilerLoop) handleTargetClusterPause(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	err := c.ensureClientContainersPaused(ctx)
	if err != nil {
		return err
	}

	logger.Info("Client containers paused due to target cluster pause", "targetCluster", c.targetCluster.Name)
	return nil
}

func (c *clientReconcilerLoop) recoverPausedClientContainers(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	err := c.ensureClientContainersNotPaused(ctx)
	if err != nil {
		return err
	}

	logger.Info("Client containers unpaused due to target cluster unpause", "targetCluster", c.targetCluster.Name)
	return nil
}

func (c *clientReconcilerLoop) ensureClientContainersPaused(ctx context.Context) error {
	return workers.ProcessConcurrently(ctx, c.containers, 32, func(ctx context.Context, container *weka.WekaContainer) error {
		ctx, spanLogger := instrumentation.CreateLogSpan(ctx, "ensureClientContainerPaused", "container", container.Name)
		defer spanLogger.End()

		if !container.IsPaused() {
			patch := map[string]any{
				"spec": map[string]any{
					"state": weka.ContainerStatePaused,
				},
			}

			patchBytes, err := json.Marshal(patch)
			if err != nil {
				return fmt.Errorf("failed to marshal patch for container %s: %w", container.Name, err)
			}

			err = errors.Wrap(
				c.Patch(ctx, container, client.RawPatch(types.MergePatchType, patchBytes)),
				fmt.Sprintf("failed to update container state %s", container.Name),
			)
			if err != nil {
				return err
			}
		}

		if container.Status.Status != weka.Paused {
			return fmt.Errorf("container %s is not paused yet", container.Name)
		}

		return nil
	}).AsError()
}

func (c *clientReconcilerLoop) ensureClientContainersNotPaused(ctx context.Context) error {
	return workers.ProcessConcurrently(ctx, c.containers, 32, func(ctx context.Context, container *weka.WekaContainer) error {
		ctx, spanLogger := instrumentation.CreateLogSpan(ctx, "ensureClientContainerNotPaused", "container", container.Name)
		defer spanLogger.End()

		if container.IsPaused() {
			patch := map[string]any{
				"spec": map[string]any{
					"state": weka.ContainerStateActive,
				},
			}

			patchBytes, err := json.Marshal(patch)
			if err != nil {
				return fmt.Errorf("failed to marshal patch for container %s: %w", container.Name, err)
			}

			err = errors.Wrap(
				c.Patch(ctx, container, client.RawPatch(types.MergePatchType, patchBytes)),
				fmt.Sprintf("failed to update container state %s", container.Name),
			)
			if err != nil {
				return err
			}
		}

		if container.Status.Status == weka.Paused {
			return fmt.Errorf("container %s is still paused", container.Name)
		}

		return nil
	}).AsError()
}
