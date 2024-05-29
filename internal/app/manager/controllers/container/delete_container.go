package container

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type TombstoneCreationError struct {
	errors.WrappedError
	Container *wekav1alpha1.WekaContainer
}

func (state *ContainerState) DeleteContainer(client client.Client, crdManager services.CrdManager, finalizer string) lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "DeleteContainer")
		defer end()

		container := state.Subject
		if container == nil {
			return &lifecycle.StateError{Property: "Subject", Message: "Subject is nil"}
		}

		if container.GetDeletionTimestamp() != nil {
			logger.Info("Container is being deleted", "name", container.Name)
			logger.SetPhase("DELETING")
			err := crdManager.EnsureTombstone(ctx, container)
			if err != nil {
				logger.Error(err, "Error ensuring tombstone")
				return &TombstoneCreationError{
					WrappedError: errors.WrappedError{Err: err},
					Container:    container,
				}
			}
			// remove finalizer
			controllerutil.RemoveFinalizer(container, finalizer)
			if err := client.Update(ctx, container); err != nil {
				return &ContainerUpdateError{
					WrappedError: errors.WrappedError{Err: err},
					Container:    container,
				}
			}
			return nil
		}

		return nil
	}
}
