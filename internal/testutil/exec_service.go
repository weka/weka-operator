package testutil

import (
	"bytes"
	"context"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/pkg/util"
)

func NewTestingExecService() *TestingExecService {
	return &TestingExecService{}
}

type TestingExecService struct{}

func (t *TestingExecService) GetExecutor(ctx context.Context, container *wekav1alpha1.WekaContainer) (util.Exec, error) {
	return &TestingExec{}, nil
}

type TestingExec struct{}

func (t *TestingExec) Exec(ctx context.Context, command []string) (bytes.Buffer, bytes.Buffer, error) {
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}

	return stdout, stderr, nil
}

func (t *TestingExec) ExecNamed(ctx context.Context, name string, command []string) (bytes.Buffer, bytes.Buffer, error) {
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}

	return stdout, stderr, nil
}

func (t *TestingExec) ExecSensitive(ctx context.Context, name string, command []string) (bytes.Buffer, bytes.Buffer, error) {
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}

	return stdout, stderr, nil
}
