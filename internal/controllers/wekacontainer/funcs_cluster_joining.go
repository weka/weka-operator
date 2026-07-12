package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	"go.opentelemetry.io/otel/codes"

	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/pkg/util/podexec"
)

func (r *containerReconcilerLoop) reconcileClusterStatus(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	container := r.container
	pod := r.pod

	if r.pod == nil {
		return errors.New("Pod is not found")
	}

	if r.container.Status.ClusterContainerID != nil {
		return nil
	}

	timeout := time.Second * 15

	executor, err := podexec.NewExecInPodWithTimeout(r.RestClient, r.Manager.GetConfig(), pod, &timeout)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return err
	}

	containerName := container.Spec.WekaContainerName

	showAgentPortCmd := `cat /opt/weka/k8s-runtime/vars/agent_port`
	cmd := fmt.Sprintf("weka local run wapi -H localhost:$(%s)/jrpc -W container-get-identity --container-name %s --json", showAgentPortCmd, containerName)
	if container.Spec.JoinIps != nil {
		cmd = fmt.Sprintf("wekaauthcli local run wapi -H 127.0.0.1:$(%s)/jrpc -W container-get-identity --container-name %s --json", showAgentPortCmd, containerName)
	}

	stdout, _, err := executor.ExecNamed(ctx, "WekaLocalContainerGetIdentity", []string{"bash", "-ce", cmd})
	if err != nil {
		return lifecycle.NewWaitError(err)
	}
	logger.Debug("Parsing weka local container-get-identity")
	response := resources.WekaLocalContainerGetIdentityResponse{}
	err = json.Unmarshal(stdout.Bytes(), &response)
	if err != nil {
		logger.Error(err, "Error parsing weka local status")
		return lifecycle.NewWaitError(err)
	}

	waitDuration := time.Second * 15
	if !response.HasValue && response.Exception != nil {
		return lifecycle.NewWaitErrorWithDuration(errors.New(*response.Exception), waitDuration)
	}
	if response.Value == nil {
		return lifecycle.NewWaitErrorWithDuration(errors.New("no value in response from weka local container-get-identity"), waitDuration)
	}
	if response.Value.ClusterId == "" || response.Value.ClusterId == "00000000-0000-0000-0000-000000000000" {
		return lifecycle.NewWaitErrorWithDuration(errors.New("container is not part of the cluster yet"), waitDuration)
	}

	container.Status.ClusterContainerID = &response.Value.ContainerId
	container.Status.ClusterID = response.Value.ClusterId
	logger.InfoWithStatus(
		codes.Ok,
		"Cluster GUID and container ID are updated in WekaContainer status",
		"cluster_guid", response.Value.ClusterId,
		"container_id", response.Value.ContainerId,
	)
	if err := r.Status().Update(ctx, container); err != nil {
		return err
	}

	return nil
}

func (r *containerReconcilerLoop) setJoinIpsIfStuckInStemMode(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	container := r.container

	ownerRef := container.GetOwnerReferences()
	if len(ownerRef) == 0 {
		return errors.New("no owner references found")
	}

	owner := ownerRef[0]
	clusterGuid := string(owner.UID)

	clusterCreationTime, err := services.ClustersCachedInfo.GetClusterCreationTime(ctx, clusterGuid)
	if err != nil {
		// The cluster hasn't been created yet (or its creation time isn't cached). This is the
		// normal state for a founding container still waiting on cluster-create: there are no
		// join IPs to inject, so skip silently and let the reconcile proceed to applyCurrentImage.
		// This step now runs before applyCurrentImage, so we must NOT hard-fail here — that would
		// halt founding containers during creation.
		logger.Debug("cluster creation time unavailable yet, skipping join-ip injection", "error", err.Error())
		return nil
	}

	// if cluster creation time is more than 1 minute, set join ips in the container spec
	if time.Since(clusterCreationTime) > time.Minute {
		joinIps, _ := services.ClustersCachedInfo.GetJoinIps(ctx, clusterGuid, owner.Name, container.Namespace) //nolint:errcheck // error return value intentionally not checked
		if len(joinIps) > 0 {
			container.Spec.JoinIps = joinIps
			executor, err := r.ExecService.GetExecutor(ctx, r.container)
			if err != nil {
				return fmt.Errorf("error getting executor: %w", err)
			}
			// 1. Reconfigure container with new join ips
			cmd := []string{"weka", "local", "resources", "join-ips"}
			cmd = append(cmd, joinIps...)
			_, stderr, err := executor.ExecNamed(ctx, "WekaLocalResources", cmd)
			if err != nil {
				return fmt.Errorf("error executing weka local resources: %w, %s", err, stderr.String())
			}
			// 2. Apply local resources change
			_, stderr, err = executor.ExecNamed(ctx, "WekaLocalResourcesApply", []string{"weka", "local", "resources", "apply", "-f"})
			if err != nil {
				return fmt.Errorf("error executing weka local resources apply: %w, %s", err, stderr.String())
			}
			// 3. Restart container
			_, stderr, err = executor.ExecNamed(ctx, "WekaLocalRestart", []string{"weka", "local", "restart", "--force"})
			if err != nil {
				return fmt.Errorf("error executing weka local restart: %w, %s", err, stderr.String())
			}

			logger.Info("Setting join ips in the container spec", "join_ips", joinIps)
			if err := r.Update(ctx, container); err != nil {
				return fmt.Errorf("error updating container: %w", err)
			}
		}
	}

	return nil
}
