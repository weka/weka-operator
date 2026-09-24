package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

// RemoveFullDrivesFromNode drops the given serials from the node's weka.io/weka-full-drives (and
// legacy weka.io/weka-drives) annotations, then always recomputes the weka.io/drives extended
// resource from the remaining non-blocked entries so a status write that failed on a prior call is
// retried. No-op when none of the serials are present.
func RemoveFullDrivesFromNode(ctx context.Context, c client.Client, nodeName string, serials []string) error {
	if len(serials) == 0 {
		return nil
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node := &corev1.Node{}
		if err := c.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
			return err
		}
		if node.Annotations == nil {
			return nil
		}

		entries, err := domain.ReadDriveAnnotations(node.Annotations[consts.AnnotationWekaFullDrives])
		if err != nil {
			return err
		}
		remainingEntries, removedFull := domain.RemoveDriveSerials(entries, serials)

		remainingLegacy, removedLegacy, err := removeLegacyDriveSerials(node, serials)
		if err != nil {
			return err
		}

		if !removedFull && !removedLegacy {
			return nil
		}

		setJSON := func(key string, v any) error {
			raw, err := json.Marshal(v)
			if err != nil {
				return fmt.Errorf("failed to marshal %s: %w", key, err)
			}
			node.Annotations[key] = string(raw)
			return nil
		}

		// Only written when it actually changed: a legacy-only node must not gain an empty
		// weka-full-drives annotation, whose mere presence flips the "absent" guards that treat the
		// node as never having been signed.
		if removedFull {
			if err := setJSON(consts.AnnotationWekaFullDrives, remainingEntries); err != nil {
				return err
			}
		}

		if removedLegacy {
			if err := setJSON(consts.AnnotationWekaDrives, remainingLegacy); err != nil {
				return err
			}
		}

		return c.Update(ctx, node)
	})
	if err != nil {
		return fmt.Errorf("error updating node annotations: %w", err)
	}

	// Always runs, whether or not this call itself changed an annotation: a prior call's status
	// write can fail after its own annotation update succeeded, leaving weka.io/drives stale with
	// nothing left for a later call to find "changed". Idempotent via a before/after compare, so a
	// node already up to date costs a Get and no Update.
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node := &corev1.Node{}
		if getErr := c.Get(ctx, types.NamespacedName{Name: nodeName}, node); getErr != nil {
			return getErr
		}
		capBefore := node.Status.Capacity[consts.ResourceDrives]
		allocBefore := node.Status.Allocatable[consts.ResourceDrives]
		if capErr := setDriveCountCapacity(node); capErr != nil {
			return capErr
		}
		capAfter := node.Status.Capacity[consts.ResourceDrives]
		allocAfter := node.Status.Allocatable[consts.ResourceDrives]
		if capAfter.Cmp(capBefore) == 0 && allocAfter.Cmp(allocBefore) == 0 {
			return nil
		}
		return c.Status().Update(ctx, node)
	})
	if err != nil {
		return fmt.Errorf("error updating node status: %w", err)
	}

	return nil
}

// removeLegacyDriveSerials drops serials from the node's legacy weka.io/weka-drives annotation (a
// plain serial list), reporting whether anything was actually removed.
func removeLegacyDriveSerials(node *corev1.Node, serials []string) (remaining []string, removed bool, err error) {
	raw := node.Annotations[consts.AnnotationWekaDrives]
	if raw == "" {
		return nil, false, nil
	}
	var legacy []string
	if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
		return nil, false, fmt.Errorf("failed to parse %s: %w", consts.AnnotationWekaDrives, err)
	}

	remaining = slices.DeleteFunc(slices.Clone(legacy), func(s string) bool { return slices.Contains(serials, s) })
	removed = len(remaining) != len(legacy)
	return remaining, removed, nil
}
