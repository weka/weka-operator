package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
)

type PrePullImagePayload struct {
	TargetImage     string
	ImagePullSecret string
	// NodeSelector filters which nodes to pre-pull on (applied with OR logic if multiple selectors)
	NodeSelector map[string]string
	// Tolerations for the pre-pull pods
	Tolerations []v1.Toleration
	OwnerKind   string
	OwnerMeta   metav1.Object
	Role        string
}

// PrePullImageOperation implements the Operation interface for image pre-pulling
type PrePullImageOperation struct {
	client  client.Client
	scheme  *runtime.Scheme
	payload *PrePullImagePayload
	results PrePullImageResult
	timeout time.Duration

	// Cached state between steps
	targetNodes   []corev1.Node
	currentStatus PrePullStatusResult
	daemonSet     *appsv1.DaemonSet
	namespace     string
	ownerUID      string
}

// PrePullImageResult contains the result of the pre-pull operation
type PrePullImageResult struct {
	TargetImage  string `json:"targetImage"`
	TotalNodes   int    `json:"totalNodes"`
	ReadyNodes   int    `json:"readyNodes"`
	PendingNodes int    `json:"pendingNodes,omitempty"`
	FailedNodes  int    `json:"failedNodes,omitempty"`
	Status       string `json:"status"` // "InProgress", "Completed", "TimedOut", "Skipped"
	Message      string `json:"message,omitempty"`
}

func NewPrePullImageOperation(mgr ctrl.Manager, payload *PrePullImagePayload) *PrePullImageOperation {
	namespace := payload.OwnerMeta.GetNamespace()
	ownerUID := string(payload.OwnerMeta.GetUID())

	return &PrePullImageOperation{
		client:    mgr.GetClient(),
		scheme:    mgr.GetScheme(),
		payload:   payload,
		timeout:   config.Config.Upgrade.ImagePrePullTimeout,
		namespace: namespace,
		ownerUID:  ownerUID,
	}
}

// AsStep returns the operation as a single lifecycle step
func (o *PrePullImageOperation) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "PrePullImage",
		Run:  AsRunFunc(o),
	}
}

// GetSteps returns the steps for this operation
func (o *PrePullImageOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{
			Name: "CleanupStaleDaemonSets",
			Run:  o.CleanupStaleDaemonSets,
		},
		&lifecycle.SimpleStep{
			Name: "DetermineTargetNodes",
			Run:  o.DetermineTargetNodes,
		},
		&lifecycle.SimpleStep{
			Name:            "SkipIfNoNodes",
			Run:             o.SkipIfNoNodes,
			Predicates:      lifecycle.Predicates{o.hasNoTargetNodes},
			FinishOnSuccess: true,
		},
		&lifecycle.SimpleStep{
			Name: "EnsureDaemonSet",
			Run:  o.EnsureDaemonSet,
		},
		&lifecycle.SimpleStep{
			Name: "CheckPodStatus",
			Run:  o.CheckPodStatus,
		},
		&lifecycle.SimpleStep{
			Name:       "HandleTimeout",
			Run:        o.HandleTimeout,
			Predicates: lifecycle.Predicates{o.isTimedOut},
		},
		&lifecycle.SimpleStep{
			Name:       "WaitForPods",
			Run:        o.WaitForPods,
			Predicates: lifecycle.Predicates{lifecycle.IsNotFunc(o.allPodsReady)},
		},
		&lifecycle.SimpleStep{
			Name: "ReportSuccess",
			Run:  o.ReportSuccess,
		},
		&lifecycle.SimpleStep{
			Name: "CleanupStaleDaemonSets",
			Run:  o.CleanupAllPrePullDaemonSets,
		},
	}
}

// GetJsonResult returns the operation result as JSON
func (o *PrePullImageOperation) GetJsonResult() string {
	resultJSON, _ := json.Marshal(o.results)
	return string(resultJSON)
}

func (o *PrePullImageOperation) hasNoTargetNodes() bool {
	return len(o.targetNodes) == 0
}

func (o *PrePullImageOperation) isTimedOut() bool {
	if o.daemonSet == nil {
		return false
	}

	daemonsetCreationTime := o.daemonSet.GetCreationTimestamp().Time
	return time.Since(daemonsetCreationTime) > o.timeout
}

