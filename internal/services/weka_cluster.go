package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"

	"github.com/kr/pretty"
	"github.com/pkg/errors"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type WekaClusterService interface {
	GetCluster() *wekav1alpha1.WekaCluster
	FormCluster(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error
	EnsureNoContainers(ctx context.Context, mode string) error
	GetOwnedContainers(ctx context.Context, mode string) ([]*wekav1alpha1.WekaContainer, error)
}

func NewWekaClusterService(mgr ctrl.Manager, cluster *wekav1alpha1.WekaCluster) WekaClusterService {
	client := mgr.GetClient()
	config := mgr.GetConfig()
	return &wekaClusterService{
		Client:      client,
		ExecService: exec.NewExecService(config),
		Cluster:     cluster,
	}
}

type wekaClusterService struct {
	Client      client.Client
	ExecService exec.ExecService

	Cluster *wekav1alpha1.WekaCluster
}

func (r *wekaClusterService) GetCluster() *wekav1alpha1.WekaCluster {
	return r.Cluster
}

func (r *wekaClusterService) EnsureNoContainers(ctx context.Context, mode string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureNoContainers", "cluster", r.Cluster.Name, "mode", mode)
	defer end()

	containers, err := r.GetOwnedContainers(ctx, mode)
	if err != nil {
		logger.Error(err, "Failed to get owned containers")
		return err
	}

	for _, container := range containers {
		// delete object
		// check if not already being deleted
		if container.GetDeletionTimestamp() != nil {
			continue
		}
		err := r.Client.Delete(ctx, container)
		if err != nil {
			logger.Error(err, "Failed to delete container", "container", container.Name)
			return err
		}

	}

	if len(containers) != 0 {
		logger.Info("Deleted containers", "count", len(containers))
		return errors.New("containers being deleted")
	}
	return nil
}

func (r *wekaClusterService) FormCluster(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "createCluster", "cluster", r.Cluster.Name, "containers", len(containers))
	defer end()

	if len(containers) == 0 {
		err := pretty.Errorf("containers list is empty")
		logger.Error(err, "containers list is empty")
		return err
	}

	var hostIps []string
	var hostnamesList []string

	for _, container := range containers {
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeEnvoy {
			continue
		}
		if container.Spec.Ipv6 {
			hostIps = append(hostIps, fmt.Sprintf("[%s]:%d", container.Status.ManagementIP, container.GetPort()))
		} else {
			hostIps = append(hostIps, fmt.Sprintf("%s:%d", container.Status.ManagementIP, container.GetPort()))
		}
		hostnamesList = append(hostnamesList, container.Status.ManagementIP)
	}
	hostIpsStr := strings.Join(hostIps, ",")
	// cmd := fmt.Sprintf("weka status || weka cluster create %s --host-ips %s", strings.Join(hostnamesList, " "), hostIpsStr) // In general not supposed to pass join secret here, but it is broken on weka. Preserving this line for quick comment/uncomment cycles
	cmd := fmt.Sprintf("wekaauthcli status || weka cluster create %s --host-ips %s --join-secret=`cat /var/run/secrets/weka-operator/operator-user/join-secret` --admin-password `cat /var/run/secrets/weka-operator/operator-user/password`", strings.Join(hostnamesList, " "), hostIpsStr)
	logger.Info("Creating cluster", "cmd", cmd)

	executor, err := r.ExecService.GetExecutor(ctx, containers[0])
	if err != nil {
		logger.Error(err, "Could not create executor")
		return errors.Wrap(err, "Could not create executor")
	}
	stdout, stderr, err := executor.ExecNamed(ctx, "WekaStatusOrWekaClusterCreate", []string{"bash", "-ce", cmd})
	if err != nil {
		logger.Error(err, "Failed to create cluster")
		return errors.Wrapf(err, "Failed to create cluster: %s", stderr.String())
	}
	logger.Info("Cluster created", "stdout", stdout.String(), "stderr", stderr.String())

	// update cluster name
	clusterName := r.Cluster.ObjectMeta.Name
	cmd = fmt.Sprintf("wekaauthcli cluster update --cluster-name %s", clusterName)
	logger.Debug("Updating cluster name")
	_, stderr, err = executor.ExecNamed(ctx, "WekaClusterSetName", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to update cluster name: %s", stderr.String())
	}

	//cmd = fmt.Sprintf("weka cluster hot-spare 0")
	//logger.Debug("Disabling hot spare")
	//_, stderr, err = executor.ExecNamed(ctx, "WekaClusterSetHotSpare", []string{"bash", "-ce", cmd})
	//if err != nil {
	//	return errors.Wrapf(err, "Failed to disable hot spare: %s", stderr.String())
	//}

	if err := r.Client.Status().Update(ctx, r.Cluster); err != nil {
		return errors.Wrap(err, "Failed to update wekaCluster status")
	}

	logger.SetPhase("Cluster created")
	return nil
}

func (r *wekaClusterService) GetOwnedContainers(ctx context.Context, mode string) ([]*wekav1alpha1.WekaContainer, error) {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "GetClusterContainers", "cluster", r.Cluster.Name, "mode", mode, "cluster_uid", string(r.Cluster.UID))
	defer end()

	return discovery.GetOwnedContainers(ctx, r.Client, r.Cluster.UID, r.Cluster.Namespace, mode)
}

func (r *wekaClusterService) EnsureNoS3Containers(ctx context.Context) error {
	return r.EnsureNoContainers(ctx, wekav1alpha1.WekaContainerModeS3)
}
