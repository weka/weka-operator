package exec

import (
	"context"

	"github.com/pkg/errors"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	util2 "github.com/weka/weka-operator/pkg/util"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type ExecService interface {
	GetExecutor(ctx context.Context, container *wekav1alpha1.WekaContainer) (util2.Exec, error)
}

func NewExecServiceFromManager(manager manager.Manager) ExecService {
	return &PodExecService{
		config: manager.GetConfig(),
	}
}

var NewExecService = newExecService

func newExecService(config *rest.Config) ExecService {
	return &PodExecService{
		config: config,
	}
}

func NullExecService(config *rest.Config) ExecService {
	return &nullExecService{}
}

type PodExecService struct {
	config *rest.Config
}

func (s *PodExecService) GetExecutor(ctx context.Context, container *wekav1alpha1.WekaContainer) (util2.Exec, error) {
	config := s.getConfig()
	executor, err := util2.NewExecWithConfig(config, util2.NamespacedObject{
		Namespace: container.ObjectMeta.Namespace,
		Name:      container.ObjectMeta.Name,
	})
	if err != nil {
		return nil, errors.Wrap(err, "Could not create executor")
	}
	return executor, nil
}

func (s *PodExecService) getConfig() *rest.Config {
	return s.config
}

// NullExecService is an ExecService that does nothing.
type nullExecService struct{}

func (s *nullExecService) GetExecutor(ctx context.Context, container *wekav1alpha1.WekaContainer) (util2.Exec, error) {
	return util2.NewNullExec(), nil
}
