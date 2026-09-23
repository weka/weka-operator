package adhoc

import (
	"context"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/results"
)

// RunFeatureFlagsUpdate writes the feature flags already parsed from RELEASE_SPEC env var.
// Mirrors Python feature-flags-update branch at weka_runtime.py:4199.
func RunFeatureFlagsUpdate(_ context.Context, cfg *config.Config) error {
	return results.Write(domain.FeatureFlagsResult{FeatureFlags: &cfg.Features})
}
