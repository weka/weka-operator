package wekacluster

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	awslib "github.com/weka/weka-operator/internal/services/aws"
	"github.com/weka/weka-operator/internal/services/discovery"
)

const (
	// hookHeartbeatTimeoutSeconds is the HeartbeatTimeout set on the EC2_INSTANCE_TERMINATING hook.
	// AWS caps HeartbeatTimeout at 7200s; the per-container hold heartbeats hourly, well under it.
	hookHeartbeatTimeoutSeconds int32 = 7200

	// noAwsTerminationLifecycleHookReason / asgResolutionFailedReason are the Warning event reasons
	// emitted on the WekaCluster when the operator cannot ensure the ASG's termination hook.
	noAwsTerminationLifecycleHookReason = "NoAwsTerminationLifecycleHook"
	asgResolutionFailedReason           = "ASGResolutionFailed"

	// ensureRetryInterval throttles how often the operator re-attempts the AWS ensure calls
	// (DescribeInstance/PutLifecycleHook) for a cluster that still has un-ensured backend ASGs — e.g.
	// one blocked on missing IAM. The abort gate still fires every reconcile; only the AWS calls are
	// throttled to this interval. Trade-off: a stuck cluster self-heals within this window after the
	// cause (e.g. IAM) is fixed, rather than within a reconcile cycle. An operator restart clears the
	// in-memory timestamp for an immediate retry.
	ensureRetryInterval = 15 * time.Minute
)

var (
	// newClusterLifecycleClient builds a LifecycleClient for a region. Overridable in tests.
	newClusterLifecycleClient = awslib.NewLifecycleClient

	// verifiedHookNodes / verifiedHookASGs record the nodes and ASGs whose termination hook has been
	// confirmed good, for 6h. The TTL re-asserts the hook after it lapses (repairing IaC drift) while
	// keeping the steady state free of AWS calls.
	verifiedHookNodes = awslib.NewTTLSet(6 * time.Hour)
	verifiedHookASGs  = awslib.NewTTLSet(6 * time.Hour)

	// ensureAttemptMu guards lastEnsureAttempt.
	ensureAttemptMu sync.Mutex
	// lastEnsureAttempt records, per cluster UID, the last time the operator actually made AWS ensure
	// calls, so re-attempts for a not-yet-ensured cluster are throttled to ensureRetryInterval.
	lastEnsureAttempt = map[string]time.Time{}
)

