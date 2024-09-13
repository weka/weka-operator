package services

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	v1 "k8s.io/api/core/v1"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type SecretsService interface {
	EnsureLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error
	EnsureClientLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error
	EnsureCSILoginCredentials(ctx context.Context, clusterService WekaClusterService) error
	UpdateOperatorLoginSecret(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error
}

func NewSecretsService(client client.Client, scheme *runtime.Scheme, service exec.ExecService) SecretsService {
	return &secretsService{
		Client:      client,
		Scheme:      scheme,
		ExecService: service,
	}
}

type secretsService struct {
	Client      client.Client
	Scheme      *runtime.Scheme
	ExecService exec.ExecService
}

func (r *secretsService) UpdateOperatorLoginSecret(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "updateOperatorLoginSecret")
	defer end()

	secret := v1.Secret{}
	err := r.Client.Get(ctx, client.ObjectKey{
		Name:      cluster.GetOperatorSecretName(),
		Namespace: cluster.Namespace,
	}, &secret)
	if err != nil {
		return err
	}

	currentUsername := string(secret.Data["username"])

	if currentUsername == cluster.GetOperatorClusterUsername() {
		return nil
	}

	secret.Data["username"] = []byte(cluster.GetOperatorClusterUsername())
	// update
	if err := r.Client.Update(ctx, &secret); err != nil {
		return err
	}

	return nil
}

func (r *secretsService) EnsureCSILoginCredentials(ctx context.Context, clusterService WekaClusterService) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureCSILoginCredentials")
	defer end()

	kubeService := kubernetes.NewKubeService(r.Client)
	containers, err := clusterService.GetOwnedContainers(ctx, wekav1alpha1.WekaContainerModeDrive)
	if err != nil {
		return err
	}

	endpoints := []string{}
	for _, container := range containers {
		endpoints = append(endpoints, fmt.Sprintf("%s:%d", container.Status.ManagementIP, container.GetPort()))
	}

	cluster := clusterService.GetCluster()
	clientSecret := cluster.NewCsiSecret(endpoints)

	if err := kubeService.EnsureSecret(ctx, clientSecret, &kubernetes.K8sOwnerRef{
		Scheme: r.Scheme,
		Obj:    cluster,
	}); err != nil {
		return err
	}
	return nil
}

func (r *secretsService) EnsureLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureLoginCredentials")
	defer end()

	kubeService := kubernetes.NewKubeService(r.Client)

	// generate random password
	operatorLogin := cluster.NewOperatorLoginSecret()
	// Hacking it into initial (admin) username, and we will update later once new user is created
	// Both admin and user have the same password
	operatorLogin.StringData["username"] = cluster.GetInitialOperatorUsername()
	userLogin := cluster.NewUserLoginSecret()

	if cluster.Spec.OperatorSecretRef != "" {
		return nil
	}

	if err := kubeService.EnsureSecret(ctx, operatorLogin, &kubernetes.K8sOwnerRef{
		Scheme: r.Scheme,
		Obj:    cluster,
	}); err != nil {
		return err
	}

	if err := kubeService.EnsureSecret(ctx, userLogin, &kubernetes.K8sOwnerRef{
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

	container, err := cluster.SelectActiveContainer(ctx, containers, wekav1alpha1.WekaContainerModeDrive)
	if err != nil {
		return err
	}

	kubeService := kubernetes.NewKubeService(r.Client)
	wekaService := NewWekaService(r.ExecService, container)
	joinSecret, err := wekaService.GenerateJoinSecret(ctx)
	if err != nil {
		return err
	}

	clientSecret := cluster.NewClientSecret()
	clientSecret.StringData["join-secret"] = joinSecret

	if err := kubeService.EnsureSecret(ctx, clientSecret, &kubernetes.K8sOwnerRef{
		Scheme: r.Scheme,
		Obj:    cluster,
	}); err != nil {
		return err
	}

	return nil
}
