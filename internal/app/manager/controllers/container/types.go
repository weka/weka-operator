package container

import (
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ContainerState struct {
	lifecycle.ReconciliationState[*wekav1alpha1.WekaContainer]
	Logger            *instrumentation.SpanLogger
	ExecService       services.ExecService
	Client            client.Client
	CrdManager        services.CrdManager
	ContainerServices map[*wekav1alpha1.WekaContainer]services.WekaContainerService

	Pod *v1.Pod
}

type ContainerUpdateError struct {
	errors.WrappedError
	Container *wekav1alpha1.WekaContainer
}

type ConditionUpdateError struct {
	errors.WrappedError
	Container *wekav1alpha1.WekaContainer
	Condition string
}

// ------------------------------

func (state *ContainerState) NewContainerService() services.WekaContainerService {
	return services.NewWekaContainerService(state.Client, state.CrdManager, state.ExecService, state.Subject)
}

func (state *ContainerState) GetWekaContainerService() services.WekaContainerService {
	if state.ContainerServices == nil {
		state.ContainerServices = make(map[*wekav1alpha1.WekaContainer]services.WekaContainerService)
	}
	containerService, ok := state.ContainerServices[state.Subject]
	if !ok {
		containerService := services.NewWekaContainerService(state.Client, state.CrdManager, state.ExecService, state.Subject)
		state.ContainerServices[state.Subject] = containerService
		return containerService
	}

	return containerService
}

func (state *ContainerState) NewWekaService() services.WekaService {
	execService := state.ExecService
	subject := state.Subject
	return services.NewWekaService(execService, subject)
}
