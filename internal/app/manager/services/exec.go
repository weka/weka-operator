package services

import (
	"context"

	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/util"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type ExecService interface {
	GetExecutor(ctx context.Context, container *wekav1alpha1.WekaContainer) (*util.Exec, error)
}

func NewExecService(manager manager.Manager) ExecService {
	return &PodExecService{
		Manager: manager,
	}
}

type PodExecService struct {
	Manager manager.Manager
}

func (s *PodExecService) GetExecutor(ctx context.Context, container *wekav1alpha1.WekaContainer) (*util.Exec, error) {
	pod, err := resources.NewContainerFactory(container).Create(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "Could not find executor pod")
	}
	executor, err := util.NewExecWithConfig(s.Manager.GetConfig(), pod)
	if err != nil {
		return nil, errors.Wrap(err, "Could not create executor")
	}
	return executor, nil
}
