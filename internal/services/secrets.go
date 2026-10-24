package services

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
	wekav1alpha1 "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
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

func NewCsiSecret(ctx context.Context, cluster *wekav1alpha1.WekaCluster, endpoints, nfsTargetIps []string) *v1.Secret {
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
	GetAdminCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster) (*v1.Secret, error)
}

func NewSecretsService(k8sClient client.Client, scheme *runtime.Scheme, service exec.ExecService) SecretsService {
	return &secretsService{
		Client:      k8sClient,
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
	ctx, spanLogger := instrumentation.CreateLogSpan(ctx, "updateOperatorLoginSecret")
	defer spanLogger.End()

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
	ctx, spanLogger := instrumentation.CreateLogSpan(ctx, "ensureLoginCredentials")
	defer spanLogger.End()

	kubeService := kubernetes.NewKubeService(r.Client)

	// generate random password
	operatorLogin := NewOperatorLoginSecret(ctx, cluster)
	// Hacking it into initial (admin) username, and we will update later once new user is created
	// Both admin and user have the same password
	operatorLogin.StringData["username"] = cluster.GetInitialOperatorUsername()

	if cluster.Spec.OperatorSecretRef != "" {
		return nil
	}

	if err := kubeService.EnsureSecret(ctx, operatorLogin, &kubernetes.K8sOwnerRef{
		Scheme: r.Scheme,
		Obj:    cluster,
	}); err != nil {
		return err
	}

	if cluster.Spec.AdminCredentialsSecretRef != nil {
		_, err := r.GetAdminCredentials(ctx, cluster)
		return err
	}

	if err := kubeService.EnsureSecret(ctx, NewUserLoginSecret(ctx, cluster), &kubernetes.K8sOwnerRef{
		Scheme: r.Scheme,
		Obj:    cluster,
	}); err != nil {
		return err
	}

	return nil
}

func (r *secretsService) GetAdminCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster) (*v1.Secret, error) {
	name := cluster.GetUserSecretName()
	// the client and CSI steps reuse an existing Secret and skip existing users, so sharing one of these
	// would hand the admin credentials to clients or CSI, and the CSI step would write into the user's Secret
	if slices.Contains([]string{cluster.GetOperatorSecretName(), cluster.GetClientSecretName(), wekav1alpha1.GetCsiSecretName(cluster.Name)}, name) {
		return nil, fmt.Errorf("admin credentials secret %s: name is reserved for operator-managed credentials", name)
	}
	secret, err := kubernetes.NewKubeService(r.Client).GetSecret(ctx, name, cluster.Namespace)
	if err != nil {
		return nil, fmt.Errorf("get admin credentials secret %s: %w", name, err)
	}
	username := string(secret.Data["username"])
	if username == "" || len(secret.Data["password"]) == 0 {
		return nil, fmt.Errorf("admin credentials secret %s must have non-empty username and password", name)
	}
	// a NUL cannot be passed to the weka CLI, the password would be cut at it
	if bytes.IndexByte(secret.Data["password"], 0) >= 0 {
		return nil, fmt.Errorf("admin credentials secret %s: password must not contain a NUL byte", name)
	}
	// "admin" is removed by DeleteAdminUser, the rest are operator-managed users: EnsureUser skips existing users,
	// so sharing one of their names leaves a password unapplied
	reserved := []string{
		cluster.GetInitialOperatorUsername(),
		cluster.GetOperatorClusterUsername(),
		cluster.GetClusterClientUsername(),
		cluster.GetClusterCsiUsername(),
	}
	if slices.Contains(reserved, username) {
		return nil, fmt.Errorf("admin credentials secret %s: username %q is reserved", name, username)
	}
	return secret, nil
}

func (r *secretsService) EnsureClientLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster, container *wekav1alpha1.WekaContainer) error {
	ctx, spanLogger := instrumentation.CreateLogSpan(ctx, "ensureClientLoginCredentials")
	defer spanLogger.End()

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
