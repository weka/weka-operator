package services

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/util"

	"go.opentelemetry.io/otel/codes"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type WekaContainerService interface {
	// Driver Management
	EnsureDriversLoader(ctx context.Context) error
	CheckIfLoaderFinished(ctx context.Context) error

	ReconcileManagementIP(ctx context.Context) error
	ReconcileWekaLocalStatus(ctx context.Context) error
}

func NewWekaContainerService(
	client client.Client,
	crdManager CrdManager,
	execService ExecService,
	container *wekav1alpha1.WekaContainer,
) WekaContainerService {
	return &wekaContainerService{
		Container: container,

		Client:      client,
		CrdManager:  crdManager,
		ExecService: execService,
	}
}

type wekaContainerService struct {
	Container *wekav1alpha1.WekaContainer

	Client      client.Client
	CrdManager  CrdManager
	ExecService ExecService
}

// Errors ---------------------------------------------------------------------

type ContainerServiceError struct {
	errors.WrappedError
	Container *wekav1alpha1.WekaContainer
	Method    string
	Message   string
}

func (e *ContainerServiceError) Error() string {
	return fmt.Sprintf("container service error - container: %s, method: %s, message: %s, error: %v",
		e.Container.Name, e.Method, e.Message, e.WrappedError.Error())
}

type ContainerExecError struct {
	errors.WrappedError
	Container *wekav1alpha1.WekaContainer
	Command   string
	StdErr    string
}

func (e *ContainerExecError) Error() string {
	return fmt.Sprintf("container exec error - container: %s, command: %s, stderr: %s, error: %v",
		e.Container.Name, e.Command, e.StdErr, e.WrappedError.Error())
}

// Driver Management -----------------------------------------------------------
func (s *wekaContainerService) EnsureDriversLoader(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureDriversLoader")
	defer end()

	container := s.Container
	if container == nil {
		return &errors.ArgumentError{ArgName: "container", Message: "container is nil"}
	}

	logger.SetValues("container", container.Name)

	if s.CrdManager == nil {
		return &lifecycle.StateError{Property: "CrdManager", Message: "CrdManager is nil"}
	}

	pod, err := s.CrdManager.RefreshPod(ctx, container)
	if err != nil {
		return &ContainerServiceError{
			WrappedError: errors.WrappedError{Err: err},
			Container:    container,
			Method:       "EnsureDriverLoader",
			Message:      "Error refreshing pod",
		}
	}
	// namespace := pod.Namespace
	namespace, err := util.GetPodNamespace()
	if err != nil {
		logger.Error(err, "GetPodNamespace")
		return err
	}
	loaderContainer := &wekav1alpha1.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "weka-drivers-loader-" + pod.Spec.NodeName,
			Namespace: namespace,
		},
		Spec: wekav1alpha1.WekaContainerSpec{
			Image:               container.Spec.Image,
			Mode:                wekav1alpha1.WekaContainerModeDriversLoader,
			ImagePullSecret:     container.Spec.ImagePullSecret,
			Hugepages:           0,
			NodeAffinity:        container.Spec.NodeAffinity,
			DriversDistService:  container.Spec.DriversDistService,
			TracesConfiguration: container.Spec.TracesConfiguration,
		},
	}

	found := &wekav1alpha1.WekaContainer{}
	err = s.Client.Get(ctx, client.ObjectKey{Name: loaderContainer.Name, Namespace: loaderContainer.ObjectMeta.Namespace}, found)
	l := logger.WithValues("container", loaderContainer.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			l.Info("Creating drivers loader pod", "node_name", pod.Spec.NodeName, "namespace", loaderContainer.Namespace)
			err = s.Client.Create(ctx, loaderContainer)
			if err != nil {
				l.Error(err, "Error creating drivers loader pod")
				return err
			}
		}
	}
	if found != nil {
		logger.InfoWithStatus(codes.Ok, "Drivers loader pod already exists")
		return nil // TODO: Update handling?
	}
	// Should we have an owner? Or should we just delete it once done? We cant have owner in different namespace
	// It would be convenient, if container would just exit.
	// Maybe, we should just replace this with completely different entry point and consolidate everything under single script
	// Agent does us no good. Container that runs on-time and just finished and removed afterwards would be simpler
	loaderContainer.Status.Status = "Active"
	if err := s.Client.Status().Update(ctx, loaderContainer); err != nil {
		l.Error(err, "Failed to update status of container")
		return err

	}
	return nil
}

