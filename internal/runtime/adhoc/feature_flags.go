package adhoc

import (
	"context"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/results"
)

// RunFeatureFlagsUpdate writes the feature flags already parsed from RELEASE_SPEC env var.
// Mirrors Python feature-flags-update branch at weka_runtime.py:4199.
func RunFeatureFlagsUpdate(ctx context.Context, features domain.FeatureFlags, resultsPath string) error {
	return results.Write(ctx, resultsPath, domain.FeatureFlagsResult{FeatureFlags: &features})
}
