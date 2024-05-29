package container

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (state *ContainerState) EnsureFinalizer(client client.Client, finalizer string) lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureFinalizer")
		defer end()

		container := state.Subject
		if container == nil {
			return &lifecycle.StateError{Property: "Subject", Message: "Subject is nil"}
		}

		if ok := controllerutil.AddFinalizer(container, finalizer); !ok {
			return nil
		}

		logger.Info("Adding Finalizer for weka container")
		err := client.Update(ctx, container)
		if err != nil {
			return &ContainerUpdateError{
				WrappedError: errors.WrappedError{Err: err},
				Name:         container.Name,
			}
		}
		return nil
	}
}
