package services

import (
	"context"
	"github.com/weka/weka-operator/internal/app/manager/domain"

	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/util"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type ExecService interface {
	GetExecutor(ctx context.Context, container *wekav1alpha1.WekaContainer) (util.Exec, error)
}

func NewExecServiceFromManager(manager manager.Manager) ExecService {
	return &PodExecService{
		config: manager.GetConfig(),
	}
}

func NewExecService(config *rest.Config) ExecService {
	return &PodExecService{
		config: config,
	}
}

type PodExecService struct {
	config *rest.Config
}

func (s *PodExecService) GetExecutor(ctx context.Context, container *wekav1alpha1.WekaContainer) (util.Exec, error) {
	compatibilityConfig := domain.CompatibilityConfig{}
	pod, err := resources.NewContainerFactory(container).Create(ctx, compatibilityConfig)
	if err != nil {
		return nil, errors.Wrap(err, "Could not find executor pod")
	}
	config := s.getConfig()
	executor, err := util.NewExecWithConfig(config, pod)
	if err != nil {
		return nil, errors.Wrap(err, "Could not create executor")
	}
	return executor, nil
}

func (s *PodExecService) getConfig() *rest.Config {
	return s.config
}
