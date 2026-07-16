package node_agent

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
)

// lifecycleStateProvider abstracts IMDS lookups needed by the watcher, so the loop can be
// unit-tested with a fake without touching real IMDS.
type lifecycleStateProvider interface {
	// InstanceIdentity returns this node's EC2 instance-id and region.
	InstanceIdentity(ctx context.Context) (instanceID string, region string, err error)
	// TargetLifecycleState reads the IMDS autoscaling/target-lifecycle-state value. It is
	// expected to contain "Terminated" while a terminating lifecycle hook holds the instance.
	TargetLifecycleState(ctx context.Context) (string, error)
}

// asgClient abstracts the AWS Auto Scaling calls needed by the watcher.
type asgClient interface {
	DescribeInstanceASG(ctx context.Context, instanceID string) (asgName string, lifecycleState string, err error)
	RecordHeartbeat(ctx context.Context, hookName, asgName, instanceID string) error
	CompleteAction(ctx context.Context, hookName, asgName, instanceID, result string) error
}

// deactivationChecker abstracts the WEKA-native "is it safe to deactivate this node's drive"
// oracle so the watcher can be tested without a live cluster/exec.
type deactivationChecker interface {
	// Allowed returns true when it is currently safe (per WEKA) to lose this node's drive
	// failure domain. If there is nothing to protect on this node (no drive container), it
	// returns (true, nil) so the watcher completes immediately.
	Allowed(ctx context.Context) (bool, error)
}

// TerminationLifecycleWatcher holds an EKS EC2 instance in Terminating:Wait (via an ASG lifecycle hook)
// until WEKA reports it is safe to deactivate this node's drive — escaping a managed-node-group
// drain's ~15min ceiling.
type TerminationLifecycleWatcher struct {
	logger  logr.Logger
	states  lifecycleStateProvider
	asg     asgClient
	checker deactivationChecker

	hookName          string
	maxHold           time.Duration
	pollInterval      time.Duration
	heartbeatInterval time.Duration

	// now is injectable for deterministic max-hold testing.
	now func() time.Time
}

const (
	// lifecyclePollInterval is how often the watcher polls IMDS for a pending termination while
	// idle. Must be well under the hook's HeartbeatTimeout or the brief Terminating:Wait window
	// can be missed entirely.
	lifecyclePollInterval = 30 * time.Second
	// lifecycleHeartbeatInterval is the hold-loop cadence: how often the watcher heartbeats the ASG
	// lifecycle hook and re-checks whether the drive WekaContainer is gone. Must stay well under the
	// hook's HeartbeatTimeout (set at registration) so heartbeats land before it expires.
	lifecycleHeartbeatInterval = 30 * time.Second
	// lifecycleMaxHold is a client-side backstop on the total hold. AWS independently caps the hold
	// at min(48h, 100×HeartbeatTimeout) via the hook's GlobalTimeout; this mirrors that 48h ceiling,
	// so in practice the AWS-side cap (governed by the registered HeartbeatTimeout) is the real limit.
	lifecycleMaxHold = 48 * time.Hour
)

// NewTerminationLifecycleWatcher builds a watcher wired to real AWS/K8s/WEKA implementations. The hook name
// (the enable toggle) comes from config.Config.Aws.NodeLifecycleHookName; the poll/heartbeat/hold
// cadences are fixed constants. The watcher is a no-op unless a hook name is configured; Run
// guards on this internally.
func NewTerminationLifecycleWatcher(logger logr.Logger) *TerminationLifecycleWatcher {
	return &TerminationLifecycleWatcher{
		logger:            logger.WithName("lifecycle-watcher"),
		states:            &imdsStateProvider{},
		asg:               &realASGClient{},
		checker:           &driveContainerPresenceChecker{logger: logger.WithName("lifecycle-watcher")},
		hookName:          config.Config.Aws.NodeLifecycleHookName,
		maxHold:           lifecycleMaxHold,
		pollInterval:      lifecyclePollInterval,
		heartbeatInterval: lifecycleHeartbeatInterval,
		now:               time.Now,
	}
}

