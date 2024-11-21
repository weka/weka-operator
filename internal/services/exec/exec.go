package exec

import (
	"context"
	"time"

	"github.com/pkg/errors"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	util2 "github.com/weka/weka-operator/pkg/util"
	"k8s.io/client-go/rest"
)

type ExecService interface {
	GetExecutor(ctx context.Context, container *wekav1alpha1.WekaContainer) (util2.Exec, error)
	GetExecutorWithTimeout(ctx context.Context, container *wekav1alpha1.WekaContainer, timeout *time.Duration) (util2.Exec, error)
}

func NewExecService(config *rest.Config) ExecService {
	return &PodExecService{
		config: config,
	}
}

type PodExecService struct {
	config *rest.Config
}

func (s *PodExecService) GetExecutorWithTimeout(ctx context.Context, container *wekav1alpha1.WekaContainer, timeout *time.Duration) (util2.Exec, error) {
	config := s.getConfig()
	executor, err := util2.NewExecWithConfig(config, util2.NamespacedObject{
		Namespace: container.ObjectMeta.Namespace,
		Name:      container.ObjectMeta.Name,
	}, timeout)
	if err != nil {
		return nil, errors.Wrap(err, "Could not create executor")
	}
	return executor, nil
}

func (s *PodExecService) GetExecutor(ctx context.Context, container *wekav1alpha1.WekaContainer) (util2.Exec, error) {
	config := s.getConfig()
	executor, err := util2.NewExecWithConfig(config, util2.NamespacedObject{
		Namespace: container.ObjectMeta.Namespace,
		Name:      container.ObjectMeta.Name,
	}, nil)
	if err != nil {
		return nil, errors.Wrap(err, "Could not create executor")
	}
	return executor, nil
}

func (s *PodExecService) getConfig() *rest.Config {
	return s.config
}
