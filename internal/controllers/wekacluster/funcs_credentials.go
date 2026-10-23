package wekacluster

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1/condition"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

func (r *wekaClusterReconcilerLoop) ApplyCredentials(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)
	cluster := r.cluster
	containers := r.containers

	container := discovery.SelectActiveContainer(containers)
	if container == nil {
		return errors.New("No active container found")
	}
	wekaService := services.NewWekaService(r.ExecService, container)

	logger.SetValues("user_name", cluster.GetOperatorClusterUsername())
	logger.Info("Ensuring operator user")
	username, password, err := r.getUsernameAndPassword(ctx, cluster.Namespace, cluster.GetOperatorSecretName())
	if err != nil {
		return err
	}
	if username == "admin" {
		// force switch back to operator user
		username = cluster.GetOperatorClusterUsername()
	}
	err = wekaService.EnsureUser(ctx, username, password, "clusteradmin")
	if err != nil {
		logger.Error(err, "Failed to apply operator user credentials")
		return err
	}

	logger.SetValues("user_name", cluster.GetUserClusterUsername())
	logger.Info("Ensuring admin user")
	adminSecret, err := r.SecretsService.GetAdminCredentials(ctx, cluster)
	if err != nil {
		return err
	}
	err = applyAdminUser(ctx, wekaService, adminSecret)
	if err != nil {
		logger.Error(err, "Failed to apply admin user credentials")
		return err
	}
	// the exact version applied here, so a rotation before the first sync is still applied by SyncAdminCredentials
	err = r.setAdminCredentialsCondition(ctx, metav1.ConditionTrue, "Synced", adminSecretVersion(adminSecret))
	if err != nil {
		return err
	}

	// change k8s secret to use proper account
	err = r.SecretsService.UpdateOperatorLoginSecret(ctx, cluster)
	if err != nil {
		return err
	}

	logger.Info("Cluster credentials applied")

	return nil
}

// SyncAdminCredentials applies admin Secret changes (password rotation, new username) to WEKA.
// The condition message holds the Secret's UID/resourceVersion, never password material.
func (r *wekaClusterReconcilerLoop) SyncAdminCredentials(ctx context.Context) error {
	err := r.syncAdminCredentials(ctx)
	// no condition: a cluster created before rotation sync, keep it unset so a later success only records the version
	if err == nil || meta.FindStatusCondition(r.cluster.Status.Conditions, condition.CondAdminCredentialsSynced) == nil {
		return err
	}
	// the step continues on error, so the condition is the only place a failed sync shows on the CR
	if updateErr := r.setAdminCredentialsCondition(ctx, metav1.ConditionFalse, "SyncFailed", err.Error()); updateErr != nil {
		return errors.Wrap(updateErr, err.Error())
	}
	return err
}

func (r *wekaClusterReconcilerLoop) syncAdminCredentials(ctx context.Context) error {
	secret, err := r.SecretsService.GetAdminCredentials(ctx, r.cluster)
	if err != nil {
		return err
	}
	version := adminSecretVersion(secret)
	// ApplyCredentials records the version it applied, so no condition means the cluster predates rotation sync
	// and its WEKA password may have been changed directly: record the current version without resetting it
	if meta.FindStatusCondition(r.cluster.Status.Conditions, condition.CondAdminCredentialsSynced) == nil {
		return r.setAdminCredentialsCondition(ctx, metav1.ConditionTrue, "Synced", version)
	}
	if getConditionConfigHash(r.cluster.Status.Conditions, condition.CondAdminCredentialsSynced) == version {
		return nil
	}

	container := discovery.SelectActiveContainer(r.containers)
	if container == nil {
		return errors.New("No active container found")
	}
	wekaService := services.NewWekaService(r.ExecService, container)

	if err := applyAdminUser(ctx, wekaService, secret); err != nil {
		return err
	}
	return r.setAdminCredentialsCondition(ctx, metav1.ConditionTrue, "Synced", version)
}

// applyAdminUser sets the Secret's password on an existing user instead of skipping it like EnsureUser,
// so WEKA always ends up with the version that gets recorded
func applyAdminUser(ctx context.Context, wekaService services.WekaService, secret *v1.Secret) error {
	username, password := string(secret.Data["username"]), string(secret.Data["password"])
	users, err := wekaService.GetUsers(ctx)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(users, func(u services.WekaUserResponse) bool { return u.Username == username }) {
		return wekaService.SetUserPassword(ctx, username, password)
	}
	// a renamed user is created alongside the old one, which is left in place
	return wekaService.EnsureUser(ctx, username, password, "clusteradmin")
}

func adminSecretVersion(secret *v1.Secret) string {
	return string(secret.UID) + "/" + secret.ResourceVersion
}

