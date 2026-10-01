package weka

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/paths"
)

// WriteFeatureFlags atomically writes feature flags to roots.K8sRuntime/feature_flags.json.
// Mirrors Python write_feature_flags_json() at weka_runtime.py:1401.
func WriteFeatureFlags(_ context.Context, roots *paths.Roots, f domain.FeatureFlags) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	dst := filepath.Join(roots.K8sRuntime, "feature_flags.json")
	tmp := dst + ".tmp"
	if err := os.MkdirAll(roots.K8sRuntime, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// WriteTelemetryOverride writes the telemetry audit-traces config override atomically.
// Mirrors Python write_telemetry_config_override() at weka_runtime.py:3344.
func WriteTelemetryOverride(ctx context.Context, roots *paths.Roots) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "WriteTelemetryOverride")
	defer logger.End()

	auditDir := filepath.Join(roots.OptWeka, "external-mounts", "shared_boot_level", "audit-traces")
	configPath := filepath.Join(auditDir, "override.config.json")

	if _, err := os.Stat(auditDir); os.IsNotExist(err) {
		logger.Info("Audit traces directory does not exist, skipping config override", "dir", auditDir)
		return nil
	}

	var stat syscall.Statfs_t
	minimumFreeSpace := int64(5368709120) // fallback ~5GiB
	if err := syscall.Statfs(auditDir, &stat); err == nil {
		// Python uses f_blocks * f_frsize (weka_runtime.py:3360); Bsize is f_bsize which may differ
		// on some NFS/btrfs mounts. statfsFragmentSize returns Frsize on Linux (f_frsize).
		total := int64(stat.Blocks) * statfsFragmentSize(&stat)
		minimumFreeSpace = total * 20 / 100
	}

	const tracesRetentionSize = 10 * 1024 * 1024 * 1024 // 10 GiB

	configOverride := map[string]interface{}{
		"global": map[string]interface{}{
			"dumping": map[string]interface{}{
				"histogramRetentionSize": 134217728, // 128 MiB
				"maxHistograms":          30000,
				"minimumFreeSpace":       minimumFreeSpace,
				"tracesRetentionSize":    tracesRetentionSize,
			},
		},
	}

	newContent, err := json.Marshal(configOverride)
	if err != nil {
		return err
	}

	if existing, err := os.ReadFile(configPath); err == nil {
		if bytes.Equal(existing, newContent) {
			return nil
		}
	}

	logger.Info("Writing telemetry config override",
		"tracesRetentionSize", tracesRetentionSize, "minimumFreeSpace", minimumFreeSpace)

	tmpPath := fmt.Sprintf("%s/.config.json.tmp.%d", auditDir, os.Getpid())
	if err := os.WriteFile(tmpPath, newContent, 0o644); err != nil {
		return fmt.Errorf("WriteTelemetryOverride write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath) //nolint:errcheck // best-effort cleanup of orphaned temp file
		return fmt.Errorf("WriteTelemetryOverride rename: %w", err)
	}
	return nil
}
