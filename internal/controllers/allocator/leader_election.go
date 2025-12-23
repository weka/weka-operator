// Package allocator provides leader election for per-node resource allocation
package allocator

import (
	"context"
	"fmt"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coordinationv1 "k8s.io/client-go/kubernetes/typed/coordination/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"github.com/weka/weka-operator/pkg/util"
)

const (
	// LeaseDuration is how long the lease is valid before it expires
	LeaseDuration = 15 * time.Second

	// RenewDeadline is how long the holder has to renew the lease
	RenewDeadline = 10 * time.Second

	// RetryPeriod is how often a non-leader will retry acquiring the lease
	RetryPeriod = 2 * time.Second
)

// NodeAllocationLock provides leader election for per-node resource allocation
// It ensures only one WekaContainer allocates resources on a node at a time
//
// All containers on the same node (regardless of namespace) compete for the same lease.
// This prevents resource conflicts since drives and ports are node-level resources.
type NodeAllocationLock struct {
	restConfig *rest.Config
	nodeName   string
	identity   string // Container identity: namespace/name
	namespace  string // Container's namespace (for logging)
}

// NewNodeAllocationLock creates a new leader election lock for a specific node
//
// All WekaContainers allocating on the same node compete for the same lease:
//   - Lease name: "weka-allocator-{nodeName}"
//   - Lease namespace: "weka-operator-system" (cluster-wide)
//   - Identity: "{containerNamespace}/{containerName}"
//
// Example:
//   - default/backend-0 on node-1 → competes for "weka-operator-system/weka-allocator-node-1"
//   - other-ns/backend-1 on node-1 → competes for same lease
//   - Only one acquires at a time
func NewNodeAllocationLock(restConfig *rest.Config, namespace, nodeName, containerName string) *NodeAllocationLock {
	return &NodeAllocationLock{
		restConfig: restConfig,
		nodeName:   nodeName,
		identity:   fmt.Sprintf("%s/%s", namespace, containerName),
		namespace:  namespace,
	}
}

// RunWithLease executes the given function while holding the node allocation lease
//
// This ensures only one container at a time allocates resources on this node,
// preventing race conditions and resource conflicts.
//
// The function blocks until:
//  1. The lease is acquired and the function completes (returns result)
//  2. The context is cancelled/times out (returns timeout error)
//
// If the container crashes while holding the lease, Kubernetes automatically
// expires it after LeaseDuration (15s), allowing others to proceed.
//
// Example usage:
//
//	lock := NewNodeAllocationLock(restConfig, "default", "node-1", "container-1")
//	err := lock.RunWithLease(ctx, func(ctx context.Context) error {
//	    // Your allocation logic here - only runs when lease is held
//	    return allocateResources(ctx)
//	})
func (l *NodeAllocationLock) RunWithLease(ctx context.Context, fn func(context.Context) error) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "NodeAllocationLock.RunWithLease")
	defer end()

	leaseName := fmt.Sprintf("weka-allocator-%s", l.nodeName)

	logger.Info("Attempting to acquire allocation lease",
		"node", l.nodeName,
		"lease", leaseName,
		"identity", l.identity)

	// Create Kubernetes clients for lease operations
	coordinationClient, err := coordinationv1.NewForConfig(l.restConfig)
	if err != nil {
		return fmt.Errorf("failed to create coordination client: %w", err)
	}

	coreClient, err := corev1.NewForConfig(l.restConfig)
	if err != nil {
		return fmt.Errorf("failed to create core client: %w", err)
	}

	// Channel to communicate result from leader callback
	resultChan := make(chan error, 1)
	runCompleted := make(chan struct{})

	operatorNamespace, err := util.GetPodNamespace()
	if err != nil {
		return fmt.Errorf("failed to get operator namespace: %w", err)
	}

	// Create Lease-based resource lock
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      leaseName,
			Namespace: operatorNamespace,
		},
		Client: coordinationClient,
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: l.identity,
		},
	}

	// Fallback to ConfigMap lock if needed (for older K8s versions)
	// But prefer Lease for efficiency
	_, _ = coreClient, lock // Use coreClient if needed for ConfigMapLock fallback

	// Create leader elector
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   LeaseDuration,
		RenewDeadline:   RenewDeadline,
		RetryPeriod:     RetryPeriod,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leaderCtx context.Context) {
				logger.Info("Acquired allocation lease, executing allocation", "node", l.nodeName)

				// Execute the allocation function
				allocErr := fn(leaderCtx)

				// Send result
				select {
				case resultChan <- allocErr:
				case <-ctx.Done():
				}

				// Signal completion
				close(runCompleted)
			},
			OnStoppedLeading: func() {
				logger.Info("Lost allocation lease", "node", l.nodeName)

				// Only send error if we haven't completed
				select {
				case <-runCompleted:
					// Already completed successfully
				default:
					// Lost lease before completing
					select {
					case resultChan <- fmt.Errorf("lost leadership before completing allocation"):
					default:
					}
				}
			},
			OnNewLeader: func(identity string) {
				if identity != l.identity {
					logger.Info("Waiting for current allocation to complete",
						"node", l.nodeName,
						"currentLeader", identity,
						"waiting", l.identity)
				}
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create leader elector: %w", err)
	}

	// Create a context we can cancel to stop the elector
	electorCtx, cancelElector := context.WithCancel(ctx)
	defer cancelElector()

	// Run leader election in background
	go func() {
		elector.Run(electorCtx)
	}()

	// Wait for result or timeout
	select {
	case err := <-resultChan:
		if err != nil {
			logger.Error(err, "Allocation failed while holding lease", "node", l.nodeName)
		} else {
			logger.Info("Allocation completed successfully, releasing lease", "node", l.nodeName)
		}
		// Cancel elector to release lease
		cancelElector()
		// Give it a moment to release gracefully
		time.Sleep(100 * time.Millisecond)
		return err
	case <-ctx.Done():
		logger.Info("Context cancelled while waiting for allocation lease",
			"node", l.nodeName,
			"identity", l.identity,
			"error", ctx.Err())
		return fmt.Errorf("allocation timed out waiting for lease: %w", ctx.Err())
	}
}
