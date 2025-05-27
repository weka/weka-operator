// package wekacluster contains the reconciliation logic for WekaCluster resources
package wekacluster

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/operations/csi"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

func (r *wekaClusterReconcilerLoop) ApplyCredentials(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()
	cluster := r.cluster
	containers := r.containers

	container := discovery.SelectActiveContainer(containers)
	if container == nil {
		return errors.New("No active container found")
	}
	wekaService := services.NewWekaService(r.ExecService, container)

	ensureUser := func(secretName string) error {
		// fetch secret from k8s
		username, password, err := r.getUsernameAndPassword(ctx, cluster.Namespace, secretName)
		if username == "admin" {
			// force switch back to operator user
			username = cluster.GetOperatorClusterUsername()
		}
		if err != nil {
			return err
		}
		err = wekaService.EnsureUser(ctx, username, password, "clusteradmin")
		if err != nil {
			return err
		}
		return nil
	}

	logger.WithValues("user_name", cluster.GetOperatorClusterUsername()).Info("Ensuring operator user")
	if err := ensureUser(cluster.GetOperatorSecretName()); err != nil {
		logger.Error(err, "Failed to apply operator user credentials")
		return err
	}

	logger.WithValues("user_name", cluster.GetUserClusterUsername()).Info("Ensuring admin user")
	if err := ensureUser(cluster.GetUserSecretName()); err != nil {
		logger.Error(err, "Failed to apply admin user credentials")
		return err
	}

	// change k8s secret to use proper account
	err := r.SecretsService.UpdateOperatorLoginSecret(ctx, cluster)
	if err != nil {
		return err
	}

	// Cannot delete admin at is used until we apply credential here, i.e creating new users
	// TODO: Delete later? How can we know that all pod updated their secrets? Or just, delay by 5 minutes? Docs says it's 1 minute

	//err := wekaService.EnsureNoUser(ctx, "admin")
	//if err != nil {
	//	return err
	//}
	logger.Info("Cluster credentials applied")

	return nil
}

func (r *wekaClusterReconcilerLoop) EnsureLoginCredentials(ctx context.Context) error {
	return r.SecretsService.EnsureLoginCredentials(ctx, r.cluster)
}

func (r *wekaClusterReconcilerLoop) EnsureCsiLoginCredentials(ctx context.Context) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster
	secret := &v1.Secret{}
	err := r.getClient().Get(ctx, client.ObjectKey{
		Name:      csi.GetCsiSecretName(cluster),
		Namespace: cluster.Namespace,
	}, secret)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
	}

	containers := discovery.SelectOperationalContainers(r.containers, 30, nil)
	endpoints := discovery.GetClusterEndpoints(ctx, containers, 30, cluster.Spec.CsiConfig)

	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		return errors.New("Failed to get template")
	}

	var nfsContainers []*weka.WekaContainer
	var nfsTargetIps []string
	nfsTargetIpsBytes := []byte{}
	endpointsBytes := []byte{}

	if template.NfsContainers != 0 {
		nfsContainers = discovery.SelectOperationalContainers(r.containers, 30, []string{weka.WekaContainerModeNfs})
		nfsTargetIps = discovery.GetClusterNfsTargetIps(ctx, nfsContainers)
	}

	if nfsTargetIps != nil && len(nfsTargetIps) > 0 && nfsTargetIps[0] != "" {
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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "applyClientLoginCredentials")
	defer end()

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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster
	containers := r.containers

	container := discovery.SelectActiveContainer(containers)
	if container == nil {
		return errors.New("No active container found")
	}
	username, password, err := r.getUsernameAndPassword(ctx, cluster.Namespace, csi.GetCsiSecretName(cluster))
	if err != nil {
		err = fmt.Errorf("failed to get client login credentials: %w", err)
		return err
	}

	wekaService := services.NewWekaService(r.ExecService, container)
	err = wekaService.EnsureUser(ctx, username, password, "clusteradmin")
	if err != nil {
		logger.Error(err, "Failed to ensure user")
		return err
	}
	logger.SetStatus(codes.Ok, "CSI login credentials applied")
	return nil
}

func (r *wekaClusterReconcilerLoop) getUsernameAndPassword(ctx context.Context, namespace string, secretName string) (string, string, error) {
	secret := &v1.Secret{}
	err := r.getClient().Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret)
	if err != nil {
		return "", "", err
	}
	username := secret.Data["username"]
	password := secret.Data["password"]
	return string(username), string(password), nil
}

func (r *wekaClusterReconcilerLoop) ensureClientLoginCredentials(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

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
