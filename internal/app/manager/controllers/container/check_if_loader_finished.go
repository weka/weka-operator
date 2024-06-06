package container

import (
	"context"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"go.opentelemetry.io/otel/attribute"
)

func (state *ContainerState) DriverLoaderFinished() lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "DriverLoaderFinished")
		defer end()

		container := state.Subject
		logger.SetAttributes(
			attribute.String("container", container.Name),
		)

		if container == nil {
			return &lifecycle.StateError{
				Property: "Subject",
				Message:  "container is nil",
			}
		}

		containerService := state.NewContainerService()
		err := containerService.CheckIfLoaderFinished(ctx)
		if err != nil {
			return &errors.RetryableError{
				Err:        err,
				RetryAfter: 3 * time.Second,
			}
		} else {
			// if drivers loaded we can delete this weka container
			client := state.Client
			err := client.Delete(ctx, container)
			if err != nil {
				return err
			}
			logger.SetPhase("DELETING_DRIVER_LOADER")
		}
		logger.SetPhase("DRIVERS_LOADED")

		return nil
	}
}