func (r *wekaClusterReconcilerLoop) setAdminCredentialsCondition(ctx context.Context, status metav1.ConditionStatus, reason, message string) error {
	previous := slices.Clone(r.cluster.Status.Conditions)
	if !meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
		Type:    condition.CondAdminCredentialsSynced,
		Status:  status,
		Reason:  reason,
		Message: message,
	}) {
		return nil
	}
	if err := r.getClient().Status().Update(ctx, r.cluster); err != nil {
		// an unpersisted condition must not count as recorded: SyncAdminCredentials uses its absence for upgraded clusters
		r.cluster.Status.Conditions = previous
		return err
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) EnsureLoginCredentials(ctx context.Context) error {
	return r.SecretsService.EnsureLoginCredentials(ctx, r.cluster)
}

func (r *wekaClusterReconcilerLoop) EnsureCsiLoginCredentials(ctx context.Context) error {
	cluster := r.cluster
	secret := &v1.Secret{}
	err := r.getClient().Get(ctx, client.ObjectKey{
		Name:      cluster.GetCsiSecretName(),
		Namespace: cluster.Namespace,
	}, secret)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
	}

	containers := discovery.SelectOperationalContainers(r.containers, 30, nil)
	endpoints := discovery.GetClusterEndpoints(ctx, containers, 30, cluster.Spec.CsiConfig)

	containerNums := allocator.GetWekaContainerNumbers(cluster.Spec.Dynamic)

	var nfsContainers []*weka.WekaContainer
	var nfsTargetIps []string
	var nfsTargetIpsBytes []byte
	var endpointsBytes []byte

	if containerNums.Nfs > 0 {
		nfsContainers = discovery.SelectOperationalContainers(r.containers, 30, []string{weka.WekaContainerModeNfs})
		nfsTargetIps = discovery.GetClusterNfsTargetIps(ctx, nfsContainers)
	}

	if len(nfsTargetIps) > 0 && nfsTargetIps[0] != "" {
		nfsTargetIpsBytes = []byte(strings.Join(nfsTargetIps, ","))
	}

	endpointsBytes = []byte(strings.Join(endpoints, ","))

	if secret.Data == nil {
		clientSecret := services.NewCsiSecret(ctx, cluster, endpoints, nfsTargetIps)

		kubeService := kubernetes.NewKubeService(r.getClient())
		return kubeService.EnsureSecret(ctx, clientSecret, &kubernetes.K8sOwnerRef{
			Scheme: r.getClient().Scheme(),
			Obj:    cluster,
		})
	} else {
		if len(nfsTargetIpsBytes) != 0 {
			secret.Data["nfsTargetIps"] = nfsTargetIpsBytes
		}
		secret.Data["endpoints"] = endpointsBytes
		return r.getClient().Update(ctx, secret)
	}
}

func (r *wekaClusterReconcilerLoop) applyClientLoginCredentials(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	cluster := r.cluster
	containers := r.containers

	container := discovery.SelectActiveContainer(containers)
	if container == nil {
		return errors.New("No active container found")
	}
	username, password, err := r.getUsernameAndPassword(ctx, cluster.Namespace, cluster.GetClientSecretName())
	if err != nil {
		err = fmt.Errorf("failed to get client login credentials: %w", err)
		return err
	}

	wekaService := services.NewWekaService(r.ExecService, container)
	err = wekaService.EnsureUser(ctx, username, password, "regular")
	if err != nil {
		logger.Error(err, "Failed to ensure user")
		return err
	}

	logger.SetStatus(codes.Ok, "Client login credentials applied")

	return nil
}

func (r *wekaClusterReconcilerLoop) applyCsiLoginCredentials(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "applyCsiLoginCredentials")
	defer logger.End()

	cluster := r.cluster
	containers := r.containers

	container := discovery.SelectActiveContainer(containers)
	if container == nil {
		return errors.New("No active container found")
	}

	username, password, err := r.getUsernameAndPassword(ctx, cluster.Namespace, cluster.GetCsiSecretName())
	if err != nil {
		err = fmt.Errorf("failed to get client login credentials: %w", err)
		return err
	}

	wekaService := services.NewWekaService(r.ExecService, container)
	err = wekaService.EnsureUser(ctx, username, password, "csi")
	if err != nil {
		logger.Error(err, "Failed to ensure user")
		return err
	}

	logger.SetStatus(codes.Ok, "CSI login credentials applied")

	return nil
}

func (r *wekaClusterReconcilerLoop) getUsernameAndPassword(ctx context.Context, namespace, secretName string) (username, password string, err error) {
	secret := &v1.Secret{}
	err = r.getClient().Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret)
	if err != nil {
		return "", "", err
	}
	usernameBytes := secret.Data["username"]
	passwordBytes := secret.Data["password"]

	return string(usernameBytes), string(passwordBytes), nil
}

func (r *wekaClusterReconcilerLoop) ensureClientLoginCredentials(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	cluster := r.cluster
	containers := r.containers

	activeContainer, err := discovery.SelectActiveContainerWithRole(ctx, containers, weka.WekaContainerModeDrive)
	if err != nil {
		return err
	}

	err = r.SecretsService.EnsureClientLoginCredentials(ctx, cluster, activeContainer)
	if err != nil {
		logger.Error(err, "Failed to apply client login credentials")
		return err
	}

	return nil
}
