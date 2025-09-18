package util

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	"go.opentelemetry.io/otel/codes"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"
	"k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
)

type Exec interface {
	Exec(ctx context.Context, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error)
	ExecNamed(ctx context.Context, name string, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error)
	ExecSensitive(ctx context.Context, name string, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error)
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

type ConfigurationError struct {
	Err     error
	Message string
}

func (e *ConfigurationError) Error() string {
	return fmt.Sprintf("configuration error: %s, %v", e.Message, e.Err)
}

func NewExecWithConfig(client rest.Interface, cfg *rest.Config, pod types.NamespacedName, timeout *time.Duration, containerName, nodeName string) (Exec, error) {
	if timeout == nil {
		defaultTimeout := config.Config.Timeouts.KubeExecTimeout
		timeout = &defaultTimeout
	}

	return &PodExec{
		RestClient:    client,
		Pod:           pod,
		ContainerName: containerName,
		RestConfig:    cfg,
		timeout:       timeout,
		NodeName:      nodeName,
	}, nil
}

func NewExecInPod(client rest.Interface, cfg *rest.Config, pod *v1.Pod) (Exec, error) {
	return NewExecInPodWithTimeout(client, cfg, pod, nil)
}

func NewExecInPodWithTimeout(client rest.Interface, cfg *rest.Config, pod *v1.Pod, timeout *time.Duration) (Exec, error) {
	namespacedObject := types.NamespacedName{
		Namespace: pod.Namespace,
		Name:      pod.Name,
	}
	return NewExecWithConfig(client, cfg, namespacedObject, timeout, "weka-container", pod.Spec.NodeName)
}

func NewExecInPodByName(client rest.Interface, cfg *rest.Config, pod *v1.Pod, containerName string, timeout *time.Duration) (Exec, error) {
	namespacedObject := types.NamespacedName{
		Namespace: pod.Namespace,
		Name:      pod.Name,
	}

	return NewExecWithConfig(client, cfg, namespacedObject, timeout, containerName, pod.Spec.NodeName)
}

func (e *PodExec) exec(ctx context.Context, name string, sensitive bool, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "Exec", "command_name", name)
	defer end()

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

func GetOperatorDeployment(ctx context.Context, k8sClient crclient.Client) (*appsv1.Deployment, error) {
	if config.Config.OperatorDeploymentName == "" {
		return nil, &ConfigurationError{Message: "Operator deployment name is not set"}
	}

	namespace, err := GetPodNamespace()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get operator namespace")
	}

	var deployment appsv1.Deployment
	err = k8sClient.Get(ctx, types.NamespacedName{
		Name:      config.Config.OperatorDeploymentName,
		Namespace: namespace,
	}, &deployment)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get operator deployment")
	}

	return &deployment, nil
}

func GetPodNamespace() (string, error) {
	if config.Config.OperatorPodNamespace != "" {
		return config.Config.OperatorPodNamespace, nil
	}
	namespace, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		if os.IsNotExist(err) && config.Config.DevMode {
			return config.Consts.DevModeNamespace, nil
		}
		return "", err
	}
	return string(namespace), nil
}

func GetOperatorPodName() (string, error) {
	if config.Config.OperatorPodName != "" {
		return config.Config.OperatorPodName, nil
	}

	name := os.Getenv("HOSTNAME")
	if name == "" {
		return "", errors.New("environment variable HOSTNAME is not set")
	}
	return name, nil
}

func GetOperatorPod(ctx context.Context, client client.Client) (*v1.Pod, error) {
	namespace, err := GetPodNamespace()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get operator namespace")
	}

	name, err := GetOperatorPodName()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get operator pod name")
	}

	pod := &v1.Pod{}
	err = client.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, pod)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get operator pod")
	}

	return pod, nil
}

func IsEqualConfigMapData(cm1, cm2 *v1.ConfigMap) bool {
	return reflect.DeepEqual(cm1.Data, cm2.Data)
}

// GetKubeField retrieves a field from an unstructured object given a dot-separated path.

func GetKubeField(obj *unstructured.Unstructured, fieldPath string) (interface{}, error) {
	fields := strings.Split(strings.TrimPrefix(fieldPath, "."), ".")
	value, found, err := unstructured.NestedFieldCopy(obj.Object, fields...)
	if err != nil {
		return nil, fmt.Errorf("error retrieving field %s: %w", fieldPath, err)
	}
	if !found {
		return nil, fmt.Errorf("field %s not found", fieldPath)
	}
	return value, nil
}

// GetKubeFieldValue converts the retrieved field to any specified type.
func GetKubeFieldValue[T any](obj *unstructured.Unstructured, fieldPath string) (T, error) {
	value, err := GetKubeField(obj, fieldPath)
	if err != nil {
		var zero T
		return zero, err
	}
	result, ok := value.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("field %s is not of expected type", fieldPath)
	}
	return result, nil
}

// ConvertToUnstructured converts any typed object (e.g. corev1.Node, corev1.Pod) to an unstructured.Unstructured.
func ConvertToUnstructured[T runtime.Object](obj T) (*unstructured.Unstructured, error) {
	unstrMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("error converting object: %w", err)
	}
	return &unstructured.Unstructured{Object: unstrMap}, nil
}

// GetKubeObjectFieldValue combines conversion and field extraction.
// It accepts any runtime.Object (like corev1.Node or corev1.Pod) and returns the field value of the specified type.
func GetKubeObjectFieldValue[T any, K runtime.Object](obj K, fieldPath string) (T, error) {
	unstr, err := ConvertToUnstructured(obj)
	if err != nil {
		var zero T
		return zero, err
	}
	return GetKubeFieldValue[T](unstr, fieldPath)
}

func GetKubernetesVersion(config *rest.Config) (string, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", errors.Wrap(err, "failed to create kubernetes clientset")
	}

	version, err := clientset.Discovery().ServerVersion()
	if err != nil {
		return "", errors.Wrap(err, "failed to get server version")
	}

	return version.String(), nil
}