// Run is the watcher's main loop. It is a no-op (returns promptly) when no hook name is configured
// or when IMDS is unreachable (non-AWS cluster). It otherwise runs until ctx is cancelled.
func (w *TerminationLifecycleWatcher) Run(ctx context.Context) error {
	if w.hookName == "" {
		w.logger.V(1).Info("no lifecycle hook name configured, watcher is a no-op")
		return nil
	}

	instanceID, region, err := w.states.InstanceIdentity(ctx)
	if err != nil {
		w.logger.Info("could not resolve EC2 instance identity via IMDS, lifecycle watcher is a no-op (non-AWS cluster?)", "error", err.Error())
		return nil
	}
	w.logger.Info("lifecycle watcher started", "instanceID", instanceID, "region", region, "hookName", w.hookName, "maxHold", w.maxHold.String())

	// Inject the IMDS-resolved region into the real ASG client (see realASGClient.region).
	if rc, ok := w.asg.(*realASGClient); ok {
		rc.region = region
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if err := w.awaitPendingTermination(ctx); err != nil {
			// ctx cancelled.
			return nil
		}

		if err := w.hold(ctx, instanceID); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			// Back off before resuming idle detection. Otherwise, while the instance is still in
			// Terminating:Wait, awaitPendingTermination returns immediately and we hot-loop on the
			// same error (e.g. a transient AWS API failure) with no delay.
			w.logger.Error(err, "lifecycle hold loop returned an error, backing off before resuming idle detection")
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(w.heartbeatInterval):
			}
			continue
		}
		// hold() returned nil => our lifecycle hook was completed (CONTINUE). The instance is being
		// terminated, so the watcher's work for this node is done. Stop here instead of looping:
		// other termination hooks (e.g. a platform Terminate-LC-Hook) keep IMDS reporting
		// "Terminated", so re-detecting would re-complete an already-resolved action and error.
		w.logger.Info("lifecycle hook completed, watcher going idle (instance is terminating)")
		return nil
	}
}

