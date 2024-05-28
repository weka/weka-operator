package lifecycle

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/services"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

type DefaultFsCreatedError struct {
	errors.WrappedError
}

func (e DefaultFsCreatedError) Error() string {
	return "error creating default filesystem: " + e.WrappedError.Error()
}

func (state *ClusterState) DefaultFsCreated(wekaClusterService services.WekaClusterService) StepFunc {
	return func(ctx context.Context) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "DefaultFsCreated")
		defer end()

		if state.Containers == nil {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is nil"}
		}
		containers := state.Containers
		if len(containers) == 0 {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is empty"}
		}

		logger.SetPhase("CONFIGURING_DEFAULT_FS")
		if err := wekaClusterService.EnsureDefaultFs(ctx, containers[0]); err != nil {
			return &DefaultFsCreatedError{
				WrappedError: errors.WrappedError{Err: err},
			}
		}

		return nil
	}
}
