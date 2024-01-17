package util

import (
	"bytes"
	"context"
	"os"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"
)

type Exec struct {
	ClientSet *kubernetes.Clientset
	Pod       *v1.Pod
	Config    *rest.Config
}

type ConfigurationError struct {
	Err error
}

func (e *ConfigurationError) Error() string {
	return "failed to get config"
}

func NewExecInPod(pod *v1.Pod) (*Exec, error) {
	config, err := KubernetesConfiguration()
	if err != nil {
		return nil, errors.Wrap(&ConfigurationError{err}, "failed to get config")
	}

	clientset, err := KubernetesClientSet(config)
	if err != nil {
		return nil, errors.Wrap(&ConfigurationError{err}, "failed to get clientset")
	}

	return &Exec{
		ClientSet: clientset,
		Pod:       pod,
		Config:    config,
	}, nil
}

func (e *Exec) Exec(ctx context.Context, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	podExec := e.ClientSet.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(e.Pod.Name).
		Namespace(e.Pod.Namespace).
		SubResource("exec").
		VersionedParams(&v1.PodExecOptions{
			Container: "weka-agent",
			Command:   command,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(e.Config, "POST", podExec.URL())
	if err != nil {
		return stdout, stderr, errors.Wrap(err, "Exec failed to create executor")
	}

	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
		Tty:    false,
	})
	if err != nil {
		return stdout, stderr, errors.Wrap(err, "Exec failed to stream")
	}

	return stdout, stderr, nil
}

func KubernetesClientSet(config *rest.Config) (*kubernetes.Clientset, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get clientset")
	}

	return clientset, nil
}

func KubernetesConfiguration() (*rest.Config, error) {
	kubeConfigPath := os.Getenv("KUBECONFIG")
	if kubeConfigPath == "" {
		return rest.InClusterConfig()
	} else {
		return clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	}
}
