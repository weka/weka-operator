package services

import (
	"context"

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
}

func NewWekaContainerService(client client.Client, crdManager CrdManager, container *wekav1alpha1.WekaContainer) WekaContainerService {
	return &wekaContainerService{
		Container: container,

		Client:     client,
		CrdManager: crdManager,
	}
}

type wekaContainerService struct {
	Container *wekav1alpha1.WekaContainer

	Client     client.Client
	CrdManager CrdManager
}

// Errors ---------------------------------------------------------------------

type ContainerServiceError struct {
	errors.WrappedError
	Container *wekav1alpha1.WekaContainer
	Method    string
	Message   string
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
