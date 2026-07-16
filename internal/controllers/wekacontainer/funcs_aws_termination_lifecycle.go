package wekacontainer

import (
	"context"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/go-weka-observability/instrumentation"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	awslib "github.com/weka/weka-operator/internal/services/aws"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// terminatingLifecycleState is the ASG lifecycle state reported while the EC2_INSTANCE_TERMINATING
// hook is holding an instance.
const terminatingLifecycleState = "Terminating:Wait"

// lifecycleHeartbeatThrottle is the minimum interval between RecordLifecycleActionHeartbeat calls
// for a given drive container. Must stay well under the ASG hook's registered HeartbeatTimeout
// (scripts/eks/register-lifecycle-hook.sh defaults to 2h) so heartbeats always land before it lapses.
const lifecycleHeartbeatThrottle = time.Hour

var (
	// lifecycleClientsMu guards lifecycleClients.
	lifecycleClientsMu sync.Mutex
	// lifecycleClients caches one awslib.LifecycleClient per AWS region, avoiding a fresh
	// awsconfig.LoadDefaultConfig on every reconcile of every drive container.
	lifecycleClients = map[string]awslib.LifecycleClient{}
	// newLifecycleClient builds a LifecycleClient for a region. Overridable in tests.
	newLifecycleClient = awslib.NewLifecycleClient
)

// getLifecycleClient returns the cached LifecycleClient for region, creating one if needed.
func getLifecycleClient(region string) awslib.LifecycleClient {
	lifecycleClientsMu.Lock()
	defer lifecycleClientsMu.Unlock()

	if c, ok := lifecycleClients[region]; ok {
		return c
	}
	c := newLifecycleClient(region)
	lifecycleClients[region] = c
	return c
}

// reconcileTerminationLifecycle holds an AWS EC2 instance in Terminating:Wait (via an ASG
// EC2_INSTANCE_TERMINATING lifecycle hook) until this drive WekaContainer's data has been rebuilt
// off it and it has been removed from the cluster (DrivesRemoved()), then releases the hook so the
// ASG can proceed with termination. This replaces the old node-agent IMDS-based watcher, running
// centrally from the operator instead of per-node.
//
// It is a no-op unless: this is a drive container; the node is known and is on AWS; a lifecycle
// hook name is configured; there is a local termination signal (node cordoned or pod terminating);
// and DescribeInstance reports the instance is actually held (Terminating:Wait). All AWS errors
// fail open (logged, reconcile continues) so a transient AWS outage never blocks the reconcile loop.
func (r *containerReconcilerLoop) reconcileTerminationLifecycle(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	if !r.container.IsDriveContainer() {
		return nil
	}
	if r.node == nil {
		return nil
	}
	if discovery.ProviderFromID(r.node.Spec.ProviderID) != discovery.ProviderAWS {
		return nil
	}
	hookName := config.Config.Aws.NodeLifecycleHookName
	if hookName == "" {
		return nil
	}

	localTerminationSignal := r.node.Spec.Unschedulable || (r.pod != nil && r.pod.DeletionTimestamp != nil)
	if !localTerminationSignal {
		return nil
	}

	instanceID, region, ok := discovery.InstanceIDAndRegionFromProviderID(r.node.Spec.ProviderID)
	if !ok {
		return nil
	}

	asgClient := getLifecycleClient(region)

	asgName, lifecycleState, err := asgClient.DescribeInstance(ctx, instanceID)
	if err != nil {
		logger.Info("failed to describe ASG instance for lifecycle hold, skipping this reconcile (fail-open)",
			"instanceID", instanceID, "node", r.node.Name, "error", err.Error())
		return nil
	}

	if lifecycleState != terminatingLifecycleState {
		// Not currently held by the hook, nothing to do.
		return nil
	}

	logFields := []interface{}{"instanceID", instanceID, "node", r.node.Name, "asg", asgName, "hookName", hookName}

	if !r.container.DrivesRemoved() {
		// HOLD: heartbeat, throttled to once/hour.
		last, hasLast := r.container.Status.Timestamps[consts.LifecycleHeartbeatTimestamp]
		if hasLast && time.Since(last.Time) <= lifecycleHeartbeatThrottle {
			logger.V(1).Info("HOLD: drive not yet removed, heartbeat skipped (throttled)", logFields...)
			return nil
		}

		if err := asgClient.RecordHeartbeat(ctx, hookName, asgName, instanceID); err != nil {
			logger.Info("HOLD: failed to record lifecycle heartbeat, will retry next reconcile (fail-open)",
				append(logFields, "error", err.Error())...)
			return nil
		}
		logger.Info("HOLD: recorded lifecycle heartbeat, drive not yet removed", logFields...)

		if r.container.Status.Timestamps == nil {
			r.container.Status.Timestamps = make(map[string]metav1.Time)
		}
		r.container.Status.Timestamps[consts.LifecycleHeartbeatTimestamp] = metav1.Time{Time: time.Now()}
		if err := r.Status().Update(ctx, r.container); err != nil {
			return err
		}
		return nil
	}

	// RELEASE: drive removed, let the instance terminate.
	if err := asgClient.CompleteAction(ctx, hookName, asgName, instanceID, "CONTINUE"); err != nil {
		logger.Info("RELEASE: failed to complete lifecycle action, will retry next reconcile (fail-open)",
			append(logFields, "error", err.Error())...)
		return nil
	}
	logger.Info("RELEASE: drive removed, completed lifecycle action (CONTINUE)", logFields...)

	return nil
}
