package wekacluster

import (
	"context"
	"strconv"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// EnsureWekaOverrides reconciles weka debug overrides that the operator manages.
// It is gated by the IsDriveSharing predicate so it only runs on drive-sharing clusters.
// Additional overrides can be appended here following the same ensureOverride pattern.
func (r *wekaClusterReconcilerLoop) EnsureWekaOverrides(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "EnsureWekaOverrides")
	defer logger.End()

	execInContainer := discovery.SelectActiveContainer(r.containers)
	if execInContainer == nil {
		logger.Info("No active container found, skipping weka overrides reconciliation")
		return nil
	}

	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	key := "small_big_disk_sizes_max_proportion_factor"
	val := strconv.Itoa(config.Config.DriveSharing.SmallBigDiskSizesMaxProportionFactor)
	if err := r.ensureOverride(ctx, wekaService, key, val, "weka-operator: drive-sharing small/big disk size proportion"); err != nil {
		return errors.Wrapf(err, "failed to ensure override %s", key)
	}

	return nil
}

// ensureOverride sets a weka debug override only when the current tail value differs.
// weka debug override add --force always inserts a new row (not an upsert), so we check
// the last entry before writing to keep the override table tidy.
func (r *wekaClusterReconcilerLoop) ensureOverride(ctx context.Context, svc services.WekaService, key, val, comment string) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ensureOverride", "key", key)
	defer logger.End()

	entries, err := svc.ListOverridesByKey(ctx, key)
	if err != nil {
		return errors.Wrapf(err, "failed to list overrides for key %s", key)
	}

	if len(entries) > 0 && entries[len(entries)-1].Value == val {
		logger.Info("Override already set, skipping", "key", key, "value", val)
		return nil
	}

	logger.Info("Setting override", "key", key, "value", val)
	if err := svc.AddOverride(ctx, key, val, comment, true); err != nil {
		return errors.Wrapf(err, "failed to add override %s=%s", key, val)
	}

	return nil
}