// ReconcileManagementIP -------------------------------------------------------
func (s *wekaContainerService) ReconcileManagementIP(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ReconcileManagementIP")
	defer end()

	container := s.Container

	if container.Status.ManagementIP != "" {
		return nil
	}
	executor, err := s.ExecService.GetExecutor(ctx, s.Container)
	if err != nil {
		return &ContainerServiceError{
			WrappedError: errors.WrappedError{
				Err:  err,
				Span: instrumentation.GetLogName(ctx),
			},
			Container: container,
			Method:    "ReconcileManagementIP",
			Message:   "Error getting executor",
		}
	}

	var getIpCmd string
	if container.Spec.Network.EthDevice != "" {
		getIpCmd = fmt.Sprintf("ip addr show dev %s | grep 'inet ' | awk '{print $2}' | cut -d/ -f1", container.Spec.Network.EthDevice)
	} else {
		getIpCmd = "ip route show default | grep src | awk '/default/ {print $9}' | head -n1"
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "GetManagementIpAddress", []string{"bash", "-ce", getIpCmd})
	if err != nil {
		logger.Error(err, "Error executing command", "stderr", stderr.String())
		return err
	}
	ipAddress := strings.TrimSpace(stdout.String())
	logger.WithValues("management_ip", ipAddress).Info("Got management IP")
	if container.Status.ManagementIP != ipAddress {
		container.Status.ManagementIP = ipAddress
		if err := s.Client.Status().Update(ctx, container); err != nil {
			return &ContainerServiceError{
				WrappedError: errors.WrappedError{
					Err:  err,
					Span: instrumentation.GetLogName(ctx),
				},
				Container: container,
				Method:    "ReconcileManagementIP",
				Message:   "Error updating container status",
			}
		}
		return &errors.RetryableError{
			Err:        nil,
			RetryAfter: 3 * time.Second,
		}
	}
	return nil
}

// ReconcileWekaLocalStatus ----------------------------------------------------
func (s *wekaContainerService) ReconcileWekaLocalStatus(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "reconcileWekaLocalStatus")
	defer end()

	container := s.Container

	if slices.Contains([]string{wekav1alpha1.WekaContainerModeDriversLoader}, container.Spec.Mode) {
		return nil
	}

	executor, err := s.ExecService.GetExecutor(ctx, container)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return err
	}

	statusCommand := "weka local ps -J"
	stdout, stderr, err := executor.ExecNamed(ctx, "WekaLocalPs", []string{"bash", "-ce", statusCommand})
	if err != nil {
		// logger.Error(err, "Error executing command", "command", statusCommand, "stderr", stderr.String())
		return &ContainerExecError{
			WrappedError: errors.WrappedError{
				Err:  err,
				Span: instrumentation.GetLogName(ctx),
			},
			Container: container,
			Command:   statusCommand,
			StdErr:    stderr.String(),
		}
	}
	response := []resources.WekaLocalPs{}
	err = json.Unmarshal(stdout.Bytes(), &response)
	if err != nil {
		return &ContainerServiceError{
			WrappedError: errors.WrappedError{
				Err:  err,
				Span: instrumentation.GetLogName(ctx),
			},
			Container: container,
			Method:    "ReconcileWekaLocalStatus",
			Message:   "Error unmarshalling JSON",
		}
	}

	if len(response) == 0 {
		return &ContainerServiceError{
			WrappedError: errors.WrappedError{
				Err:  nil,
				Span: instrumentation.GetLogName(ctx),
			},
			Container: container,
			Method:    "ReconcileWekaLocalStatus",
			Message:   "No weka containers found",
		}
	}

	found := false
	for _, c := range response {
		if c.Name == container.Spec.WekaContainerName {
			found = true
			break
		}
	}

	if !found {
		logger.InfoWithStatus(codes.Error, "Weka container not found", "name", response, "expected_name", container.Spec.WekaContainerName)
		return &errors.RetryableError{
			Err:        nil,
			RetryAfter: 3 * time.Second,
		}
	}

	status := response[0].RunStatus
	if container.Status.Status != status {
		logger.Info("Updating status", "from", container.Status.Status, "to", status)
		container.Status.Status = status
		if err := s.Client.Status().Update(ctx, container); err != nil {
			return &ContainerServiceError{
				WrappedError: errors.WrappedError{
					Err:  err,
					Span: instrumentation.GetLogName(ctx),
				},
				Container: container,
				Method:    "ReconcileWekaLocalStatus",
				Message:   "Error updating container status",
			}
		}
		return &errors.RetryableError{
			Err:        nil,
			RetryAfter: 3 * time.Second,
		}
	}
	return nil
}

type LoaderNotFinishedError struct {
	ContainerExecError
	Message string
}

// CheckIfDriverLoaderFinished ------------------------------------------------
func (s *wekaContainerService) CheckIfLoaderFinished(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "CheckIfLoaderFinished")
	defer end()

	executor, err := s.ExecService.GetExecutor(ctx, s.Container)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return err
	}
	cmd := "cat /tmp/weka-drivers-loader"
	stdout, stderr, err := executor.ExecNamed(ctx, "CheckDriversLoaded", []string{"bash", "-ce", cmd})
	if err != nil {
		if strings.Contains(stderr.String(), "No such file or directory") {
			return &LoaderNotFinishedError{
				ContainerExecError: ContainerExecError{
					WrappedError: errors.WrappedError{
						Err:  err,
						Span: instrumentation.GetLogName(ctx),
					},
					Container: s.Container,
					Command:   cmd,
					StdErr:    stderr.String(),
				},
			}
		}
		return &ContainerExecError{
			WrappedError: errors.WrappedError{
				Err:  err,
				Span: instrumentation.GetLogName(ctx),
			},
			Container: s.Container,
			Command:   cmd,
			StdErr:    stderr.String(),
		}
	}
	if strings.TrimSpace(stdout.String()) == "drivers_loaded" {
		return nil
	}
	return &LoaderNotFinishedError{
		ContainerExecError: ContainerExecError{
			WrappedError: errors.WrappedError{
				Err:  nil,
				Span: instrumentation.GetLogName(ctx),
			},
			Container: s.Container,
			Command:   cmd,
			StdErr:    stdout.String(),
		},
	}
}
