package wekacontainer

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

const (
	clusterContainerCheckTimeout = 30 * time.Second
	clusterContainerCheckRetry   = 15 * time.Second
)

// resolveClusterExecContainer returns a container to run cluster-scoped weka CLI commands in, or nil
// when there is none and the caller should skip the check.
//
// Client pods run restricted with the cluster's low-privilege `regular` user, so `weka cluster ...`
// from a client is capped at RegularUser - clients have to go through a backend of their target
// cluster. They are also owned by a WekaClient, so getClusterContainers() (owner UID matched against
// WekaClusters) never resolves for them.
func (r *containerReconcilerLoop) resolveClusterExecContainer(ctx context.Context) (*weka.WekaContainer, error) {
	if !r.container.IsClientContainer() {
		containers, err := r.getClusterContainers(ctx)
		if err != nil {
			return nil, fmt.Errorf("error getting cluster containers: %w", err)
		}
		return discovery.SelectActiveContainer(containers), nil
	}

	// Both are populated earlier in the flow, but only when CSI is enabled, so fetch them lazily.
	if r.wekaClient == nil {
		if err := r.GetWekaClient(ctx); err != nil {
			return nil, err
		}
	}

	// GetWekaClient returns a nil error but leaves wekaClient nil when the owner is not a WekaClient,
	// or when there are no owner refs at all. Blocking here is deliberate, and the opposite of what
	// resolveWaitSinceIoProcessesUpOverride does for the same predicate: falling back to a default
	// settle timeout is harmless, whereas skipping this check marks the image applied unverified.
	if r.wekaClient == nil {
		return nil, errors.New("weka client not found for client container")
	}

	if r.wekaClient.Spec.TargetCluster.Name == "" {
		return nil, nil
	}

	if r.targetCluster == nil {
		if err := r.FetchTargetCluster(ctx); err != nil {
			return nil, err
		}
	}

	containers, err := discovery.GetClusterContainers(ctx, r.Client, r.targetCluster, "")
	if err != nil {
		return nil, fmt.Errorf("error getting target cluster containers: %w", err)
	}

	return discovery.SelectActiveContainer(containers), nil
}

// verifyClusterContainerApplied asks the cluster - not the container's own `weka local status` -
// to confirm the container came up on the image we just rolled: ACTIVE/UP and on the target version.
// Without it a container whose pod is Running and whose local status is READY has its image marked
// applied even when weka still sees the old version, and the rolling upgrade moves on.
//
// Errors reading the cluster's view are retried, not waved through. It only skips where there is
// genuinely nothing to ask: the container has no cluster ID yet, there is no container to ask, or
// weka reports no version at all.
func (r *containerReconcilerLoop) verifyClusterContainerApplied(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "verifyClusterContainerApplied")
	defer logger.End()

	containerId := r.container.Status.ClusterContainerID
	if containerId == nil {
		// Legitimately nil on a first-ever join: reconcileClusterStatus, which populates it, runs
		// after this. Waiting here would deadlock - that step would never be reached.
		logger.Info("Cluster container ID is not known yet, skipping cluster-side image check")
		return nil
	}

	execIn, err := r.resolveClusterExecContainer(ctx)
	if err != nil {
		msg := fmt.Sprintf("could not resolve a container to read cluster container %d from: %v", *containerId, err)
		logger.Error(err, "Could not resolve exec container")
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "ClusterContainerCheckFailed", msg, time.Minute) //nolint:errcheck // error return value intentionally not checked
		return lifecycle.NewWaitErrorWithDuration(errors.New(msg), clusterContainerCheckRetry)
	}

	if execIn == nil {
		logger.Info("No container available to query cluster status, skipping cluster-side image check")
		return nil
	}

	timeout := clusterContainerCheckTimeout
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, execIn, &timeout)

	clusterContainer, err := wekaService.GetWekaContainer(ctx, *containerId)
	if err != nil {
		containerNotFound := &services.WekaContainerNotFound{}
		if errors.As(err, &containerNotFound) {
			msg := fmt.Sprintf("container %d is not known to the cluster", *containerId)
			_ = r.RecordEventThrottled(v1.EventTypeWarning, "ClusterContainerNotUp", msg, time.Minute) //nolint:errcheck // error return value intentionally not checked
			return lifecycle.NewWaitErrorWithDuration(errors.New(msg), clusterContainerCheckRetry)
		}

		msg := fmt.Sprintf("could not read cluster container %d: %v", *containerId, err)
		logger.Error(err, "Could not read cluster container info")
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "ClusterContainerCheckFailed", msg, time.Minute) //nolint:errcheck // error return value intentionally not checked
		return lifecycle.NewWaitErrorWithDuration(errors.New(msg), clusterContainerCheckRetry)
	}

	if clusterContainer.State != "ACTIVE" || clusterContainer.Status != "UP" {
		msg := fmt.Sprintf(
			"cluster reports container %d as state %s, status %s (expected ACTIVE/UP)",
			*containerId, clusterContainer.State, clusterContainer.Status,
		)
		logger.Info("Cluster container is not ACTIVE/UP yet", "state", clusterContainer.State, "status", clusterContainer.Status)
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "ClusterContainerNotUp", msg, time.Minute) //nolint:errcheck // error return value intentionally not checked
		return lifecycle.NewWaitErrorWithDuration(errors.New(msg), clusterContainerCheckRetry)
	}

	expected, reported := versionsToCompare(r.container.Spec.Image, clusterContainer)

	// Digest-pinned image, or a weka build that does not report its version: no information.
	if expected == "" || reported == "" {
		msg := fmt.Sprintf(
			"cannot compare versions for container %d (image %s, reported version %q)",
			*containerId, r.container.Spec.Image, reported,
		)
		logger.Info("Skipping cluster container version comparison", "image", r.container.Spec.Image, "reported_version", reported)
		// Normal, not Warning: a deliberate skip with nothing for an operator to act on.
		_ = r.RecordEventThrottled(v1.EventTypeNormal, "ClusterContainerCheckSkipped", msg, time.Minute) //nolint:errcheck // error return value intentionally not checked
		return nil
	}

	if reported != expected {
		msg := fmt.Sprintf(
			"cluster reports container %d running version %s, expected %s (image %s)",
			*containerId, reported, expected, r.container.Spec.Image,
		)
		logger.Info("Cluster container is not on the target version yet", "reported_version", reported, "expected_version", expected)
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "ClusterContainerVersionMismatch", msg, time.Minute) //nolint:errcheck // error return value intentionally not checked
		return lifecycle.NewWaitErrorWithDuration(errors.New(msg), clusterContainerCheckRetry)
	}

	logger.Debug("Cluster confirms container is up on the target version", "container_id", *containerId, "version", reported)

	return nil
}

// versionsToCompare returns the image-side and weka-side versions to compare. The image tag states
// the target version: in full it matches sw_release_string, with the build suffix stripped it
// matches sw_version - so the pair depends on whether weka reports a suffixed release string. An
// empty return means that side carries no version information.
//
// Compare the results with != and not utils.CompareVersions: that helper Atoi's each dot-separated
// component and ignores errors, so it reads "1.2.3.4-custom" and "1.2.3.4" as equal.
func versionsToCompare(image string, clusterContainer *services.WekaClusterContainer) (expected, reported string) {
	if clusterContainer.SwReleaseString != "" && clusterContainer.SwReleaseString != clusterContainer.SwVersion {
		return utils.GetImageTag(image), clusterContainer.ReportedVersion()
	}
	return utils.GetSoftwareVersion(image), clusterContainer.ReportedVersion()
}
