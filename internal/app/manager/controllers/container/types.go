package container

import (
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	v1 "k8s.io/api/core/v1"
)

type ContainerState struct {
	lifecycle.ReconciliationState[*wekav1alpha1.WekaContainer]
	Pod *v1.Pod
}

type ContainerUpdateError struct {
	errors.WrappedError
	Container *wekav1alpha1.WekaContainer
}
