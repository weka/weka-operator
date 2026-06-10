package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrl "sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/weka/go-lib/pkg/workers"
	"github.com/weka/go-steps-engine/lifecycle"
	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"
)

type WekaClusterService interface {
	GetCluster() *wekav1alpha1.WekaCluster
	FormCluster(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error
	EnsureNoContainers(ctx context.Context, mode string) error
	GetOwnedContainers(ctx context.Context, mode string) ([]*wekav1alpha1.WekaContainer, error)
}

func NewWekaClusterService(mgr ctrl.Manager, restClient rest.Interface, cluster *wekav1alpha1.WekaCluster) WekaClusterService {
	k8sClient := mgr.GetClient()
	config := mgr.GetConfig()
	return &wekaClusterService{
		Client:      k8sClient,
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
	ctx, logger := instrumentation.CreateLogSpan(ctx, "EnsureNoContainers", "cluster", r.Cluster.Name, "mode", mode)
	defer logger.End()

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
		if container.IsDestroyingState() {
			return nil
		}
		return SetContainerStateDestroying(ctx, container, r.Client)
	})

	if results.AsError() != nil {
		return results.AsError()
	}

	if len(containers) > 0 {
		err := fmt.Errorf("waiting for %d %s containers to be removed", len(containers), mode)
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	} else {
		return nil
	}
}

func (r *wekaClusterService) FormCluster(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "createCluster", "cluster", r.Cluster.Name, "containers", len(containers))
	defer logger.End()

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
		hostIps = append(hostIps, container.GetHostIps(nil)[0])
		hostnamesList = append(hostnamesList, container.Status.GetManagementIps()[0])
	}
	hostIpsStr := strings.Join(hostIps, ",")
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
	clusterName := r.Cluster.Name
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

	// Single-parity (parity==1) requires the allow_1_parity override to be present on the freshly
	// created, uninitialised cluster BEFORE the stripe is applied — weka rejects `--parity-drives 1`
	// with ProtectionNotAllowed otherwise. EnsureWekaOverrides runs post start-io (too late), so the
	// override is set here, between hot-spare and parity. Operator-gated by AllowSingleParity; QA/test
	// only (a single parity chunk leaves a stripe unprotected during rebuild).
	if globalconfig.Config.DriveSharing.AllowSingleParity && r.Cluster.Spec.RedundancyLevel > 0 && r.Cluster.Spec.RedundancyLevel < 2 {
		wekaSvc := NewWekaService(r.ExecService, containers[0])
		existing, lerr := wekaSvc.ListOverridesByKey(ctx, "allow_1_parity")
		if lerr != nil || len(existing) == 0 {
			logger.Info("Setting allow_1_parity override (single-parity protection)", "redundancyLevel", r.Cluster.Spec.RedundancyLevel)
			comment := fmt.Sprintf("weka-operator AllowSingleParity: enable single parity for cluster %s (parity=%d)", r.Cluster.Name, r.Cluster.Spec.RedundancyLevel)
			if err = wekaSvc.AddOverride(ctx, "allow_1_parity", "true", comment, true); err != nil {
				return errors.Wrapf(err, "Failed to set allow_1_parity override: %s", err.Error())
			}
		}
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
	ctx, spanLogger := instrumentation.CreateLogSpan(ctx, "GetClusterContainers", "cluster", r.Cluster.Name, "mode", mode, "cluster_uid", string(r.Cluster.UID))
	defer spanLogger.End()

	return discovery.GetOwnedContainers(ctx, r.Client, r.Cluster.UID, r.Cluster.Namespace, mode)
}
