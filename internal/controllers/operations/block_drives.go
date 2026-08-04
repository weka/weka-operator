package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

type BlockDrivesOperation struct {
	client          client.Client
	kubeService     kubernetes.KubeService
	payload         *weka.BlockDrivesPayload
	results         BlockDrivesResult
	ownerStatus     *string
	successCallback lifecycle.StepFunc
	failureCallback lifecycle.StepFunc
	unblock         bool
}

type BlockDrivesResult struct {
	Err    string `json:"err,omitempty"`
	Result string `json:"result"`
}

func NewBlockDrivesOperation(mgr ctrl.Manager, payload *weka.BlockDrivesPayload, ownerStatus *string, successCallback, failureCallback lifecycle.StepFunc) *BlockDrivesOperation {
	return &BlockDrivesOperation{
		client:          mgr.GetClient(),
		kubeService:     kubernetes.NewKubeService(mgr.GetClient()),
		payload:         payload,
		ownerStatus:     ownerStatus,
		successCallback: successCallback,
		failureCallback: failureCallback,
	}
}

func NewUnblockDrivesOperation(mgr ctrl.Manager, payload *weka.BlockDrivesPayload, ownerStatus *string, successCallback, failureCallback lifecycle.StepFunc) *BlockDrivesOperation {
	return &BlockDrivesOperation{
		client:          mgr.GetClient(),
		payload:         payload,
		ownerStatus:     ownerStatus,
		successCallback: successCallback,
		failureCallback: failureCallback,
		unblock:         true,
	}
}

func (o *BlockDrivesOperation) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "BlockDrives",
		Run:  AsRunFunc(o),
	}
}

func (o *BlockDrivesOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{
			Name:            "Noop",
			Run:             o.Noop,
			Predicates:      lifecycle.Predicates{o.IsDone},
			FinishOnSuccess: true,
		},
		&lifecycle.SimpleStep{
			Name: "BlockDrives",
			Run:  o.BlockDrives,
			Predicates: lifecycle.Predicates{
				func() bool { return !o.unblock },
				func() bool { return len(o.payload.SerialIDs) > 0 },
			},
		},
		&lifecycle.SimpleStep{
			Name: "BlockSharedDrives",
			Run:  o.BlockSharedDrives,
			Predicates: lifecycle.Predicates{
				func() bool { return !o.unblock },
				func() bool { return len(o.payload.PhysicalUUIDs) > 0 },
			},
		},
		&lifecycle.SimpleStep{
			Name: "UnblockDrives",
			Run:  o.UnblockDrives,
			Predicates: lifecycle.Predicates{
				func() bool { return o.unblock },
				func() bool { return len(o.payload.SerialIDs) > 0 },
			},
		},
		&lifecycle.SimpleStep{
			Name: "UnblockSharedDrives",
			Run:  o.UnblockSharedDrives,
			Predicates: lifecycle.Predicates{
				func() bool { return o.unblock },
				func() bool { return len(o.payload.PhysicalUUIDs) > 0 },
			},
		},
		&lifecycle.SimpleStep{
			Name: "SuccessCallback",
			Run:  o.SuccessCallback,
			Predicates: lifecycle.Predicates{
				o.OperationSucceeded,
			},
			FinishOnSuccess: true,
		},
		&lifecycle.SimpleStep{Name: "FailureCallback", Run: o.FailureCallback},
	}
}

