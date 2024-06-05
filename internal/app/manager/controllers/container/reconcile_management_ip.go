package container

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	werrors "github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

type ReconcileManagementIPError struct {
	werrors.WrappedError
	Container *wekav1alpha1.WekaContainer
}

func (state *ContainerState) ReconcileManagementIP(containerService services.WekaContainerService) lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, _, end := instrumentation.GetLogSpan(ctx, "ReconcileManagementIP")
		defer end()

		container := state.Subject
		if container == nil {
			return &lifecycle.StateError{
				Property: "Subject",
				Message:  "container is nil",
			}
		}

		if err := containerService.ReconcileManagementIP(ctx); err != nil {
			return &ReconcileManagementIPError{
				WrappedError: werrors.WrappedError{
					Err:  err,
					Span: instrumentation.GetLogName(ctx),
				},
				Container: state.Subject,
			}
		}

		return nil
	}
}