// ensureAwsTerminationLifecycleHook ensures the weka-drive-drain EC2_INSTANCE_TERMINATING lifecycle
// hook exists on the ASG of every backend node in the cluster, so the per-container hold has a hook to
// drive on scale-down. It runs before FormCluster: on INITIAL provisioning (CondClusterCreated not yet
// True) a failure to ensure returns an error, aborting the flow so an unprotected cluster is never
// formed (self-heals on retry once e.g. IAM is fixed). On an already-formed cluster it FAILS OPEN —
// emits a Warning on the WekaCluster and returns nil, so a transient AWS/IAM problem never disrupts a
// running cluster. Non-AWS clusters are a no-op. The AWS API itself is the sole authority on ASG
// membership: DescribeInstance returning an empty asgName with no error is a FACT about the instance (e.g.
// Karpenter provisions instances directly via ec2:RunInstances and joins no ASG; so can EKS Auto Mode,
// Fargate, or Hybrid Nodes); it is logged and skipped rather than blocking cluster formation.
// Any other DescribeInstance/PutTerminationHook error (IAM denied, throttling, an AWS outage) is a
// FAILURE to look up a genuine ASG member and keeps the existing fail-closed hard error on initial
// provisioning — that is the actual data-loss guard and must never be weakened by a heuristic (a label,
// an instance-lifecycle field, etc.) that could false-negative on a real ASG-backed node. In-memory
// per-node/per-ASG TTL caches keep the steady state free of AWS calls and repair drift within the TTL.
func (loop *wekaClusterReconcilerLoop) ensureAwsTerminationLifecycleHook(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	initialProvisioning := !meta.IsStatusConditionTrue(loop.cluster.Status.Conditions, condition.CondClusterCreated)

	// Distinct backend node names.
	nodeNames := map[string]struct{}{}
	for _, c := range loop.containers {
		if c == nil || !c.IsBackend() {
			continue
		}
		nodeName := string(c.GetNodeAffinity())
		if nodeName == "" {
			// Backend not scheduled yet. During initial provisioning wait for it (we must ensure ALL
			// backend ASGs before forming); on a running cluster this is transient — skip.
			if initialProvisioning {
				return lifecycle.NewWaitError(errors.Errorf("waiting for backend container %s to be scheduled before ensuring its ASG lifecycle hook", c.Name))
			}
			continue
		}
		nodeNames[nodeName] = struct{}{}
	}

	// Backend nodes whose hook isn't confirmed good yet. Steady state hits the verified cache here and
	// returns without any AWS calls.
	pending := make([]string, 0, len(nodeNames))
	for nodeName := range nodeNames {
		if !verifiedHookNodes.Has(nodeName) {
			pending = append(pending, nodeName)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	// Throttle the AWS re-ensure to at most once per ensureRetryInterval per cluster, so a cluster that
	// can't be ensured (e.g. IAM denied) doesn't hit AWS every reconcile. The gate still holds every
	// reconcile: during initial provisioning we return a WaitError (FormCluster stays blocked and the
	// reconcile requeues when the next attempt is due); a running cluster fails open until then.
	clusterKey := string(loop.cluster.GetUID())
	ensureAttemptMu.Lock()
	wait := ensureRetryInterval - time.Since(lastEnsureAttempt[clusterKey])
	ensureAttemptMu.Unlock()
	if wait > 0 {
		if initialProvisioning {
			return lifecycle.NewWaitErrorWithDuration(errors.Errorf("throttled: waiting %s before re-ensuring the termination lifecycle hook for %d backend node(s)", wait.Round(time.Second), len(pending)), wait)
		}
		return nil // running cluster: fail open until the next attempt window
	}

	// About to make AWS calls — mark the attempt once (only after we reach real AWS work, so a
	// transient node read that never gets to AWS doesn't start the throttle window).
	markedAttempt := false
	markAttempt := func() {
		if markedAttempt {
			return
		}
		markedAttempt = true
		ensureAttemptMu.Lock()
		lastEnsureAttempt[clusterKey] = time.Now()
		ensureAttemptMu.Unlock()
	}

	for _, nodeName := range pending {
		node := &v1.Node{}
		if err := loop.getClient().Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
			if initialProvisioning {
				return lifecycle.NewWaitError(errors.Wrapf(err, "waiting to read node %s before ensuring its ASG lifecycle hook", nodeName))
			}
			logger.Info("failed to read node while ensuring termination lifecycle hook, skipping (fail-open)", "node", nodeName, "error", err.Error())
			continue
		}
		if discovery.ProviderFromID(node.Spec.ProviderID) != discovery.ProviderAWS {
			continue // non-AWS node
		}
		instanceID, region, ok := discovery.InstanceIDAndRegionFromProviderID(node.Spec.ProviderID)
		if !ok {
			continue
		}
		markAttempt() // reached real AWS work — start/refresh the throttle window
		asgClient := newClusterLifecycleClient(region)
		asgName, _, err := asgClient.DescribeInstance(ctx, instanceID)
		if err == nil && asgName == "" {
			// AWS answered successfully and reported no ASG for this instance (e.g. Karpenter provisions
			// instances directly from a NodeClaim), so no lifecycle hook can exist for it. This is a fact
			// about the node, not a transient failure — never block cluster formation on it. Drain safety
			// on these nodes rests on the provisioner's own graceful termination plus the operator's
			// do-not-force-delete-unsafe finalizer / eviction gate.
			logger.Info("backend node is not a member of any Auto Scaling group; skipping termination lifecycle hook", "node", nodeName)
			verifiedHookNodes.Mark(nodeName) // nothing to ensure here; re-checked after the 6h TTL
			continue
		}
		if err != nil {
			logger.Info("failed to resolve ASG while ensuring termination lifecycle hook", "node", nodeName, "error", err.Error())
			msg := fmt.Sprintf("could not resolve the Auto Scaling group for a backend node (%s) — cluster is in risk of data loss", awslib.APIErrorSummary(err))
			_ = loop.RecordEventThrottled(v1.EventTypeWarning, asgResolutionFailedReason, msg, 10*time.Minute) //nolint:errcheck // best-effort event
			if initialProvisioning {
				return errors.New(msg)
			}
			continue // running cluster: fail open
		}

		if !verifiedHookASGs.Has(asgName) {
			if err := asgClient.PutTerminationHook(ctx, asgName, awslib.LifecycleHookName, hookHeartbeatTimeoutSeconds); err != nil {
				logger.Info("failed to ensure termination lifecycle hook", "asg", asgName, "node", nodeName, "error", err.Error())
				msg := fmt.Sprintf("could not create the termination lifecycle hook %q on Auto Scaling group %q (%s) — cluster is in risk of data loss", awslib.LifecycleHookName, asgName, awslib.APIErrorSummary(err))
				_ = loop.RecordEventThrottled(v1.EventTypeWarning, noAwsTerminationLifecycleHookReason, msg, 10*time.Minute) //nolint:errcheck // best-effort event
				if initialProvisioning {
					return errors.New(msg)
				}
				continue // running cluster: fail open
			}
			logger.Info("ensured termination lifecycle hook", "asg", asgName, "hookName", awslib.LifecycleHookName, "node", nodeName)
			verifiedHookASGs.Mark(asgName)
		}
		verifiedHookNodes.Mark(nodeName)
	}

	return nil
}
