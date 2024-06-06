package container

import (
	e "errors"
	"testing"

	werror "github.com/weka/weka-operator/internal/pkg/errors"
)

func TestContainerRefreshError(t *testing.T) {
	t.Run("Container Not Found", ContainerNotFound)
}

func ContainerNotFound(t *testing.T) {
	err := &ContainerRefreshError{
		WrappedError: werror.WrappedError{Err: &werror.NotFoundError{}},
	}

	var notFoundError *werror.NotFoundError
	if !e.As(err, &notFoundError) {
		t.Errorf("Error is not of type NotFoundError")
	}
}
