package wekacluster

import (
	"context"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// EnsureWekaOverrides keeps the drive-size ratio override in step with the helm value on a running
// drive-sharing cluster. The other creation overrides only matter before the stripe is applied and
// containers join, so they are set once by FormCluster and not reconciled.
func (r *wekaClusterReconcilerLoop) EnsureWekaOverrides(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "EnsureWekaOverrides")
	defer logger.End()

	execInContainer := discovery.SelectActiveContainer(r.containers)
	if execInContainer == nil {
		logger.Info("No active container found, skipping weka overrides reconciliation")
		return nil
	}

	wekaService := services.NewWekaService(r.ExecService, execInContainer)
	return services.ApplyOverrides(ctx, wekaService, []services.OperatorOverride{services.DriveSizeRatioOverride()})
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
		return services.EnsureOverride(ctx, wekaService, key, domain.WekaHomeCacertPath, "weka-operator: Weka Home CA bundle staged from wekaHome.cacertSecret")
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
