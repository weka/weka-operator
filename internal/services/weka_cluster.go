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

// postInitFrozenMsg is the invariant part of weka's refusal to change a formation-time setting once
// the cluster has been initialized. The rest of the sentence varies per setting, so only this half is
// matched. Both observed forms (exit 50):
//
//	--parity-drives / --data-drives:
//	  "Clustering operation failed: Can't change RAID drives configuration after the cluster has
//	   been initialized - you'll need to factory reset all the hosts"
//	--bucket-raft-size:
//	  "Clustering operation failed: Can't change Raft size configuration after the cluster has
//	   been initialized"
//
// Deliberately excludes the leading "Can't change": that fragment carries an ASCII apostrophe, so a
// weka release switching to a typographic one would silently stop matching — and the failure mode of
// a missed match is exactly the outage this guards against. This half is already specific to
// post-initialization immutability, and TestIsPostInitFrozenErr pins both real messages plus the
// near-misses that must NOT be tolerated.
const postInitFrozenMsg = "after the cluster has been initialized"

// isPostInitFrozenErr reports whether a `weka cluster update` failure is weka refusing to change a
// formation-time setting because the cluster is already initialized, rather than a real problem with
// the requested value.
//
// Matched on the message rather than the exit code: exit 50 is the generic "Clustering operation
// failed" class, so tolerating every 50 would also swallow a rejected value or an auth failure. This
// mirrors how the rest of the weka services tolerate expected CLI failures (see the
// "already part of the cluster" / "already configured" checks in weka.go).
//
// Matched on a substring rather than a whole sentence because the wording differs per setting:
// --parity-drives says "RAID drives configuration" while --bucket-raft-size says "Raft size
// configuration", and an earlier matcher pinned to the former silently failed to cover the latter,
// wedging a cluster on --bucket-raft-size instead.
//
// stderr only: PodExec.exec populates both buffers and returns them alongside the wrapped ExitError,
// and weka writes these refusals to stderr (verified against a live cluster).
func isPostInitFrozenErr(stderr string) bool {
	return strings.Contains(stderr, postInitFrozenMsg)
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

	sw, rl, hs := globalconfig.Config.DriveSharing.EffectiveProtection(r.Cluster.Spec.StripeWidth, r.Cluster.Spec.RedundancyLevel, r.Cluster.Spec.HotSpare)

	cmd = fmt.Sprintf("weka cluster hot-spare %d", hs)
	logger.Debug("Setting hot-spare", "hotSpare", hs)
	_, stderr, err = executor.ExecNamed(ctx, "WekaClusterSetHotSpare", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to set hot spare: %s", stderr.String())
	}

	// Single-parity (parity==1) requires the allow_1_parity override to be present on the freshly
	// created, uninitialised cluster BEFORE the stripe is applied — weka rejects `--parity-drives 1`
	// with ProtectionNotAllowed otherwise. EnsureWekaOverrides runs post start-io (too late), so the
	// override is set here, between hot-spare and parity. Operator-gated by AllowSingleParity; QA/test
	// only (a single parity chunk leaves a stripe unprotected during rebuild).
	if globalconfig.Config.DriveSharing.AllowSingleParity && rl > 0 && rl < 2 {
		wekaSvc := NewWekaService(r.ExecService, containers[0])
		existing, lerr := wekaSvc.ListOverridesByKey(ctx, "allow_1_parity")
		if lerr != nil || len(existing) == 0 {
			logger.Info("Setting allow_1_parity override (single-parity protection)", "redundancyLevel", rl)
			comment := fmt.Sprintf("weka-operator AllowSingleParity: enable single parity for cluster %s (parity=%d)", r.Cluster.Name, rl)
			if err = wekaSvc.AddOverride(ctx, "allow_1_parity", "true", comment, true); err != nil {
				return errors.Wrapf(err, "Failed to set allow_1_parity override: %s", err.Error())
			}
		}
	}

	// applyFrozenTolerantUpdate applies one `weka cluster update <flag> <value>` that weka only accepts
	// while the cluster is uninitialized. A refusal because the config is already frozen is expected on
	// any re-run against a live cluster and is logged rather than failed; every other failure is
	// returned. desc keeps the original human-readable wording in the error.
	applyFrozenTolerantUpdate := func(spanName, desc, flag string, value int) error {
		logger.Debug("Setting " + desc)
		updateCmd := fmt.Sprintf("weka cluster update %s %d", flag, value)
		_, updateStderr, updateErr := executor.ExecNamed(ctx, spanName, []string{"bash", "-ce", updateCmd})
		if updateErr == nil {
			return nil
		}
		if !isPostInitFrozenErr(updateStderr.String()) {
			return errors.Wrapf(updateErr, "Failed to set %s (%s): %s", desc, flag, updateStderr.String())
		}
		// Warn, not Info: this is the one branch where the operator knowingly leaves spec unapplied.
		// That is a persistent, human-actionable divergence rather than routine progress, and Info
		// risks it being invisible in a busy reconcile log.
		logger.Warn("cluster already initialized; spec value cannot be applied, keeping live configuration",
			"setting", desc, "flag", flag, "specValue", value, "stderr", updateStderr.String())
		return nil
	}

	if rl != 0 {
		if updateErr := applyFrozenTolerantUpdate("WekaClusterSetParityDrives", "redundancy level", "--parity-drives", rl); updateErr != nil {
			return updateErr
		}
	}

	if sw != 0 {
		if updateErr := applyFrozenTolerantUpdate("WekaClusterSetDataDrives", "stripe width", "--data-drives", sw); updateErr != nil {
			return updateErr
		}
	}

	// --bucket-raft-size is frozen post-init too, and confirmed to need the same tolerance: on a
	// cluster that sets spec.bucketRaftSize and loses CondClusterCreated, this is where the reconcile
	// wedges once parity and stripe are tolerated. weka words the refusal differently here ("Raft size
	// configuration" vs "RAID drives configuration"), which is why isPostInitFrozenErr matches the
	// invariant substrings instead of one exact sentence.
	if r.Cluster.Spec.BucketRaftSize != nil {
		if updateErr := applyFrozenTolerantUpdate("WekaClusterSetBucketRaftSize", "bucket raft size", "--bucket-raft-size", *r.Cluster.Spec.BucketRaftSize); updateErr != nil {
			return updateErr
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
