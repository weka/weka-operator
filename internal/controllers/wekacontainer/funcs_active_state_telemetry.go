package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/services/discovery"
)

const noComputeNeighborKey = "NoComputeNeighbor"

// getComputeNeighbor returns the compute WekaContainer on the same node as the telemetry container,
// or nil if none is found. Returns an error only on lookup failure.
func (r *containerReconcilerLoop) getComputeNeighbor(ctx context.Context) (*weka.WekaContainer, error) {
	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return nil, nil
	}

	ownerRefs := r.container.GetOwnerReferences()
	if len(ownerRefs) == 0 {
		return nil, errors.New("no owner references found")
	} else if len(ownerRefs) > 1 {
		return nil, errors.New("more than one owner reference found")
	}

	ownerUID := string(ownerRefs[0].UID)
	computeContainers, err := discovery.GetClusterContainersByClusterUID(ctx, r.Manager.GetClient(), ownerUID, r.container.Namespace, weka.WekaContainerModeCompute)
	if err != nil {
		return nil, err
	}

	for _, c := range computeContainers {
		if c.GetNodeAffinity() == nodeName {
			return c, nil
		}
	}
	return nil, nil
}

func (r *containerReconcilerLoop) handleTelemetryComputeNeighbor(ctx context.Context) error {
	if !r.container.IsTelemetry() {
		return nil
	}

	computeNeighbor, err := r.getComputeNeighbor(ctx)
	if err != nil {
		return err
	}

	if computeNeighbor == nil {
		return r.deleteTelemetryAfterGracePeriod(ctx)
	}

	// Clear stale "no neighbor" timestamp now that the neighbor is back.
	if _, ok := r.container.Status.Timestamps[noComputeNeighborKey]; ok {
		delete(r.container.Status.Timestamps, noComputeNeighborKey)
		if err := r.Status().Update(ctx, r.container); err != nil {
			return err
		}
	}
	return r.upgradeTelemetryOnComputeVersionMismatch(ctx, computeNeighbor)
}

func (r *containerReconcilerLoop) deleteTelemetryAfterGracePeriod(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "deleteTelemetryAfterGracePeriod")
	defer logger.End()

	nodeName := r.container.GetNodeAffinity()

	if r.container.Status.Timestamps == nil {
		r.container.Status.Timestamps = make(map[string]metav1.Time)
	}
	if since, ok := r.container.Status.Timestamps[noComputeNeighborKey]; !ok {
		r.container.Status.Timestamps[noComputeNeighborKey] = metav1.Time{Time: time.Now()}
		if err := r.Status().Update(ctx, r.container); err != nil {
			return err
		}

		return lifecycle.NewWaitErrorWithDuration(
			errors.New("Telemetry container has no compute neighbor, waiting before deleting it"),
			time.Second*15,
		)
	} else if time.Since(since.Time) < config.Config.DeleteTelemetryWithoutComputeNeighborTimeout {
		logger.Info("Telemetry container has no compute neighbor, but waiting before deleting it",
			"waited", time.Since(since.Time).String(),
			"node", nodeName,
		)
		return nil
	}

	_ = r.RecordEvent( //nolint:errcheck // error return value intentionally not checked
		v1.EventTypeNormal,
		"TelemetryContainerWithoutComputeNeighbor",
		"Telemetry container has no compute neighbor, deleting it",
	)

	if err := r.Delete(ctx, r.container); err != nil {
		return errors.Wrap(err, "failed to delete telemetry container")
	}

	// Clear the timestamp to avoid re-deleting the container on next reconcile
	delete(r.container.Status.Timestamps, noComputeNeighborKey)
	if err := r.Status().Update(ctx, r.container); err != nil {
		return errors.Wrap(err, "failed to update container status after deleting telemetry")
	}

	logger.Info("Telemetry container deleted as it has no compute neighbor")

	return nil
}

func (r *containerReconcilerLoop) upgradeTelemetryOnComputeVersionMismatch(ctx context.Context, computeNeighbor *weka.WekaContainer) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "upgradeTelemetryOnComputeVersionMismatch")
	defer logger.End()

	computeImage := computeNeighbor.Status.LastAppliedImage
	computeVersion := utils.GetSoftwareVersion(computeImage)
	telemetryVersion := utils.GetSoftwareVersion(r.container.Spec.Image)

	if computeVersion == "" || telemetryVersion == "" {
		return nil
	}

	cmp := utils.CompareVersions(computeVersion, telemetryVersion)
	if cmp == 0 {
		return nil
	}
	if cmp < 0 {
		_ = r.RecordEventThrottled( //nolint:errcheck // error return value intentionally not checked
			v1.EventTypeWarning,
			"TelemetryVersionAheadOfCompute",
			fmt.Sprintf("telemetry image version %s is ahead of compute lastAppliedImage version %s", telemetryVersion, computeVersion),
			10*time.Minute,
		)
		return nil
	}

	logger.Info("Upgrading telemetry container image to match compute neighbor",
		"from", r.container.Spec.Image,
		"to", computeImage,
	)
	_ = r.RecordEvent( //nolint:errcheck // error return value intentionally not checked
		v1.EventTypeNormal,
		"TelemetryImageAutoUpgrade",
		fmt.Sprintf("auto-upgrading telemetry image from %s to %s to match compute neighbor", r.container.Spec.Image, computeImage),
	)

	patchBytes, err := json.Marshal(map[string]any{
		"spec": map[string]any{"image": computeImage},
	})
	if err != nil {
		return fmt.Errorf("failed to marshal image patch: %w", err)
	}

	if err = r.Patch(ctx, r.container, client.RawPatch(types.MergePatchType, patchBytes)); err != nil {
		return fmt.Errorf("failed to patch telemetry container image: %w", err)
	}

	if r.pod != nil {
		if err = r.deletePod(ctx, r.pod); err != nil {
			return errors.Wrap(err, "failed to delete telemetry pod for image upgrade")
		}
	}

	return lifecycle.NewWaitError(errors.New("wait for telemetry pod to restart with upgraded image"))
}
