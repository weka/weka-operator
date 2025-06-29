package services

import (
	"context"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const DefaultOrg = "Root"

func NewUserLoginSecret(ctx context.Context, cluster *wekav1alpha1.WekaCluster) *v1.Secret {
	return &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.GetUserSecretName(),
			Namespace: cluster.Namespace,
		},
		StringData: map[string]string{
			"username": cluster.GetUserClusterUsername(),
			"password": util.GeneratePassword(32),
			"org":      DefaultOrg,
		},
	}
}

func NewOperatorLoginSecret(ctx context.Context, cluster *wekav1alpha1.WekaCluster) *v1.Secret {
	return &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.GetOperatorSecretName(),
			Namespace: cluster.Namespace,
		},
		StringData: map[string]string{
			"username":    cluster.GetOperatorClusterUsername(),
			"password":    util.GeneratePassword(32),
			"join-secret": util.GeneratePassword(64),
			"org":         DefaultOrg,
		},
	}
}

func NewClientSecret(ctx context.Context, cluster *wekav1alpha1.WekaCluster) *v1.Secret {
	return &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.GetClientSecretName(),
			Namespace: cluster.Namespace,
		},
		StringData: map[string]string{
			"username": cluster.GetClusterClientUsername(),
			"password": util.GeneratePassword(32),
			"org":      DefaultOrg,
		},
	}
}

func NewCsiSecret(ctx context.Context, cluster *wekav1alpha1.WekaCluster, endpoints []string, nfsTargetIps []string) *v1.Secret {
	nfsTargetIpsStr := ""
	if len(nfsTargetIps) > 0 && nfsTargetIps[0] != "" {
		nfsTargetIpsStr = strings.Join(nfsTargetIps, ",")
	}
	ret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.GetCsiSecretName(),
			Namespace: cluster.Namespace,
		},
		StringData: map[string]string{
			"username":     cluster.GetClusterCsiUsername(),
			"password":     util.GeneratePassword(32),
			"organization": DefaultOrg,
			"endpoints":    strings.Join(endpoints, ","),
			"scheme":       "https",
		},
	}
	if nfsTargetIpsStr != "" {
		ret.StringData["nfsTargetIps"] = nfsTargetIpsStr
	}
	return ret
}

type SecretsService interface {
	EnsureLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error
	EnsureClientLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster, container *wekav1alpha1.WekaContainer) error
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

func (r *secretsService) EnsureLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureLoginCredentials")
	defer end()

	kubeService := kubernetes.NewKubeService(r.Client)

	// generate random password
	operatorLogin := NewOperatorLoginSecret(ctx, cluster)
	// Hacking it into initial (admin) username, and we will update later once new user is created
	// Both admin and user have the same password
	operatorLogin.StringData["username"] = cluster.GetInitialOperatorUsername()
	userLogin := NewUserLoginSecret(ctx, cluster)

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

func (r *secretsService) EnsureClientLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster, container *wekav1alpha1.WekaContainer) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureClientLoginCredentials")
	defer end()

	kubeService := kubernetes.NewKubeService(r.Client)
	wekaService := NewWekaService(r.ExecService, container)
	joinSecret, err := wekaService.GenerateJoinSecret(ctx)
	if err != nil {
		return err
	}

	clientSecret := NewClientSecret(ctx, cluster)
	clientSecret.StringData["join-secret"] = joinSecret

	if err := kubeService.EnsureSecret(ctx, clientSecret, &kubernetes.K8sOwnerRef{
		Scheme: r.Scheme,
		Obj:    cluster,
	}); err != nil {
		return err
	}

	return nil
}
