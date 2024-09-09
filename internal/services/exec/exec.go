package exec

import (
	"bytes"
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
	return &nullExec{}, nil
}

var _ util2.Exec = (*nullExec)(nil)

type nullExec struct{}

func (e *nullExec) Exec(ctx context.Context, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	return bytes.Buffer{}, bytes.Buffer{}, nil
}

func (e *nullExec) ExecNamed(ctx context.Context, name string, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	resultFn, ok := cmdStubs[name]
	if !ok {
		return bytes.Buffer{}, bytes.Buffer{}, nil // Default to no-op
	}
	result := resultFn()
	return *bytes.NewBufferString(result), bytes.Buffer{}, nil
}

func (e *nullExec) ExecSensitive(ctx context.Context, name string, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	resultFn, ok := cmdStubs[name]
	if !ok {
		return bytes.Buffer{}, bytes.Buffer{}, nil // Default to no-op
	}
	result := resultFn()
	return *bytes.NewBufferString(result), bytes.Buffer{}, nil
}

var cmdStubs = map[string]func() string{
	"AddClusterUser": func() string {
		return ""
	},
	"GenerateJoinSecret": func() string {
		return `"abc123"`
	},
	"GetWekaStatus": func() string {
		return `{
			"status": "READY",
			"capacity": {
				"unprovisioned_bytes": 1000000000000,
				"total_bytes": 10000000000000
			}
		}`
	},
	"WekaListUsers": func() string {
		return "[]"
	},
}
