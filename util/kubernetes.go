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
	"go.opentelemetry.io/otel/attribute"
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

type Exec struct {
	ClientSet *kubernetes.Clientset
	Pod       *v1.Pod
	Config    *rest.Config
}

type ConfigurationError struct {
	Err     error
	Message string
}

func (e *ConfigurationError) Error() string {
	return fmt.Sprintf("configuration error: %s, %v", e.Message, e.Err)
}

func NewExecWithConfig(config *rest.Config, pod *v1.Pod) (*Exec, error) {
	clientset, err := KubernetesClientSet(config)
	if err != nil {
		return nil, &ConfigurationError{err, "failed to get Kubernetes clientset"}
	}

	return &Exec{
		ClientSet: clientset,
		Pod:       pod,
		Config:    config,
	}, nil
}

func NewExecInPod(pod *v1.Pod) (*Exec, error) {
	config, err := KubernetesConfiguration()
	if err != nil {
		return nil, &ConfigurationError{err, "failed to get Kubernetes configuration"}
	}

	return NewExecWithConfig(config, pod)
}

func (e *Exec) exec(ctx context.Context, name string, sensitive bool, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	ctx, span := instrumentation.Tracer.Start(ctx, "Exec")
	defer span.End()
	//TODO: hide sensitive data
	span.SetAttributes(
		attribute.String("pod", e.Pod.Name),
		attribute.String("namespace", e.Pod.Namespace),
	)
	if name != "" {
		span.SetName(name)
	}

	if !sensitive {
		span.SetAttributes(attribute.String("command", strings.Join(command, " ")))
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
		span.RecordError(err)
		span.SetStatus(codes.Error, "Exec failed to create executor")
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
			span.SetAttributes(attribute.Int("exit_code", exitCode))
			span.SetStatus(codes.Ok, "Execution succeeded with remote error")
			if !sensitive {
				span.SetAttributes(
					attribute.String("stdout", stdout.String()),
					attribute.String("stderr", stderr.String()),
				)
			}
			return stdout, stderr, err
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "Exec failed to stream")
		span.AddEvent("Exec failed to stream")
		return stdout, stderr, errors.Wrap(err, "Exec failed to stream")
	}
	span.SetAttributes(attribute.Int("exit_code", 0))
	span.SetStatus(codes.Ok, "Exec success")
	span.AddEvent("Execution completed")
	return stdout, stderr, err
}

// Exec executes a command in a pod. Logs input and output if exit code != 0. Should be used in rare cases as might reveal sensitive data.
func (e *Exec) Exec(ctx context.Context, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	return e.exec(ctx, "", false, command)
}

// ExecNamed executes a command in a pod. Logs input and output if exit code != 0. However, provides a name for the span.
func (e *Exec) ExecNamed(ctx context.Context, name string, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	return e.exec(ctx, fmt.Sprintf("Exec.%s", name), false, command)
}

func (e *Exec) ExecSensitive(ctx context.Context, name string, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	return e.exec(ctx, fmt.Sprintf("Exec.%s", name), true, command)
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
			return "weka-operator-system", nil
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