func (o *PrePullImageOperation) allPodsReady() bool {
	return o.currentStatus.AllReady
}

func (o *PrePullImageOperation) SkipIfNoNodes(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "SkipIfNoNodes")
	defer end()

	logger.Info("No target nodes found for pre-pull, skipping")
	o.results = PrePullImageResult{
		TargetImage: o.payload.TargetImage,
		Status:      "Skipped",
		Message:     "No target nodes found",
	}
	return nil
}

func (o *PrePullImageOperation) DetermineTargetNodes(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "DetermineTargetNodes")
	defer end()

	nodes, err := GetTargetNodes(ctx, o.client, o.payload.NodeSelector, o.payload.Tolerations)
	if err != nil {
		return errors.Wrap(err, "failed to get target nodes")
	}

	o.targetNodes = nodes
	logger.Info(
		"Found target nodes for pre-pull",
		"count", len(nodes),
		"image", o.payload.TargetImage,
		"nodeSelector", o.payload.NodeSelector,
	)

	return nil
}

func (o *PrePullImageOperation) EnsureDaemonSet(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureDaemonSet")
	defer end()

	dsNamePrefix := o.payload.OwnerMeta.GetName() + "-" + o.payload.Role
	dsName := GetPrePullDaemonSetName(dsNamePrefix, o.payload.TargetImage)

	// Check if DaemonSet already exists
	existingDS := &appsv1.DaemonSet{}
	err := o.client.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: dsName}, existingDS)
	if err == nil {
		// DaemonSet exists
		o.daemonSet = existingDS
		logger.Debug("Pre-pull DaemonSet already exists", "name", dsName)
		return nil
	}

	if !apierrors.IsNotFound(err) {
		return errors.Wrap(err, "failed to get DaemonSet")
	}

	// Create new DaemonSet
	cfg := PrePullDaemonSetConfig{
		Name:            dsName,
		Namespace:       o.namespace,
		OwnerUID:        string(o.payload.OwnerMeta.GetUID()),
		OwnerKind:       o.payload.OwnerKind,
		OwnerName:       o.payload.OwnerMeta.GetName(),
		OwnerNamespace:  o.payload.OwnerMeta.GetNamespace(),
		TargetImage:     o.payload.TargetImage,
		ImagePullSecret: o.payload.ImagePullSecret,
		NodeSelector:    o.payload.NodeSelector,
		Tolerations:     o.payload.Tolerations,
	}

	ds := BuildPrePullDaemonSet(cfg)

	logger.Info("Creating pre-pull DaemonSet", "name", dsName, "image", o.payload.TargetImage, "targetNodes", len(o.targetNodes))

	if err := o.client.Create(ctx, ds); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Race condition - another reconcile created it
			if err := o.client.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: dsName}, existingDS); err != nil {
				return errors.Wrap(err, "failed to get existing DaemonSet after create conflict")
			}
			o.daemonSet = existingDS
			return nil
		}
		return errors.Wrap(err, "failed to create DaemonSet")
	}

	o.daemonSet = ds
	return nil
}

func (o *PrePullImageOperation) CheckPodStatus(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "CheckPodStatus")
	defer end()

	if o.daemonSet == nil {
		return errors.New("DaemonSet not found")
	}

	status, err := CheckPrePullStatus(ctx, o.client, o.daemonSet, o.targetNodes)
	if err != nil {
		return errors.Wrap(err, "failed to check pre-pull status")
	}

	o.currentStatus = status
	o.results = PrePullImageResult{
		TargetImage:  o.payload.TargetImage,
		TotalNodes:   status.TotalNodes,
		ReadyNodes:   status.ReadyNodes,
		PendingNodes: status.PendingNodes,
		FailedNodes:  status.FailedNodes,
		Status:       "InProgress",
	}

	logger.Info("Pre-pull status",
		"image", o.payload.TargetImage,
		"ready", status.ReadyNodes,
		"total", status.TotalNodes,
		"pending", status.PendingNodes,
		"failed", status.FailedNodes,
	)

	return nil
}

