package wekacontainer

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	"go.opentelemetry.io/otel/codes"

	"github.com/weka/weka-operator/internal/node_agent"
	"github.com/weka/weka-operator/internal/services"
)

var errNotJoined = errors.New("container is not part of the cluster yet")

func (r *containerReconcilerLoop) reconcileClusterStatus(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	container := r.container

	if r.pod == nil {
		return errors.New("Pod is not found")
	}

	if r.container.Status.ClusterContainerID != nil {
		return nil
	}

	waitDuration := time.Second * 15

	agentPod, err := r.GetNodeAgentPod(ctx, container.GetNodeAffinity())
	if err != nil {
		return lifecycle.NewWaitError(err)
	}

	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		return lifecycle.NewWaitError(err)
	}

	var identity struct {
		ClusterID   string `json:"cluster_guid"`
		ContainerID int    `json:"host_id"`
	}
	err = node_agent.ProxyJSONRPC(ctx, agentPod, token, node_agent.JSONRPCProxyPayload{
		ContainerId:       string(container.GetUID()),
		WekaContainerName: container.Spec.WekaContainerName,
		Method:            "cluster_identity",
		Params:            map[string]any{},
	}, &identity)
	var jrpcErr *node_agent.JSONRPCError
	if errors.As(err, &jrpcErr) && jrpcErr.IsException("NodeIsStemException") ||
		err == nil && (identity.ClusterID == "" || identity.ClusterID == "00000000-0000-0000-0000-000000000000") {
		return lifecycle.NewWaitErrorWithDuration(errNotJoined, waitDuration)
	}
	if err != nil {
		return lifecycle.NewWaitError(err)
	}

	container.Status.ClusterContainerID = &identity.ContainerID
	container.Status.ClusterID = identity.ClusterID
	logger.InfoWithStatus(
		codes.Ok,
		"Cluster GUID and container ID are updated in WekaContainer status",
		"cluster_guid", identity.ClusterID,
		"container_id", identity.ContainerID,
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