func (o *BlockDrivesOperation) UnblockDrives(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "UnblockDrives", "node", o.payload.Node)
	defer logger.End()

	node := &corev1.Node{}
	if err := o.client.Get(ctx, types.NamespacedName{Name: o.payload.Node}, node); err != nil {
		return err
	}

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	blockedDrives := []string{}
	if blockedDrivesStr, ok := node.Annotations[consts.AnnotationBlockedDrives]; ok {
		if err := json.Unmarshal([]byte(blockedDrivesStr), &blockedDrives); err != nil {
			err = fmt.Errorf("failed to unmarshal blocked-drives annotation: %w", err)
			return err
		}
	}

	allDrives := []string{}
	fullAnnotation := node.Annotations[consts.AnnotationWekaFullDrives]
	legacyAnnotation := node.Annotations[consts.AnnotationWekaDrives]
	if serials, err := domain.ReadAnnotatedDriveSerials(fullAnnotation, legacyAnnotation); err == nil {
		allDrives = serials
	}

	logger.Debug("Available drives", "drives", allDrives)
	logger.Debug("Blocked drives", "drives", blockedDrives)

	notFoundDrives := []string{}
	updatedBlockedDrives := []string{}

	// Remove the unblocked drives from the list
	for _, serialID := range o.payload.SerialIDs {
		found := false
		for i, blockedDrive := range blockedDrives {
			if blockedDrive == serialID {
				found = true
				updatedBlockedDrives = append(blockedDrives[:i], blockedDrives[i+1:]...) //nolint:gocritic // intentional code pattern, linter suggestion does not apply here
				break
			}
		}
		if !found {
			notFoundDrives = append(notFoundDrives, serialID)
		}
	}

	if len(notFoundDrives) > 0 {
		err := fmt.Errorf("the following drives were not found in the blocked drives list: %v", notFoundDrives)
		logger.Error(err, "Failed to unblock drives")
		o.results = BlockDrivesResult{
			Err: err.Error(),
		}
		return nil
	}

	newBlockedDrivesStr, err := json.Marshal(updatedBlockedDrives)
	if err != nil {
		err = fmt.Errorf("failed to marshal blocked-drives: %w", err)
		return err
	}
	node.Annotations[consts.AnnotationBlockedDrives] = string(newBlockedDrivesStr)

	domain.SetNodeDriveAllocatable(node, allDrives, updatedBlockedDrives)

	if err := o.client.Status().Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node status: %w", err)
		return err
	}

	if err := o.client.Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node annotations: %w", err)
		return err
	}

	o.results = BlockDrivesResult{
		Result: fmt.Sprintf("Successfully unblocked %d drives on node %s", len(o.payload.SerialIDs), o.payload.Node),
	}
	return nil
}

func (o *BlockDrivesOperation) BlockDrives(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "BlockDrives", "node", o.payload.Node)
	defer logger.End()

	node := &corev1.Node{}
	if err := o.client.Get(ctx, types.NamespacedName{Name: o.payload.Node}, node); err != nil {
		return err
	}

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	blockedDrives := []string{}
	if blockedDrivesStr, ok := node.Annotations[consts.AnnotationBlockedDrives]; ok {
		if err := json.Unmarshal([]byte(blockedDrivesStr), &blockedDrives); err != nil {
			err = fmt.Errorf("failed to unmarshal blocked-drives annotation: %w", err)
			return err
		}
	}

	allDrives := []string{}
	fullAnnotation := node.Annotations[consts.AnnotationWekaFullDrives]
	legacyAnnotation := node.Annotations[consts.AnnotationWekaDrives]
	if serials, err := domain.ReadAnnotatedDriveSerials(fullAnnotation, legacyAnnotation); err == nil {
		allDrives = serials
	}

	logger.Debug("Available drives", "drives", allDrives)
	logger.Debug("Blocked drives", "drives", blockedDrives)

	notFoundDrives := []string{}

	// Add the new blocked drives to the list (if not already there)
	for _, serialID := range o.payload.SerialIDs {
		isBlocked := slices.Contains(blockedDrives, serialID)

		// check if blocked drive exists in the available drives list
		existsInAllDrives := slices.Contains(allDrives, serialID)

		if !existsInAllDrives {
			notFoundDrives = append(notFoundDrives, serialID)
		}

		if !isBlocked {
			blockedDrives = append(blockedDrives, serialID)
		}
	}

	if len(notFoundDrives) > 0 {
		err := fmt.Errorf("the following drives were not found in the available drives list: %v", notFoundDrives)
		logger.Error(err, "Failed to block drives")
		o.results = BlockDrivesResult{
			Err: err.Error(),
		}
		return nil
	}

	newBlockedDrivesStr, err := json.Marshal(blockedDrives)
	if err != nil {
		err = fmt.Errorf("failed to marshal blocked-drives: %w", err)
		return err
	}
	node.Annotations[consts.AnnotationBlockedDrives] = string(newBlockedDrivesStr)

	domain.SetNodeDriveAllocatable(node, allDrives, blockedDrives)

	if err := o.client.Status().Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node status: %w", err)
		return err
	}

	// remove weka.io/sign-drives-hash annotation from nodes to force drives re-scan on the next sign drives operation
	delete(node.Annotations, consts.AnnotationSignDrivesHash)

	if err := o.client.Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node: %w", err)
		return err
	}

	o.results = BlockDrivesResult{
		Result: fmt.Sprintf("Successfully blocked %d drives on node %s", len(o.payload.SerialIDs), o.payload.Node),
	}
	return nil
}

