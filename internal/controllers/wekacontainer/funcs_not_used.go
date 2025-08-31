// This file contains functions that are currently not used and can be removed completely if not needed for a while
package wekacontainer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/go-lib/pkg/workers"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

func (r *containerReconcilerLoop) DeactivateDrives(ctx context.Context) error {
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

	wekaService := services.NewWekaService(r.ExecService, executeInContainer)
	statusActive := "ACTIVE"
	statusInactive := "INACTIVE"

	drives, err := wekaService.ListContainerDrives(ctx, *containerId)
	if err != nil {
		return err
	}

	return workers.ProcessConcurrently(ctx, drives, 5, func(ctx context.Context, drive services.Drive) error {
		switch drive.Status {
		case statusActive:
			logger.Info("Deactivating drive", "drive_id", drive.Uuid)
			return wekaService.DeactivateDrive(ctx, drive.Uuid)
		case statusInactive:
			return nil
		default:
			return fmt.Errorf("drive %s has status '%s', wait for it to become 'INACTIVE'", drive.SerialNumber, drive.Status)
		}
	}).AsError()
}

func (r *containerReconcilerLoop) s3ContainerPreDeactivate(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "s3ContainerPreDeactivate")
	defer end()

	executeInContainer := r.container

	nodeReady := NodeIsReady(r.node)
	podAvailable := r.pod != nil && r.pod.Status.Phase == v1.PodRunning

	wekaService := services.NewWekaService(r.ExecService, executeInContainer)

	// TODO: temporary check caused by weka s3 container remove behavior
	if podAvailable && nodeReady {
		// check that local s3 container does not exist anymore
		// if it does, wait for it to be removed
		localContainers, err := wekaService.ListLocalContainers(ctx)
		if err != nil {
			if strings.Contains(err.Error(), "weka-agent isn't running") {
				logger.Info("weka-agent isn't running, skipping local containers check")
				return nil
			}

			err = errors.Wrap(err, "Failed to list weka local containers")
			return err
		}

		logger.Debug("weka local ps", "containers", localContainers)

		for _, localContainer := range localContainers {
			if localContainer.Type == "s3" {
				err := errors.New("local s3 container still exists")
				return err
			}
		}
	} else {
		err := errors.New("cannot check local s3 container - node is not ready or pod is not available")
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	return nil
}

func (r *containerReconcilerLoop) isExistingCluster(ctx context.Context, guid string) (bool, error) {
	// TODO: Query by status?
	// TODO: Cache?
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "isExistingCluster")
	defer end()

	logger.WithValues("cluster_guid", guid).Info("Verifying for existing cluster")

	clusters, err := discovery.GetAllClusters(ctx, r.Client)
	if err != nil {
		return false, err
	}
	for _, cluster := range clusters {
		// strip `-` from saved cluster name
		stripped := strings.ReplaceAll(cluster.Status.ClusterID, "-", "")
		if stripped == guid {
			logger.InfoWithStatus(codes.Ok, "Cluster found")
			return true, nil
		}
	}
	logger.InfoWithStatus(codes.Ok, "Cluster not found")
	return false, nil
}
