package container

import (
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
)

type ContainerState struct {
	lifecycle.ReconciliationState[*wekav1alpha1.WekaContainer]
}

type ContainerUpdateError struct {
	errors.WrappedError
	Container *wekav1alpha1.WekaContainer
}
