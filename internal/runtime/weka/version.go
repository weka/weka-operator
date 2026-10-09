package weka

import (
	"context"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// EnsureWekaVersion sets the active Weka version if not already set.
// Mirrors Python ensure_weka_version(force_set=False) at weka_runtime.py:3291.
func EnsureWekaVersion(ctx context.Context, runner process.CommandRunner, features domain.FeatureFlags) error {
	if features.WekactlAsDefault {
		// wekactl's `weka version -J` emits objects (not strings) and marks the active
		// version via a "current" field instead of the legacy '*' marker.
		_, err := runner.Run(ctx, process.Shell(
			`weka version -J | jq -e 'any(.[]; .current)' >/dev/null || weka version set $(weka version -J | jq -r '.[0].version')`))
		return err
	}
	_, err := runner.Run(ctx, process.Shell("weka version | grep '*' || weka version set $(weka version)"))
	return err
}

// ForceSetWekaVersion unconditionally pins the active Weka version.
// Used by ssdproxy mode (force_set=True) after creating the proxy container.
// Mirrors Python ensure_weka_version(force_set=True) at weka_runtime.py:3291.
func ForceSetWekaVersion(ctx context.Context, runner process.CommandRunner, features domain.FeatureFlags) error {
	if features.WekactlAsDefault {
		_, err := runner.Run(ctx, process.Shell(`weka version set $(weka version -J | jq -r '.[0].version')`))
		return err
	}
	_, err := runner.Run(ctx, process.Shell("weka version set $(weka version -J | jq -r '.[0]')"))
	return err
}
