package services

import (
	"context"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type SecretsService interface {
	EnsureLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error
}

func NewSecretsService(client client.Client, scheme *runtime.Scheme) SecretsService {
	return &secretsService{
		Client: client,
		Scheme: scheme,
	}
}

type secretsService struct {
	Client client.Client
	Scheme *runtime.Scheme
}

func (r *secretsService) EnsureLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureLoginCredentials")
	defer end()

	// generate random password
	operatorLogin := cluster.NewOperatorLoginSecret()
	userLogin := cluster.NewUserLoginSecret()

	ensureSecret := func(secret *v1.Secret) error {
		err := r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: secret.Name}, secret)
		if err != nil && apierrors.IsNotFound(err) {
			err := ctrl.SetControllerReference(cluster, secret, r.Scheme)
			if err != nil {
				return err
			}

			err = r.Client.Create(ctx, secret)
			if err != nil {
				return err
			}
		}

		return nil
	}

	if err := ensureSecret(operatorLogin); err != nil {
		return err
	}
	if err := ensureSecret(userLogin); err != nil {
		return err
	}
	return nil
}
