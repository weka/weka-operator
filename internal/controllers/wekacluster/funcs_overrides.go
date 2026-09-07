package wekacluster

import (
	"context"
	"strconv"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/pkg/domain"
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

// EnsureWekaHomeCacertOverride keeps the cluster-wide weka_cloud_ca_cert_path override in step
// with the resolved cacert secret. configureWekaHome sets it once at provisioning; this step
// covers a secret added later and, more importantly, one that is cleared: an explicit CA path
// replaces the OS trust store, so a leftover path pointing at a file no pod stages any more
// breaks Weka Home for the whole cluster.
func (r *wekaClusterReconcilerLoop) EnsureWekaHomeCacertOverride(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "EnsureWekaHomeCacertOverride")
	defer logger.End()

	wekahomeConfig, err := domain.GetWekahomeConfig(r.cluster)
	if err != nil {
		return err
	}

	execInContainer := discovery.SelectActiveContainer(r.containers)
	if execInContainer == nil {
		logger.Info("No active container found, skipping weka home cacert override reconciliation")
		return nil
	}
	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	return r.reconcileWekaHomeCacertOverride(ctx, wekaService, cacertSecretForOverride(wekahomeConfig))
}

// cacertSecretForOverride resolves the secret the override should point at. An empty Endpoint
// means Weka Home is disabled, which resolves to "" — the removal path — so an override left
// from when it was enabled still gets reaped. AllowInsecure deliberately does not gate this:
// insecure_weka_cloud_https_call is written once at provisioning, so removing the CA path when
// insecure is enabled later would leave a still-verifying cluster with no CA.
func cacertSecretForOverride(cfg weka.WekaHomeConfig) string {
	if cfg.Endpoint == "" {
		return ""
	}
	return cfg.CacertSecret
}

func (r *wekaClusterReconcilerLoop) reconcileWekaHomeCacertOverride(ctx context.Context, wekaService services.WekaService, cacertSecret string) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "reconcileWekaHomeCacertOverride")
	defer logger.End()

	const key = "weka_cloud_ca_cert_path"
	if cacertSecret != "" {
		return r.ensureOverride(ctx, wekaService, key, domain.WekaHomeCacertPath, "weka-operator: Weka Home CA bundle staged from wekaHome.cacertSecret")
	}

	entries, err := wekaService.ListOverridesByKey(ctx, key)
	if err != nil {
		return errors.Wrapf(err, "failed to list overrides for key %s", key)
	}
	for _, e := range entries {
		// An override row carries no owner, so its value is the only marker of authorship: a row
		// pointing anywhere else was set by hand or by another tool (a CA mounted through
		// extraVolumes, say) and is not ours to reap.
		if e.Value != domain.WekaHomeCacertPath {
			continue
		}
		logger.Info("Removing override, cacert secret is no longer set", "key", key, "id", e.OverrideID)
		if err := wekaService.RemoveOverride(ctx, e.OverrideID); err != nil {
			return errors.Wrapf(err, "failed to remove override %s", e.OverrideID)
		}
	}
	return nil
}

// ensureOverride sets a weka debug override unless the current tail row already carries the wanted
// value and is enabled. weka debug override add --force always inserts a new row (not an upsert),
// so we check the last entry before writing to keep the override table tidy. A disabled row does
// not count as set: its value is inert, so skipping on it would report success over a setting that
// is not in effect.
func (r *wekaClusterReconcilerLoop) ensureOverride(ctx context.Context, svc services.WekaService, key, val, comment string) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ensureOverride", "key", key)
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
