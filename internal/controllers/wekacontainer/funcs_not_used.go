// This file contains functions that are currently not used and can be removed completely if not needed for a while
package wekacontainer

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-lib/pkg/workers"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

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

	return workers.ProcessConcurrently(ctx, drives, 5, func(ctx context.Context, drive weka.Drive) error {
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


