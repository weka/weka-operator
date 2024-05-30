package container

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
)

type EnsureDriversLoaderError struct {
	errors.WrappedError
	Container *wekav1alpha1.WekaContainer
}

func (state *ContainerState) EnsureDriversLoader(containerServer services.WekaContainerService) lifecycle.StepFunc {
	return func(ctx context.Context) error {
		container := state.Subject
		if container == nil {
			return &errors.ArgumentError{ArgName: "container", Message: "container is nil"}
		}

		if container.Spec.DriversDistService == "" {
			return nil
		}

		err := containerServer.EnsureDriversLoader(ctx)
		if err != nil {
			return &EnsureDriversLoaderError{
				WrappedError: errors.WrappedError{Err: err},
				Container:    container,
			}
		}

		return nil
	}
}
