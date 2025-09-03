package exec

import (
	"context"
	"time"

	"github.com/pkg/errors"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	util2 "github.com/weka/weka-operator/pkg/util"
)

type ExecService interface {
	GetExecutor(ctx context.Context, container *wekav1alpha1.WekaContainer) (util2.Exec, error)
	GetExecutorWithTimeout(ctx context.Context, container *wekav1alpha1.WekaContainer, timeout *time.Duration) (util2.Exec, error)
}

func NewExecService(client rest.Interface, config *rest.Config) ExecService {
	return &PodExecService{
		config:     config,
		restClient: client,
	}
}

type PodExecService struct {
	config     *rest.Config
	restClient rest.Interface
}

func (s *PodExecService) GetExecutorWithTimeout(ctx context.Context, container *wekav1alpha1.WekaContainer, timeout *time.Duration) (util2.Exec, error) {
	config := s.getConfig()
	nodeName := string(container.GetNodeAffinity())

	executor, err := util2.NewExecWithConfig(s.restClient, config, types.NamespacedName{
		Namespace: container.ObjectMeta.Namespace,
		Name:      container.ObjectMeta.Name,
	}, timeout, "weka-container", nodeName)
	if err != nil {
		return nil, errors.Wrap(err, "Could not create executor")
	}
	return executor, nil
}

func (s *PodExecService) GetExecutor(ctx context.Context, container *wekav1alpha1.WekaContainer) (util2.Exec, error) {
	return s.GetExecutorWithTimeout(ctx, container, nil)
}

func (s *PodExecService) getConfig() *rest.Config {
	return s.config
}
