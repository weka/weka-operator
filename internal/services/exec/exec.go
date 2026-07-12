package exec

import (
	"context"
	"time"

	"github.com/pkg/errors"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"github.com/weka/weka-operator/pkg/util/podexec"
)

type ExecService interface {
	GetExecutor(ctx context.Context, container *wekav1alpha1.WekaContainer) (podexec.Exec, error)
	GetExecutorWithTimeout(ctx context.Context, container *wekav1alpha1.WekaContainer, timeout *time.Duration) (podexec.Exec, error)
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

func (s *PodExecService) GetExecutorWithTimeout(ctx context.Context, container *wekav1alpha1.WekaContainer, timeout *time.Duration) (podexec.Exec, error) {
	if container == nil {
		return nil, errors.New("container is nil")
	}
	config := s.getConfig()
	nodeName := string(container.GetNodeAffinity())

	executor, err := podexec.NewExecWithConfig(s.restClient, config, types.NamespacedName{
		Namespace: container.Namespace,
		Name:      container.Name,
	}, timeout, "weka-container", nodeName)
	if err != nil {
		return nil, errors.Wrap(err, "Could not create executor")
	}
	return executor, nil
}

func (s *PodExecService) GetExecutor(ctx context.Context, container *wekav1alpha1.WekaContainer) (podexec.Exec, error) {
	return s.GetExecutorWithTimeout(ctx, container, nil)
}

func (s *PodExecService) getConfig() *rest.Config {
	return s.config
}
