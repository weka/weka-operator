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

	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/services"
)

func (r *containerReconcilerLoop) reconcileWekaLocalStatus(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	node := r.node

	if node != nil && !NodeIsReady(node) {
		err := fmt.Errorf("node %s is not ready", node.Name)

		_ = r.RecordEventThrottled(v1.EventTypeWarning, "NodeNotReady", err.Error(), time.Minute) //nolint:errcheck // error return value intentionally not checked

		logger.Info("Skipping weka local status reconciliation on NotReady node", "node", node.Name)

		return nil
	}

	container := r.container

	wekaLocalPsTimeout := 10 * time.Second
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &wekaLocalPsTimeout)

	localContainers, err := wekaService.ListLocalContainers(ctx)
	if err != nil {
		logger.Error(err, "Error getting weka local ps")
		// TODO: Validate agent-specific errors, but should not be very important

		// check if drivers should be force-reloaded
		loaded, driversErr := r.driversLoaded(ctx)
		if driversErr != nil {
			return driversErr
		}

		if !loaded {
			err = r.updateStatusWaitForDrivers(ctx)
			if err != nil {
				return err
			}

			details := r.container.ToOwnerDetails()
			driversLoader := operations.NewLoadDrivers(r.Manager, r.node, *details, r.container.Spec.DriversLoaderImage,
				r.container.Spec.DriversBuildId, r.container.Spec.DriversDistService, r.container.HasFrontend(), true)
			loaderErr := operations.ExecuteOperation(ctx, driversLoader)
			if loaderErr != nil {
				driversLoadErr := fmt.Errorf("drivers are not loaded: %v; %v", driversErr, loaderErr)
				return lifecycle.NewWaitError(driversLoadErr)
			}

			err = fmt.Errorf("weka local ps failed: %v", err)
			return lifecycle.NewWaitError(err)
		}
		return lifecycle.NewWaitError(err)
	}

	err = r.checkContainerNotFound(localContainers, err)
	if err != nil {
		if statusErr := r.updateContainerStatusIfNotEquals(ctx, weka.Unhealthy); statusErr != nil {
			return statusErr
		}
		return lifecycle.NewWaitErrorWithDuration(err, 15*time.Second)
	}

	var localContainer services.WekaLocalContainer
	for i := range localContainers {
		if localContainers[i].Name == container.Spec.WekaContainerName {
			localContainer = localContainers[i]
			break
		}
	}
	if localContainer.Name == "" {
		localContainer = localContainers[0]
	}
	status := localContainer.RunStatus

	// check local container status and propagate failure message (if any) as event
	internalStatus := localContainer.InternalStatus.DisplayStatus
	if internalStatus != "READY" && localContainer.LastFailure != "" && !container.IsDistMode() {
		msg := fmt.Sprintf(
			"Container is not ready, status: %s, last failure: %s (%s)",
			internalStatus, localContainer.LastFailure, localContainer.LastFailureTime,
		)
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "WekaLocalStatus", msg, time.Minute) //nolint:errcheck // error return value intentionally not checked
	}

	// skip status update for DrivesAdding (if still adding drives)
	if status == string(weka.Running) && container.Status.Status == weka.DrivesAdding && r.HasDrivesToAdd() {
		// update internal status if it changed (this part is for backward compatibility)
		if container.Status.InternalStatus != internalStatus {
			return fmt.Errorf("internal status changed: %s -> %s", container.Status.InternalStatus, internalStatus)
		}
		return nil
	}

	containerStatus := weka.ContainerStatus(status)
	if (container.Status.Status != containerStatus && r.IsStatusOverwritableByLocal()) || container.Status.InternalStatus != internalStatus {
		logger.Debug("Updating status", "old_status", container.Status.Status, "new_status", containerStatus, "old_internal_status", container.Status.InternalStatus, "new_internal_status", internalStatus)
		if r.IsStatusOverwritableByLocal() {
			container.Status.Status = containerStatus
		}
		container.Status.InternalStatus = internalStatus
		if updateErr := r.Status().Update(ctx, container); updateErr != nil {
			return updateErr
		}
		logger.WithValues("status", status, "internal_status", internalStatus).Info("Status updated")
		return nil
	}

	return nil
}

func (r *containerReconcilerLoop) checkContainerNotFound(localPsResponse []services.WekaLocalContainer, psErr error) error {
	container := r.container

	if len(localPsResponse) == 0 {
		return errors.New("weka local ps response is empty")
	}

	found := false
	for i := range localPsResponse {
		switch {
		case localPsResponse[i].Name == container.Spec.WekaContainerName:
			found = true
		case localPsResponse[i].Name == "envoy" && container.IsEnvoy():
			found = true
		case localPsResponse[i].Name == "telemetry" && container.IsTelemetry():
			found = true
		}
		if found {
			break
		}
	}

	if !found {
		return errors.New("weka container not found")
	}

	return nil
}
