package services

import (
	"context"
	"encoding/json"
	"fmt"
	"k8s.io/client-go/rest"
	"strings"

	"github.com/weka/weka-operator/internal/pkg/lifecycle"

	"github.com/weka/weka-operator/pkg/workers"

	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type WekaClusterService interface {
	GetCluster() *wekav1alpha1.WekaCluster
	FormCluster(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error
	EnsureNoContainers(ctx context.Context, mode string) error
	GetOwnedContainers(ctx context.Context, mode string) ([]*wekav1alpha1.WekaContainer, error)
}

func NewWekaClusterService(mgr ctrl.Manager, restClient rest.Interface, cluster *wekav1alpha1.WekaCluster) WekaClusterService {
	client := mgr.GetClient()
	config := mgr.GetConfig()
	return &wekaClusterService{
		Client:      client,
		ExecService: exec.NewExecService(restClient, config),
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

	toDelete := []*wekav1alpha1.WekaContainer{}
	for _, container := range containers {
		if container.IsDestroyingState() {
			continue
		} else {
			toDelete = append(toDelete, container)
		}
	}

	results := workers.ProcessConcurrently(ctx, toDelete, 32, func(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
		if !container.IsDestroyingState() {
			patch := map[string]interface{}{
				"spec": map[string]interface{}{
					"state": wekav1alpha1.ContainerStateDestroying,
				},
			}

			patchBytes, err := json.Marshal(patch)
			if err != nil {
				return err
			}

			err = r.Client.Patch(ctx, container, client.RawPatch(types.MergePatchType, patchBytes))
			if err != nil {
				return err
			}
		}
		return nil
	})

	if results.AsError() != nil {
		return results.AsError()
	}

	if len(containers) > 0 {
		return lifecycle.NewWaitError(fmt.Errorf("not all containers were fully removed"))
	} else {
		return nil
	}
}

func (r *wekaClusterService) FormCluster(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "createCluster", "cluster", r.Cluster.Name, "containers", len(containers))
	defer end()

	if len(containers) == 0 {
		err := errors.New("cannot form cluster with no containers")
		logger.Error(err, "containers list is empty")
		return err
	}

	var hostIps []string
	var hostnamesList []string

	for _, container := range containers {
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeEnvoy {
			continue
		}
		hostIps = append(hostIps, container.GetHostIps()[0])
		hostnamesList = append(hostnamesList, container.Status.GetManagementIps()[0])
	}
	hostIpsStr := strings.Join(hostIps, ",")
	//cmd := fmt.Sprintf("weka status || weka cluster create %s --host-ips %s", strings.Join(hostnamesList, " "), hostIpsStr) // In general not supposed to pass join secret here, but it is broken on weka. Preserving this line for quick comment/uncomment cycles
	leadershipSizeStr := ""
	if r.Cluster.Spec.LeadershipSize != nil {
		leadershipSizeStr = fmt.Sprintf("--leadership-size %d", *r.Cluster.Spec.LeadershipSize)
	}
	cmd := fmt.Sprintf("weka status || weka cluster create %s --host-ips %s --join-secret=`cat /var/run/secrets/weka-operator/operator-user/join-secret` --admin-password `cat /var/run/secrets/weka-operator/operator-user/password` %s", strings.Join(hostnamesList, " "), hostIpsStr, leadershipSizeStr)
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
	cmd = fmt.Sprintf("weka cluster update --cluster-name %s", clusterName)
	logger.Debug("Updating cluster name")
	_, stderr, err = executor.ExecNamed(ctx, "WekaClusterSetName", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to update cluster name: %s", stderr.String())
	}

	cmd = fmt.Sprintf("weka cluster hot-spare %d", r.Cluster.Spec.HotSpare)
	logger.Debug("Setting hot-spare", "hotSpare", r.Cluster.Spec.HotSpare)
	_, stderr, err = executor.ExecNamed(ctx, "WekaClusterSetHotSpare", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to set hot spare: %s", stderr.String())
	}

	if r.Cluster.Spec.RedundancyLevel != 0 {
		logger.Debug("Setting parity drives")
		cmd = fmt.Sprintf("weka cluster update --parity-drives %d", r.Cluster.Spec.RedundancyLevel)
		_, stderr, err = executor.ExecNamed(ctx, "WekaClusterSetParityDrives", []string{"bash", "-ce", cmd})
		if err != nil {
			return errors.Wrapf(err, "Failed to set redundancy level (--parity-drives): %s", stderr.String())
		}
	}

	if r.Cluster.Spec.StripeWidth != 0 {
		logger.Debug("Setting data drives")
		cmd = fmt.Sprintf("weka cluster update --data-drives %d", r.Cluster.Spec.StripeWidth)
		_, stderr, err = executor.ExecNamed(ctx, "WekaClusterSetDataDrives", []string{"bash", "-ce", cmd})
		if err != nil {
			return errors.Wrapf(err, "Failed to set stripe width (--data-drives): %s", stderr.String())
		}
	}

	if r.Cluster.Spec.BucketRaftSize != nil {
		logger.Debug("Setting bucket raft size")
		cmd = fmt.Sprintf("weka cluster update --bucket-raft-size %d", *r.Cluster.Spec.BucketRaftSize)
		_, stderr, err = executor.ExecNamed(ctx, "WekaClusterSetBucketRaftSize", []string{"bash", "-ce", cmd})
		if err != nil {
			return errors.Wrapf(err, "Failed to set bucket raft size (--bucket-raft-size): %s", stderr.String())
		}
	}

	if r.Cluster.Spec.GetOverrides().ForceAio {
		cmd = "weka debug config override clusterInfo.nvmeEnabled false"
		_, stderr, err = executor.ExecNamed(ctx, "WekaClusterSetForceAio", []string{"bash", "-ce", cmd})
		if err != nil {
			return errors.Wrapf(err, "Failed to set force aio: %s", stderr.String())
		}
	}

	cmd = "weka debug override list | grep authenticate_client_join || weka debug override add --key authenticate_client_join || weka debug override add --key authenticate_client_join --force" //
	_, stderr, err = executor.ExecNamed(ctx, "WekaClusterSetAuthenticateClientJoin", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to set authenticate client join: %s", stderr.String())
	}

	if err := r.Client.Status().Update(ctx, r.Cluster); err != nil {
		return errors.Wrap(err, "Failed to update wekaCluster status")
	}

	logger.Info("Cluster created")
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

func (r *wekaClusterService) EnsureNoNfsContainers(ctx context.Context) error {
	return r.EnsureNoContainers(ctx, wekav1alpha1.WekaContainerModeNfs)
}
