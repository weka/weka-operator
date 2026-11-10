package podexec

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	ExecSensitiveWithStdin(ctx context.Context, name string, command []string, stdin string) (stdout, stderr bytes.Buffer, err error)
}

// ChannelError marks a failure of the pods/exec transport (API server, network, executor), not of the command.
type ChannelError struct{ Err error }

func (e *ChannelError) Error() string { return e.Err.Error() }
func (e *ChannelError) Unwrap() error { return e.Err }

// IsChannelError reports whether err is a transport failure of the API server / pods/exec connection: a
// ChannelError, an API 5xx or 429, or a network-level error. Command failures (exec.ExitError), container-side
// errors reported by the kubelet, TLS/config errors, definitive API answers and context expiry are not.
func IsChannelError(err error) bool {
	if err == nil {
		return false
	}
	var ce *ChannelError
	if errors.As(err, &ce) {
		return true
	}
	return isTransportError(err)
}

func isTransportError(err error) bool {
	var exitErr exec.ExitError
	if errors.As(err, &exitErr) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var statusErr apierrors.APIStatus
	if errors.As(err, &statusErr) {
		code := statusErr.Status().Code
		return code >= 500 || code == http.StatusTooManyRequests
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	var x509Err x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var certErr x509.CertificateInvalidError
	var recErr *tls.RecordHeaderError
	if errors.As(err, &x509Err) || errors.As(err, &hostErr) || errors.As(err, &certErr) || errors.As(err, &recErr) {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
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

func (e *PodExec) exec(ctx context.Context, name string, sensitive bool, command []string, stdin io.Reader) (stdout, stderr bytes.Buffer, err error) {
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
			Stdin:     stdin != nil,
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
		Stdin:  stdin,
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
		wrapped := errors.Wrap(err, "Exec failed to stream")
		// The exec's own timeout is a hung command, not a broken channel.
		if ctx.Err() == nil && isTransportError(err) {
			return stdout, stderr, &ChannelError{Err: wrapped}
		}
		return stdout, stderr, wrapped
	}
	logger.SetValues("exit_code", 0)
	logger.SetStatus(codes.Ok, "Exec success")
	logger.AddEvent("Execution completed")
	return stdout, stderr, err
}

// Exec executes a command in a pod. Logs input and output if exit code != 0. Should be used in rare cases as might reveal sensitive data.
func (e *PodExec) Exec(ctx context.Context, command []string) (stdout, stderr bytes.Buffer, err error) {
	return e.exec(ctx, "", false, command, nil)
}

// ExecNamed executes a command in a pod. Logs input and output if exit code != 0. However, provides a name for the span.
func (e *PodExec) ExecNamed(ctx context.Context, name string, command []string) (stdout, stderr bytes.Buffer, err error) {
	return e.exec(ctx, fmt.Sprintf("Exec.%s", name), false, command, nil)
}

func (e *PodExec) ExecSensitive(ctx context.Context, name string, command []string) (stdout, stderr bytes.Buffer, err error) {
	return e.exec(ctx, fmt.Sprintf("Exec.%s", name), true, command, nil)
}

// ExecSensitiveWithStdin is for secrets: unlike argv, stdin is not part of the pods/exec request URI that API server audit logs record.
func (e *PodExec) ExecSensitiveWithStdin(ctx context.Context, name string, command []string, stdin string) (stdout, stderr bytes.Buffer, err error) {
	return e.exec(ctx, fmt.Sprintf("Exec.%s", name), true, command, strings.NewReader(stdin))
}