func (o *BlockDrivesOperation) BlockSharedDrives(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "BlockSharedDrives", "node", o.payload.Node)
	defer logger.End()

	node := &corev1.Node{}
	if err := o.client.Get(ctx, types.NamespacedName{Name: o.payload.Node}, node); err != nil {
		return err
	}

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	blockedDriveUuids := []string{}
	if blockedDrivesStr, ok := node.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids]; ok {
		err := json.Unmarshal([]byte(blockedDrivesStr), &blockedDriveUuids)
		if err != nil {
			err = fmt.Errorf("failed to unmarshal blocked-drives annotation: %w", err)
			return err
		}
	}

	// blockedSerials tracks drives blocked by serial (weka.io/blocked-drives), e.g. because
	// appendMissingDrivesToBlocked found them missing from the kernel view. The capacity
	// resources below must exclude these too, or a serial-blocked drive's capacity gets
	// silently added back the next time this UUID-keyed path recomputes them.
	blockedSerials, err := domain.ReadBlockedDriveSerials(node)
	if err != nil {
		return err
	}

	sharedDrives := []domain.SharedDriveInfo{}
	if sharedDrivesStr, ok := node.Annotations[consts.AnnotationSharedDrives]; ok {
		err = json.Unmarshal([]byte(sharedDrivesStr), &sharedDrives)
		if err != nil {
			err = fmt.Errorf("failed to unmarshal shared-drives annotation: %w", err)
			return err
		}
	}

	allSharedDriveUuids := []string{}
	for _, drive := range sharedDrives {
		allSharedDriveUuids = append(allSharedDriveUuids, drive.PhysicalUUID)
	}

	logger.Debug("Available shared drives", "shared_drive_uuids", allSharedDriveUuids)
	logger.Debug("Blocked drive uuids", "blocked_drive_uuids", blockedDriveUuids)

	notFoundDrives := []string{}

	// Add the new blocked drives to the list (if not already there)
	for _, physicalUuid := range o.payload.PhysicalUUIDs {
		isBlocked := slices.Contains(blockedDriveUuids, physicalUuid)

		// check if blocked drive exists in the available drives list
		existsInAllDrives := slices.Contains(allSharedDriveUuids, physicalUuid)

		if !existsInAllDrives {
			notFoundDrives = append(notFoundDrives, physicalUuid)
		}

		if !isBlocked {
			blockedDriveUuids = append(blockedDriveUuids, physicalUuid)
		}
	}

	if len(notFoundDrives) > 0 {
		notFoundErr := fmt.Errorf("the following drives were not found in the available drives list: %v", notFoundDrives)
		logger.Error(notFoundErr, "Failed to block drives")
		o.results = BlockDrivesResult{
			Err: notFoundErr.Error(),
		}
		return nil
	}

	newBlockedDrivesStr, err := json.Marshal(blockedDriveUuids)
	if err != nil {
		err = fmt.Errorf("failed to marshal blocked-drives: %w", err)
		return err
	}
	node.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids] = string(newBlockedDrivesStr)

	// update weka.io/shared-drives-capacity extended resources (TLC + QLC), excluding
	// drives blocked by either physical UUID (this operation) or serial (funcs_oneoff.go's
	// updateProxyModeAnnotations), so all writers of these resources agree.
	domain.SetSharedDriveCapacityResources(node, sharedDrives, blockedDriveUuids, blockedSerials)

	if err := o.client.Status().Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node status: %w", err)
		return err
	}

	// remove weka.io/sign-drives-hash annotation from nodes to force drives re-scan on the next sign drives operation
	delete(node.Annotations, consts.AnnotationSignDrivesHash)

	if err := o.client.Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node: %w", err)
		return err
	}

	o.results = BlockDrivesResult{
		Result: fmt.Sprintf("Successfully blocked %d drives on node %s", len(o.payload.PhysicalUUIDs), o.payload.Node),
	}
	return nil
}

