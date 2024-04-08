package util

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"
	"k8s.io/utils/exec"
)

const DevModeNamespace = "weka-operator-system"

type Exec interface {
	Exec(ctx context.Context, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error)
	ExecNamed(ctx context.Context, name string, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error)
	ExecSensitive(ctx context.Context, name string, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error)
	GetNodeName() string
}

type PodExec struct {
	ClientSet *kubernetes.Clientset
	Pod       *v1.Pod
	Config    *rest.Config
}

func (e *PodExec) NodeName() string {
	return e.Pod.Spec.NodeName
}

type ConfigurationError struct {
	Err     error
	Message string
}

func (e *ConfigurationError) Error() string {
	return fmt.Sprintf("configuration error: %s, %v", e.Message, e.Err)
}

func NewExecWithConfig(config *rest.Config, pod *v1.Pod) (Exec, error) {
	clientset, err := KubernetesClientSet(config)
	if err != nil {
		return nil, &ConfigurationError{err, "failed to get Kubernetes clientset"}
	}

	return &PodExec{
		ClientSet: clientset,
		Pod:       pod,
		Config:    config,
	}, nil
}

func NewExecInPod(pod *v1.Pod) (Exec, error) {
	config, err := KubernetesConfiguration()
	if err != nil {
		return nil, &ConfigurationError{err, "failed to get Kubernetes configuration"}
	}

	return NewExecWithConfig(config, pod)
}

func (e *PodExec) exec(ctx context.Context, name string, sensitive bool, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "Exec", "command_name", name)
	defer end()
	// TODO: hide sensitive data
	logger.SetValues(
		"pod", e.Pod.Name,
	)

	if !sensitive {
		logger.SetValues("command", strings.Join(command, " "))
	}

	podExec := e.ClientSet.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(e.Pod.Name).
		Namespace(e.Pod.Namespace).
		SubResource("exec").
		VersionedParams(&v1.PodExecOptions{
			Container: "weka-container",
			Command:   command,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(e.Config, "POST", podExec.URL())
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
			return stdout, stderr, err
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
func (e *PodExec) Exec(ctx context.Context, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	return e.exec(ctx, "", false, command)
}

// ExecNamed executes a command in a pod. Logs input and output if exit code != 0. However, provides a name for the span.
func (e *PodExec) ExecNamed(ctx context.Context, name string, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	return e.exec(ctx, fmt.Sprintf("Exec.%s", name), false, command)
}

func (e *PodExec) ExecSensitive(ctx context.Context, name string, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	return e.exec(ctx, fmt.Sprintf("Exec.%s", name), true, command)
}

func (e *PodExec) GetNodeName() string {
	return e.Pod.Spec.NodeName
}

func KubernetesClientSet(config *rest.Config) (*kubernetes.Clientset, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get clientset")
	}

	return clientset, nil
}

func KubernetesConfiguration() (*rest.Config, error) {
	if os.Getenv("UNIT_TEST") == "true" {
		return &rest.Config{}, nil
	}
	kubeConfigPath := os.Getenv("KUBECONFIG")
	if kubeConfigPath == "" {
		return rest.InClusterConfig()
	} else {
		return clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	}
}

func GetPodNamespace() (string, error) {
	namespace, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		if os.IsNotExist(err) && os.Getenv("OPERATOR_DEV_MODE") == "true" {
			return DevModeNamespace, nil
		}
		return "", err
	}
	return string(namespace), nil
}

func GetLastGuidPart(uid types.UID) string {
	guidLastPart := string(uid[strings.LastIndex(string(uid), "-")+1:])
	return guidLastPart
}

func IsEqualConfigMapData(cm1, cm2 *v1.ConfigMap) bool {
	return reflect.DeepEqual(cm1.Data, cm2.Data)
}
