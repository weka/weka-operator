package podexec

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"
	"k8s.io/utils/exec"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
)

type Exec interface {
	Exec(ctx context.Context, command []string) (stdout, stderr bytes.Buffer, err error)
	ExecNamed(ctx context.Context, name string, command []string) (stdout, stderr bytes.Buffer, err error)
	ExecSensitive(ctx context.Context, name string, command []string) (stdout, stderr bytes.Buffer, err error)
}

type PodExec struct {
	RestClient    rest.Interface
	RestConfig    *rest.Config
	Pod           types.NamespacedName
	timeout       *time.Duration
	ContainerName string
	// node name is provided for debugging purposes only, it is not used for anything else
	NodeName string
}

func NewExecWithConfig(restClient rest.Interface, cfg *rest.Config, pod types.NamespacedName, timeout *time.Duration, containerName, nodeName string) (Exec, error) {
	if timeout == nil {
		defaultTimeout := config.Config.Timeouts.KubeExecTimeout
		timeout = &defaultTimeout
	}

	return &PodExec{
		RestClient:    restClient,
		Pod:           pod,
		ContainerName: containerName,
		RestConfig:    cfg,
		timeout:       timeout,
		NodeName:      nodeName,
	}, nil
}

func NewExecInPod(restClient rest.Interface, cfg *rest.Config, pod *v1.Pod) (Exec, error) {
	return NewExecInPodWithTimeout(restClient, cfg, pod, nil)
}

func NewExecInPodWithTimeout(restClient rest.Interface, cfg *rest.Config, pod *v1.Pod, timeout *time.Duration) (Exec, error) {
	namespacedObject := types.NamespacedName{
		Namespace: pod.Namespace,
		Name:      pod.Name,
	}
	return NewExecWithConfig(restClient, cfg, namespacedObject, timeout, consts.WekaContainerName, pod.Spec.NodeName)
}

func NewExecInPodByName(restClient rest.Interface, cfg *rest.Config, pod *v1.Pod, containerName string, timeout *time.Duration) (Exec, error) {
	namespacedObject := types.NamespacedName{
		Namespace: pod.Namespace,
		Name:      pod.Name,
	}

	return NewExecWithConfig(restClient, cfg, namespacedObject, timeout, containerName, pod.Spec.NodeName)
}

func (e *PodExec) exec(ctx context.Context, name string, sensitive bool, command []string) (stdout, stderr bytes.Buffer, err error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "Exec", "command_name", name)
	defer logger.End()

	ctx, cancel := context.WithTimeout(ctx, *e.timeout)
	defer cancel()

	// TODO: hide sensitive data
	logger.SetValues(
		"pod", e.Pod.Name,
		"node", e.NodeName,
	)

	if !sensitive {
		logger.SetValues("command", strings.Join(command, " "))
	}

	podExec := e.RestClient.Post().
		Resource("pods").
		Name(e.Pod.Name).
		Namespace(e.Pod.Namespace).
		SubResource("exec").
		VersionedParams(&v1.PodExecOptions{
			Container: e.ContainerName,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(e.RestConfig, "POST", podExec.URL())
	if err != nil {
		logger.SetError(err, "Exec failed to create executor")
		return stdout, stderr, errors.Wrap(err, "Exec failed to create executor")
	}

	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
		Tty:    false,
	})
	if err != nil {
		var exitError exec.ExitError
		if errors.As(err, &exitError) {
			exitCode := exitError.ExitStatus() // ExitStatus() returns the exit code
			logger.SetValues("exit_code", exitCode)
			logger.SetStatus(codes.Ok, "Execution succeeded with remote error")
			if !sensitive {
				logger.SetValues("stdout", stdout.String(), "stderr", stderr.String())
			}
			return stdout, stderr, errors.Wrap(err, fmt.Sprintf("command %s failed", name))
		}
		logger.SetError(err, "Exec failed to stream")
		return stdout, stderr, errors.Wrap(err, "Exec failed to stream")
	}
	logger.SetValues("exit_code", 0)
	logger.SetStatus(codes.Ok, "Exec success")
	logger.AddEvent("Execution completed")
	return stdout, stderr, err
}

// Exec executes a command in a pod. Logs input and output if exit code != 0. Should be used in rare cases as might reveal sensitive data.
func (e *PodExec) Exec(ctx context.Context, command []string) (stdout, stderr bytes.Buffer, err error) {
	return e.exec(ctx, "", false, command)
}

// ExecNamed executes a command in a pod. Logs input and output if exit code != 0. However, provides a name for the span.
func (e *PodExec) ExecNamed(ctx context.Context, name string, command []string) (stdout, stderr bytes.Buffer, err error) {
	return e.exec(ctx, fmt.Sprintf("Exec.%s", name), false, command)
}

func (e *PodExec) ExecSensitive(ctx context.Context, name string, command []string) (stdout, stderr bytes.Buffer, err error) {
	return e.exec(ctx, fmt.Sprintf("Exec.%s", name), true, command)
}