func (o *BlockDrivesOperation) UnblockSharedDrives(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "UnblockSharedDrives", "node", o.payload.Node)
	defer logger.End()

	node := &corev1.Node{}
	if err := o.client.Get(ctx, types.NamespacedName{Name: o.payload.Node}, node); err != nil {
		return err
	}

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	blockedDriveUuids := []string{}
	if blockedDrivesStr, ok := node.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids]; ok {
		err := json.Unmarshal([]byte(blockedDrivesStr), &blockedDriveUuids)
		if err != nil {
			err = fmt.Errorf("failed to unmarshal blocked-drives annotation: %w", err)
			return err
		}
	}

	// blockedSerials tracks drives blocked by serial (weka.io/blocked-drives), e.g. because
	// appendMissingDrivesToBlocked found them missing from the kernel view. The capacity
	// resources below must exclude these too, or a serial-blocked drive's capacity gets
	// silently added back the next time this UUID-keyed path recomputes them.
	blockedSerials, err := domain.ReadBlockedDriveSerials(node)
	if err != nil {
		return err
	}

	sharedDrives := []domain.SharedDriveInfo{}
	if sharedDrivesStr, ok := node.Annotations[consts.AnnotationSharedDrives]; ok {
		err = json.Unmarshal([]byte(sharedDrivesStr), &sharedDrives)
		if err != nil {
			err = fmt.Errorf("failed to unmarshal shared-drives annotation: %w", err)
			return err
		}
	}

	allSharedDriveUuids := []string{}
	for _, drive := range sharedDrives {
		allSharedDriveUuids = append(allSharedDriveUuids, drive.PhysicalUUID)
	}

	logger.Debug("Available shared drives", "shared_drive_uuids", allSharedDriveUuids)
	logger.Debug("Blocked drive uuids", "blocked_drive_uuids", blockedDriveUuids)

	notFoundDrives := []string{}
	updatedBlockedDriveUuids := []string{}

	// Remove the unblocked drives from the list
	for _, physicalUuid := range o.payload.PhysicalUUIDs {
		found := false
		for i, blockedUuid := range blockedDriveUuids {
			if blockedUuid == physicalUuid {
				found = true
				updatedBlockedDriveUuids = append(blockedDriveUuids[:i], blockedDriveUuids[i+1:]...) //nolint:gocritic // intentional code pattern, linter suggestion does not apply here
				break
			}
		}
		if !found {
			notFoundDrives = append(notFoundDrives, physicalUuid)
		}
	}

	if len(notFoundDrives) > 0 {
		notFoundErr := fmt.Errorf("the following drives were not found in the blocked drives list: %v", notFoundDrives)
		logger.Error(notFoundErr, "Failed to unblock drives")
		o.results = BlockDrivesResult{
			Err: notFoundErr.Error(),
		}
		return nil
	}

	newBlockedDrivesStr, err := json.Marshal(updatedBlockedDriveUuids)
	if err != nil {
		err = fmt.Errorf("failed to marshal blocked-drives: %w", err)
		return err
	}
	node.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids] = string(newBlockedDrivesStr)

	// update weka.io/shared-drives-capacity extended resources (TLC + QLC). Previously this
	// summed TLC and QLC drives together into a single total and wrote it only to the TLC
	// resource, silently leaving the QLC resource stale (never zeroed, never updated) — fixed
	// here to match BlockSharedDrives above by delegating to the same domain helper. Also
	// excludes drives blocked by serial (weka.io/blocked-drives) so this path agrees with the
	// other writers of these resources.
	domain.SetSharedDriveCapacityResources(node, sharedDrives, updatedBlockedDriveUuids, blockedSerials)

	if err := o.client.Status().Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node status: %w", err)
		return err
	}

	if err := o.client.Update(ctx, node); err != nil {
		err = fmt.Errorf("error updating node annotations: %w", err)
		return err
	}

	o.results = BlockDrivesResult{
		Result: fmt.Sprintf("Successfully unblocked %d drives on node %s", len(o.payload.PhysicalUUIDs), o.payload.Node),
	}
	return nil
}

func (o *BlockDrivesOperation) GetResult() BlockDrivesResult {
	return o.results
}

func (o *BlockDrivesOperation) GetJsonResult() string {
	resultJSON, err := json.Marshal(o.results)
	if err != nil {
		return ""
	}
	return string(resultJSON)
}

func (o *BlockDrivesOperation) IsDone() bool {
	return o.ownerStatus != nil && *o.ownerStatus == "Done"
}

func (o *BlockDrivesOperation) OperationSucceeded() bool {
	return o.results.Err == ""
}

func (o *BlockDrivesOperation) SuccessCallback(ctx context.Context) error {
	if o.successCallback == nil {
		return nil
	}
	return o.successCallback(ctx)
}

func (o *BlockDrivesOperation) FailureCallback(ctx context.Context) error {
	if o.failureCallback == nil {
		return nil
	}
	return o.failureCallback(ctx)
}

func (o *BlockDrivesOperation) Noop(ctx context.Context) error {
	return nil
}
