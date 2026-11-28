package services

import (
	"context"
	"fmt"
	"strconv"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"

	globalconfig "github.com/weka/weka-operator/internal/config"
	wekav1alpha1 "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

type OperatorOverride struct {
	Key     string
	Value   string
	Comment string
}

// CreationOverrides lists the weka overrides the operator manages that must already be in place
// when containers join, drives are added and the stripe is applied: weka enforces them before
// start-io, so setting them from a post-start-io step deadlocks cluster creation.
func CreationOverrides(cluster *wekav1alpha1.WekaCluster) []OperatorOverride {
	out := []OperatorOverride{{
		Key:     "authenticate_client_join",
		Comment: "weka-operator: require the join secret for joining containers",
	}}
	_, rl, _ := globalconfig.Config.DriveSharing.EffectiveProtection(cluster.Spec.StripeWidth, cluster.Spec.RedundancyLevel, cluster.Spec.HotSpare)
	// QA/test only: a single parity chunk leaves a stripe unprotected during rebuild.
	if globalconfig.Config.DriveSharing.AllowSingleParity && rl == 1 {
		out = append(out, OperatorOverride{
			Key:     "allow_1_parity",
			Value:   "true",
			Comment: fmt.Sprintf("weka-operator AllowSingleParity: enable single parity for cluster %s (parity=%d)", cluster.Name, rl),
		})
	}
	if cluster.IsDriveSharing() {
		out = append(out, DriveSizeRatioOverride())
	}
	return out
}

// DriveSizeRatioOverride is the one creation override that still matters after creation: it caps the
// size ratio of drives added later (replacements, growth), so it follows the helm value.
func DriveSizeRatioOverride() OperatorOverride {
	return OperatorOverride{
		Key:     "small_big_disk_sizes_max_proportion_factor",
		Value:   strconv.Itoa(globalconfig.Config.DriveSharing.SmallBigDiskSizesMaxProportionFactor),
		Comment: "weka-operator: drive-sharing small/big disk size proportion",
	}
}

// ApplyOverrides applies each override via EnsureOverride, wrapping errors with the key.
func ApplyOverrides(ctx context.Context, svc WekaService, overrides []OperatorOverride) error {
	for _, o := range overrides {
		if err := EnsureOverride(ctx, svc, o.Key, o.Value, o.Comment); err != nil {
			return errors.Wrapf(err, "failed to ensure override %s", o.Key)
		}
	}
	return nil
}

// EnsureOverride sets a weka debug override unless the current tail row already carries the wanted
// value and is enabled. weka debug override add --force always inserts a new row (not an upsert),
// so we check the last entry before writing to keep the override table tidy. A disabled row does
// not count as set: its value is inert, so skipping on it would report success over a setting that
// is not in effect.
func EnsureOverride(ctx context.Context, svc WekaService, key, val, comment string) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "EnsureOverride", "key", key)
	defer logger.End()

	entries, err := svc.ListOverridesByKey(ctx, key)
	if err != nil {
		return errors.Wrapf(err, "failed to list overrides for key %s", key)
	}

	if last := len(entries) - 1; last >= 0 && entries[last].Value == val && entries[last].Enabled {
		logger.Info("Override already set, skipping", "key", key, "value", val)
		return nil
	}

	logger.Info("Setting override", "key", key, "value", val)
	if err := svc.AddOverride(ctx, key, val, comment, true); err != nil {
		return errors.Wrapf(err, "failed to add override %s=%s", key, val)
	}

	return nil
}