// awaitPendingTermination polls IMDS at pollInterval until the target lifecycle state indicates a
// pending termination (returns nil), or ctx is cancelled (returns the ctx error).
func (w *TerminationLifecycleWatcher) awaitPendingTermination(ctx context.Context) error {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		state, err := w.states.TargetLifecycleState(ctx)
		if err != nil {
			w.logger.V(1).Info("failed to read target-lifecycle-state, will retry", "error", err.Error())
		} else if strings.Contains(state, "Terminated") {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// hold runs the heartbeat/deactivation-check loop while the instance sits in Terminating:Wait,
// releasing it (CompleteAction CONTINUE) once WEKA permits deactivation or maxHold is exceeded.
func (w *TerminationLifecycleWatcher) hold(ctx context.Context, instanceID string) error {
	asgName, _, err := w.asg.DescribeInstanceASG(ctx, instanceID)
	if err != nil {
		return errors.Wrap(err, "failed to resolve ASG for instance")
	}

	start := w.now()
	logger := w.logger.WithValues("asg", asgName, "instanceID", instanceID)
	logger.Info("instance entered Terminating:Wait, holding until this node's drive container is removed")

	ticker := time.NewTicker(w.heartbeatInterval)
	defer ticker.Stop()

	for {
		allowed, err := w.checker.Allowed(ctx)
		if err != nil {
			logger.Info("drive-container check failed, treating as still-present (fail closed)", "error", err.Error())
		}
		if allowed && err == nil {
			logger.Info("drive container removed, releasing lifecycle hook (CONTINUE)")
			return w.asg.CompleteAction(ctx, w.hookName, asgName, instanceID, "CONTINUE")
		}

		if w.now().Sub(start) > w.maxHold {
			logger.Info("max hold exceeded, releasing lifecycle hook (CONTINUE) before the drive container was removed", "maxHold", w.maxHold.String())
			return w.asg.CompleteAction(ctx, w.hookName, asgName, instanceID, "CONTINUE")
		}

		if err := w.asg.RecordHeartbeat(ctx, w.hookName, asgName, instanceID); err != nil {
			// Fails open: we keep retrying (better than giving up and letting AWS terminate), but a
			// persistent failure (bad IAM / wrong hook name) means the hook's HeartbeatTimeout lapses
			// and AWS terminates this instance mid-drain. Log at Error so it is not silent.
			logger.Error(err, "failed to record lifecycle heartbeat; hold is failing open")
		} else {
			logger.V(1).Info("recorded lifecycle heartbeat, drive container still present")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// --- real implementations ---

type imdsStateProvider struct {
	client *imds.Client
}

func (p *imdsStateProvider) ensureClient(ctx context.Context) (*imds.Client, error) {
	if p.client != nil {
		return p.client, nil
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load AWS config")
	}
	p.client = imds.NewFromConfig(cfg)
	return p.client, nil
}

func (p *imdsStateProvider) InstanceIdentity(ctx context.Context) (instanceID, region string, err error) {
	c, err := p.ensureClient(ctx)
	if err != nil {
		return "", "", err
	}
	doc, err := c.GetInstanceIdentityDocument(ctx, &imds.GetInstanceIdentityDocumentInput{})
	if err != nil {
		return "", "", errors.Wrap(err, "failed to get instance identity document from IMDS")
	}
	return doc.InstanceID, doc.Region, nil
}

func (p *imdsStateProvider) TargetLifecycleState(ctx context.Context) (string, error) {
	c, err := p.ensureClient(ctx)
	if err != nil {
		return "", err
	}
	out, err := c.GetMetadata(ctx, &imds.GetMetadataInput{Path: "autoscaling/target-lifecycle-state"})
	if err != nil {
		return "", errors.Wrap(err, "failed to get target-lifecycle-state from IMDS")
	}
	defer out.Content.Close() //nolint:errcheck // error return value intentionally not checked
	body, err := io.ReadAll(out.Content)
	if err != nil {
		return "", errors.Wrap(err, "failed to read target-lifecycle-state body")
	}
	return strings.TrimSpace(string(body)), nil
}

type realASGClient struct {
	// region is resolved from IMDS by the watcher and injected before first use. In-pod there is
	// no AWS_REGION env and the default region provider does not reliably resolve from IMDS, so we
	// must pass it explicitly or the autoscaling client fails with "Missing Region".
	region string
	client *autoscaling.Client
}

func (a *realASGClient) ensureClient(ctx context.Context) (*autoscaling.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if a.region != "" {
		opts = append(opts, awsconfig.WithRegion(a.region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load AWS config")
	}
	a.client = autoscaling.NewFromConfig(cfg)
	return a.client, nil
}

func (a *realASGClient) DescribeInstanceASG(ctx context.Context, instanceID string) (asgName, lifecycleState string, err error) {
	c, err := a.ensureClient(ctx)
	if err != nil {
		return "", "", err
	}
	out, err := c.DescribeAutoScalingInstances(ctx, &autoscaling.DescribeAutoScalingInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return "", "", errors.Wrap(err, "DescribeAutoScalingInstances failed")
	}
	if len(out.AutoScalingInstances) == 0 {
		return "", "", errors.Errorf("no ASG instance found for instance-id %s", instanceID)
	}
	inst := out.AutoScalingInstances[0]
	return aws.ToString(inst.AutoScalingGroupName), aws.ToString(inst.LifecycleState), nil
}

func (a *realASGClient) RecordHeartbeat(ctx context.Context, hookName, asgName, instanceID string) error {
	c, err := a.ensureClient(ctx)
	if err != nil {
		return err
	}
	_, err = c.RecordLifecycleActionHeartbeat(ctx, &autoscaling.RecordLifecycleActionHeartbeatInput{
		LifecycleHookName:    aws.String(hookName),
		AutoScalingGroupName: aws.String(asgName),
		InstanceId:           aws.String(instanceID),
	})
	return errors.Wrap(err, "RecordLifecycleActionHeartbeat failed")
}

func (a *realASGClient) CompleteAction(ctx context.Context, hookName, asgName, instanceID, result string) error {
	c, err := a.ensureClient(ctx)
	if err != nil {
		return err
	}
	_, err = c.CompleteLifecycleAction(ctx, &autoscaling.CompleteLifecycleActionInput{
		LifecycleHookName:     aws.String(hookName),
		AutoScalingGroupName:  aws.String(asgName),
		InstanceId:            aws.String(instanceID),
		LifecycleActionResult: aws.String(result),
	})
	if err != nil && strings.Contains(err.Error(), "No active Lifecycle Action found") {
		// Idempotent: our hook was already resolved (completed earlier, or it timed out). Not an error.
		return nil
	}
	return errors.Wrap(err, "CompleteLifecycleAction failed")
}

// driveContainerPresenceChecker decides whether it is safe to permit this node's EC2 instance to
// terminate. It holds (heartbeats, does not permit termination) for as long as this node's drive
// WekaContainer CR still exists, and permits termination the moment that CR is gone.
//
// The CR is the right signal (not the pod): the operator only removes the WekaContainer — clearing
// its finalizer — after the drive has been fully deactivated and removed from the WEKA cluster,
// i.e. its data has been rebuilt onto the surviving drives. The backing pod, by contrast,
// disappears as soon as the local weka process finishes its SIGTERM shutdown — long before the
// cluster-side rebuild completes — so gating on the pod releases the instance (and drops the
// physical drive) mid-rebuild. Holding until the CR is gone keeps the drive present throughout the
// rebuild so WEKA can actually complete it. There is no cross-node coordination or lease here;
// WEKA's own removal safety (it refuses to remove a drive that would breach protection) paces how
// fast the CRs — and thus the instances — are released.
type driveContainerPresenceChecker struct {
	logger logr.Logger

	k8sClient client.Client
}

func (c *driveContainerPresenceChecker) ensureK8sClient() error {
	if c.k8sClient != nil {
		return nil
	}
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return errors.Wrap(err, "failed to load in-cluster config")
	}
	k8sClient, err := client.New(restConfig, client.Options{Scheme: watcherScheme()})
	if err != nil {
		return errors.Wrap(err, "failed to build in-cluster client")
	}
	c.k8sClient = k8sClient
	return nil
}

func (c *driveContainerPresenceChecker) Allowed(ctx context.Context) (bool, error) {
	if err := c.ensureK8sClient(); err != nil {
		return false, err
	}

	nodeName := config.Config.MetricsServerEnv.NodeName
	// Lists all WekaContainers cluster-wide. This runs only while this instance is actively held
	// (about once per heartbeat interval during a drain), so the unfiltered list is acceptable.
	var containers wekav1alpha1.WekaContainerList
	if err := c.k8sClient.List(ctx, &containers); err != nil {
		return false, errors.Wrap(err, "failed to list WekaContainers")
	}

	// Find this node's drive container by node affinity. Its WekaContainer CR stays present (even
	// while Deleting) until the operator finishes deactivation+removal and clears the finalizer.
	var driveContainer *wekav1alpha1.WekaContainer
	for i := range containers.Items {
		item := &containers.Items[i]
		if item.IsDriveContainer() && string(item.GetNodeAffinity()) == nodeName {
			driveContainer = item
			break
		}
	}
	if driveContainer == nil {
		// The drive container CR is gone (fully deactivated + removed) — nothing left to protect.
		return true, nil
	}

	c.logger.Info("holding: drive container still present, waiting before allowing node termination",
		"driveContainer", driveContainer.GetName())
	return false, nil
}

// watcherScheme returns a runtime scheme with core + weka types registered, sufficient for
// listing WekaContainers via the controller-runtime client.
func watcherScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(wekav1alpha1.AddToScheme(s))
	return s
}
