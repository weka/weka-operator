package services

import (
	"context"
	"errors"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type SecretsService interface {
	EnsureLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error
	EnsureClientLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error
}

func NewSecretsService(client client.Client, scheme *runtime.Scheme, service ExecService) SecretsService {
	return &secretsService{
		Client:      client,
		Scheme:      scheme,
		ExecService: service,
	}
}

type secretsService struct {
	Client      client.Client
	Scheme      *runtime.Scheme
	ExecService ExecService
}

func (r *secretsService) EnsureLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureLoginCredentials")
	defer end()

	kubeService := NewKubeService(r.Client)

	// generate random password
	operatorLogin := cluster.NewOperatorLoginSecret()
	userLogin := cluster.NewUserLoginSecret()

	if err := kubeService.EnsureSecret(ctx, operatorLogin, &K8sOwnerRef{
		Scheme: r.Scheme,
		Obj:    cluster,
	}); err != nil {
		return err
	}

	if err := kubeService.EnsureSecret(ctx, userLogin, &K8sOwnerRef{
		Scheme: r.Scheme,
		Obj:    cluster,
	}); err != nil {
		return err
	}

	return nil
}

func (r *secretsService) EnsureClientLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureClientLoginCredentials")
	defer end()

	container := cluster.SelectActiveContainer(ctx, containers, wekav1alpha1.WekaContainerModeDrive)
	if container == nil {
		return errors.New("no active container found")
	}

	kubeService := NewKubeService(r.Client)
	wekaService := NewWekaService(r.ExecService, container)
	joinSecret, err := wekaService.GenerateJoinSecret(ctx)
	if err != nil {
		return err
	}

	clientSecret := cluster.NewClientSecret()
	clientSecret.StringData["join-secret"] = joinSecret

	if err := kubeService.EnsureSecret(ctx, clientSecret, &K8sOwnerRef{
		Scheme: r.Scheme,
		Obj:    cluster,
	}); err != nil {
		return err
	}

	return nil
}
