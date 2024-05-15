package services

import (
	"context"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type K8sOwnerRef struct {
	Scheme *runtime.Scheme
	Obj    metav1.Object
}

type KubeService interface {
	GetNode(ctx context.Context, nodeName string) (*v1.Node, error)
	GetSecret(ctx context.Context, secretName, namespace string) (*v1.Secret, error)
	EnsureSecret(ctx context.Context, secret *v1.Secret, owner *K8sOwnerRef) error
}

func NewKubeService(client client.Client) KubeService {
	return &ApiKubeService{
		Client: client,
	}
}

type ApiKubeService struct {
	Client client.Client
}

func (s *ApiKubeService) GetNode(ctx context.Context, nodeName string) (*v1.Node, error) {
	node := &v1.Node{}
	err := s.Client.Get(ctx, client.ObjectKey{
		Name: nodeName,
	}, node)
	if err != nil {
		return nil, err
	}
	return node, nil
}

func (s *ApiKubeService) GetSecret(ctx context.Context, secretName, namespace string) (*v1.Secret, error) {
	secret := &v1.Secret{}
	err := s.Client.Get(ctx, client.ObjectKey{
		Name:      secretName,
		Namespace: namespace,
	}, secret)
	if err != nil {
		return nil, err
	}
	return secret, nil
}

func (s *ApiKubeService) EnsureSecret(ctx context.Context, secret *v1.Secret, owner *K8sOwnerRef) error {
	existing := &v1.Secret{}
	err := s.Client.Get(ctx, client.ObjectKey{Namespace: secret.ObjectMeta.Namespace, Name: secret.ObjectMeta.Name}, existing)
	if err != nil && apierrors.IsNotFound(err) {
		if owner != nil {
			err := ctrl.SetControllerReference(owner.Obj, secret, owner.Scheme)
			if err != nil {
				return err
			}
		}

		err = s.Client.Create(ctx, secret)
		if err != nil {
			return err
		}
	}

	return nil
}