func (o *PrePullImageOperation) HandleTimeout(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleTimeout")
	defer end()

	// Fail-open: proceed with upgrade even if timeout is reached
	logger.Warn("Pre-pull timed out, proceeding with upgrade (fail-open)",
		"timeout", o.timeout,
		"ready", o.currentStatus.ReadyNodes,
		"total", o.currentStatus.TotalNodes,
		"pending", o.currentStatus.PendingNodes,
		"failed", o.currentStatus.FailedNodes,
	)

	o.results = PrePullImageResult{
		TargetImage:  o.payload.TargetImage,
		TotalNodes:   o.currentStatus.TotalNodes,
		ReadyNodes:   o.currentStatus.ReadyNodes,
		PendingNodes: o.currentStatus.PendingNodes,
		FailedNodes:  o.currentStatus.FailedNodes,
		Status:       "TimedOut",
		Message:      fmt.Sprintf("Timed out after %v, proceeding with upgrade", o.timeout),
	}

	// Don't return error - allow upgrade to proceed
	return nil
}

func (o *PrePullImageOperation) WaitForPods(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "WaitForPods")
	defer end()

	logger.Debug("Waiting for pre-pull pods",
		"ready", o.currentStatus.ReadyNodes,
		"total", o.currentStatus.TotalNodes,
	)

	return lifecycle.NewWaitErrorWithDuration(
		fmt.Errorf("pre-pull in progress: %d/%d nodes ready", o.currentStatus.ReadyNodes, o.currentStatus.TotalNodes),
		15*time.Second,
	)
}

func (o *PrePullImageOperation) ReportSuccess(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ReportSuccess")
	defer end()

	logger.Info("Pre-pull completed successfully",
		"image", o.payload.TargetImage,
		"nodes", o.currentStatus.TotalNodes,
	)

	o.results = PrePullImageResult{
		TargetImage:  o.payload.TargetImage,
		TotalNodes:   o.currentStatus.TotalNodes,
		ReadyNodes:   o.currentStatus.ReadyNodes,
		PendingNodes: o.currentStatus.PendingNodes,
		FailedNodes:  o.currentStatus.FailedNodes,
		Status:       "Completed",
		Message:      fmt.Sprintf("All %d nodes have pulled the image", o.currentStatus.TotalNodes),
	}

	return nil
}

// cleanupStaleDaemonSets removes DaemonSets for old/completed upgrades
func (o *PrePullImageOperation) CleanupStaleDaemonSets(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "CleanupStaleDaemonSets")
	defer end()

	dsList, err := ListPrePullDaemonSets(ctx, o.client, o.namespace, o.ownerUID)
	if err != nil {
		return errors.Wrap(err, "failed to list DaemonSets")
	}

	currentImageHash := GetPrePullImageHash(o.payload.TargetImage)

	for i := range dsList {
		ds := &dsList[i]
		dsImageHash := ds.Labels[PrePullLabelImageHash]

		// Delete DaemonSets for different images (old upgrades)
		if dsImageHash != currentImageHash {
			logger.Info("Deleting stale pre-pull DaemonSet", "name", ds.Name, "imageHash", dsImageHash)
			if err := o.client.Delete(ctx, ds); err != nil && !apierrors.IsNotFound(err) {
				logger.Warn("Failed to delete stale DaemonSet", "name", ds.Name, "error", err)
			}
		}
	}

	return nil
}

// CleanupAllPrePullDaemonSets removes all pre-pull DaemonSets for the given owner
func (o *PrePullImageOperation) CleanupAllPrePullDaemonSets(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "CleanupPrePullDaemonSet")
	defer end()

	dsList, err := ListPrePullDaemonSets(ctx, o.client, o.namespace, o.ownerUID)
	if err != nil {
		return errors.Wrap(err, "failed to list DaemonSets")
	}

	// delete all DaemonSets for the owner
	for i := range dsList {
		ds := &dsList[i]

		logger.Debug("Cleaning up pre-pull DaemonSet", "name", ds.Name)

		if err := o.client.Delete(ctx, ds); err != nil && !apierrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to delete DaemonSet")
		}
	}

	return nil
}
