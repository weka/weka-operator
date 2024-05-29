package container

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/services"
	"github.com/weka/weka-operator/internal/pkg/errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
)

type ContainerRefreshError struct {
	errors.WrappedError
	Name string
}

func (e *ContainerRefreshError) Error() string {
	return fmt.Sprintf("Error refreshing container: %s", e.WrappedError.Error())
}

type ContainerNotFoundError struct {
	errors.WrappedError
	Name string
}

func (e *ContainerNotFoundError) Error() string {
	return fmt.Sprintf("Container not found: %s", e.WrappedError.Error())
}

func (state *ContainerState) RefreshContainer(crdManager services.CrdManager) lifecycle.StepFunc {
	return func(ctx context.Context) error {
		req := state.Request

		if req == (ctrl.Request{}) {
			return &errors.ArgumentError{ArgName: "Request", Message: "request is empty"}
		}

		container, err := crdManager.RefreshContainer(ctx, req)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return &ContainerNotFoundError{
					WrappedError: errors.WrappedError{Err: err},
					Name:         req.Name,
				}
			}
			return &ContainerRefreshError{
				WrappedError: errors.WrappedError{Err: err},
				Name:         req.Name,
			}
		}

		state.Subject = container
		state.Conditions = &container.Status.Conditions
		return nil
	}
}
