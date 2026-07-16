package wekacontainer

import (
	"context"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/go-weka-observability/instrumentation"

	"github.com/weka/weka-operator/internal/pkg/domain"
	awslib "github.com/weka/weka-operator/internal/services/aws"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

// terminatingLifecycleState is the ASG lifecycle state reported while the EC2_INSTANCE_TERMINATING
// hook is holding an instance.
const terminatingLifecycleState = "Terminating:Wait"

// lifecycleHeartbeatTimestamp is the key under WekaContainer Status.Timestamps used to throttle AWS
// ASG lifecycle-hook heartbeats (RecordLifecycleActionHeartbeat) to once per hour.
const lifecycleHeartbeatTimestamp = "lifecycleHeartbeat"

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

// releasedInstances records instances for which the operator has already completed its termination
// lifecycle action (CONTINUE), keyed by instanceID, for an hour. It stops the release path being
// re-run — and CompleteLifecycleAction re-called — on every reconcile while the instance lingers in
// Terminating:Wait behind another (non-operator) lifecycle hook; re-asserted once the TTL lapses.
var releasedInstances = awslib.NewTTLSet(time.Hour)

// NodeIsAwsProvider reports whether the container's node is an AWS (EC2) node. Used as a step
// predicate to gate the AWS-only lifecycle reconcile steps.
func (r *containerReconcilerLoop) NodeIsAwsProvider() bool {
	return r.node != nil && discovery.ProviderFromID(r.node.Spec.ProviderID) == discovery.ProviderAWS
}

// reconcileAwsTerminationLifecycle holds an AWS EC2 instance in Terminating:Wait (via an ASG
// EC2_INSTANCE_TERMINATING lifecycle hook) until every backend pod on the node has exited gracefully
// (pod status.phase == Succeeded), then releases the hook so the ASG can proceed with termination.
// The hold is node-level, not per-container: on a converged node the instance must not terminate while
// a compute/protocol pod is still shutting down, so the release waits for all backend pods on the node
// (see allBackendPodsOnNodeExited), not just this container's own drives. This replaces the old
// node-agent IMDS-based watcher, running centrally from the operator instead of per-node.
//
// It is a no-op unless: this is a backend container; the node is known and is on AWS; there is a
// local termination signal (node cordoned or pod terminating); and DescribeInstance reports the
// instance is actually held (Terminating:Wait). All AWS errors
// fail open (logged, reconcile continues) so a transient AWS outage never blocks the reconcile loop.
func (r *containerReconcilerLoop) reconcileAwsTerminationLifecycle(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	if !r.container.IsBackend() {
		return nil
	}
	if r.node == nil {
		return nil
	}
	if discovery.ProviderFromID(r.node.Spec.ProviderID) != discovery.ProviderAWS {
		return nil
	}
	hookName := awslib.LifecycleHookName

	localTerminationSignal := r.node.Spec.Unschedulable || (r.pod != nil && r.pod.DeletionTimestamp != nil)
	if !localTerminationSignal {
		return nil
	}

	instanceID, region, ok := discovery.InstanceIDAndRegionFromProviderID(r.node.Spec.ProviderID)
	if !ok {
		return nil
	}

	// Already completed our lifecycle action for this instance — nothing more to do. Skip the AWS
	// calls even though the instance may still sit in Terminating:Wait behind another hook we do not
	// manage; re-asserted after releasedInstanceTTL as a safety net.
	if releasedInstances.Has(instanceID) {
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

	exited, err := r.allBackendPodsOnNodeExited(ctx)
	if err != nil {
		// Could not determine the node-wide state — do NOT release (safe: keep holding). Retry next reconcile.
		logger.Info("failed to list backend pods on node for lifecycle release check, keeping hold (fail-open)",
			append(logFields, "error", err.Error())...)
		exited = false
	}

	if !exited {
		// HOLD: at least one backend pod on the node has not exited gracefully yet. Heartbeat, throttled to once/hour.
		last, hasLast := r.container.Status.Timestamps[lifecycleHeartbeatTimestamp]
		if hasLast && time.Since(last.Time) <= lifecycleHeartbeatThrottle {
			logger.V(1).Info("HOLD: backend pods not all exited, heartbeat skipped (throttled)", logFields...)
			return nil
		}

		if err := asgClient.RecordHeartbeat(ctx, hookName, asgName, instanceID); err != nil {
			logger.Info("HOLD: failed to record lifecycle heartbeat, will retry next reconcile (fail-open)",
				append(logFields, "error", err.Error())...)
			return nil
		}
		logger.Info("HOLD: recorded lifecycle heartbeat, backend pods not all exited", logFields...)

		if r.container.Status.Timestamps == nil {
			r.container.Status.Timestamps = make(map[string]metav1.Time)
		}
		r.container.Status.Timestamps[lifecycleHeartbeatTimestamp] = metav1.Time{Time: time.Now()}
		if err := r.Status().Update(ctx, r.container); err != nil {
			return err
		}
		return nil
	}

	// RELEASE: all backend pods on the node have exited gracefully, let the instance terminate.
	if err := asgClient.CompleteAction(ctx, hookName, asgName, instanceID, "CONTINUE"); err != nil {
		logger.Info("RELEASE: failed to complete lifecycle action, will retry next reconcile (fail-open)",
			append(logFields, "error", err.Error())...)
		return nil
	}
	logger.Info("RELEASE: all backend pods on node exited gracefully, completed lifecycle action (CONTINUE)", logFields...)
	releasedInstances.Mark(instanceID)

	return nil
}

// allBackendPodsOnNodeExited reports whether every backend pod on this node has exited gracefully,
// i.e. reached status.phase == Succeeded (weka shut down cleanly, exit 0). Pods already reaped drop
// out of the list — the operator strips the WekaFinalizer only after safe cleanup, so a reaped pod
// has necessarily exited gracefully — hence an empty result also counts as exited. Any backend pod
// still Running/Pending/Failed ⇒ not exited ⇒ the instance stays held.
func (r *containerReconcilerLoop) allBackendPodsOnNodeExited(ctx context.Context) (bool, error) {
	pods, err := r.KubeService.GetPods(ctx, kubernetes.GetPodsOptions{
		Node: r.node.Name,
		LabelsIn: map[string][]string{
			domain.WekaLabelMode: domain.ContainerModesBackend,
		},
	})
	if err != nil {
		return false, err
	}
	for i := range pods {
		if pods[i].Status.Phase != v1.PodSucceeded {
			return false, nil
		}
	}
	return true, nil
}
