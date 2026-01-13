package services

import (
	"context"
	"fmt"
	"sync"

	"github.com/weka/go-weka-observability/instrumentation"

	"github.com/weka/weka-operator/internal/pkg/domain"
)

// featureFlagsCache is the internal cache instance - use the package-level functions below
var featureFlagsCache featureFlagsCacheService

func init() {
	featureFlagsCache = featureFlagsCacheService{
		cache: make(map[string]*domain.FeatureFlags),
	}
}

// GetFeatureFlags returns cached feature flags for the given image.
// Returns ErrFeatureFlagsNotCached if flags are not in cache.
// This is the high-level accessor that should be used by all code needing feature flags.
// If flags are not cached, callers should ensure they are fetched via the appropriate
// operation (GetFeatureFlagsOperation or GetFeatureFlagsViaAdhocOperation) before retrying.
func GetFeatureFlags(ctx context.Context, image string) (*domain.FeatureFlags, error) {
	return featureFlagsCache.GetFeatureFlags(ctx, image)
}

// SetFeatureFlags caches feature flags for the given image.
// This should only be called by operations that fetch feature flags.
func SetFeatureFlags(ctx context.Context, image string, flags *domain.FeatureFlags) error {
	return featureFlagsCache.SetFeatureFlags(ctx, image, flags)
}

// HasFeatureFlags checks if feature flags for the given image are cached.
func HasFeatureFlags(ctx context.Context, image string) bool {
	return featureFlagsCache.Has(ctx, image)
}

// FeatureFlagsCache is exposed for backwards compatibility but prefer using
// the package-level functions GetFeatureFlags, SetFeatureFlags, HasFeatureFlags.
// Deprecated: Use GetFeatureFlags, SetFeatureFlags, HasFeatureFlags instead.
var FeatureFlagsCache FeatureFlagsCacheService = &featureFlagsCache

// FeatureFlagsCacheService is a service that keeps cached feature flags per weka image
// Feature flags are tied to the weka image version and don't change, so no TTL is needed
type FeatureFlagsCacheService interface {
	// GetFeatureFlags returns the cached feature flags for the given image
	// Returns nil if not found
	GetFeatureFlags(ctx context.Context, image string) (*domain.FeatureFlags, error)

	// SetFeatureFlags sets the feature flags for the given image
	SetFeatureFlags(ctx context.Context, image string, flags *domain.FeatureFlags) error

	// DeleteFeatureFlags deletes the cached feature flags for the given image
	DeleteFeatureFlags(ctx context.Context, image string) error

	// Has checks if the feature flags for the given image are cached
	Has(ctx context.Context, image string) bool
}

type featureFlagsCacheService struct {
	// map of <image> to feature flags
	cache map[string]*domain.FeatureFlags
	lock  sync.RWMutex
}

func NewFeatureFlagsCacheService() FeatureFlagsCacheService {
	return &featureFlagsCacheService{
		cache: make(map[string]*domain.FeatureFlags),
	}
}

// ErrFeatureFlagsNotCached is returned when feature flags are not in cache
var ErrFeatureFlagsNotCached = fmt.Errorf("feature flags not cached")

func (s *featureFlagsCacheService) GetFeatureFlags(ctx context.Context, image string) (*domain.FeatureFlags, error) {
	_, logger, end := instrumentation.GetLogSpan(ctx, "GetFeatureFlags", "image", image)
	defer end()

	s.lock.RLock()
	defer s.lock.RUnlock()

	flags, ok := s.cache[image]
	if !ok {
		logger.Debug("Feature flags not found in cache")
		return nil, fmt.Errorf("%w for image: %s", ErrFeatureFlagsNotCached, image)
	}

	logger.Debug("Feature flags found in cache")
	return flags, nil
}

func (s *featureFlagsCacheService) SetFeatureFlags(ctx context.Context, image string, flags *domain.FeatureFlags) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "SetFeatureFlags", "image", image)
	defer end()

	s.lock.Lock()
	defer s.lock.Unlock()

	s.cache[image] = flags

	logger.Info("Feature flags cached", "image", image, "flags", flags)
	return nil
}

func (s *featureFlagsCacheService) DeleteFeatureFlags(ctx context.Context, image string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	delete(s.cache, image)
	return nil
}

func (s *featureFlagsCacheService) Has(ctx context.Context, image string) bool {
	s.lock.RLock()
	defer s.lock.RUnlock()

	_, ok := s.cache[image]
	return ok
}
