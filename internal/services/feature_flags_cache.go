package services

import (
	"context"
	"sync"

	"github.com/weka/go-weka-observability/instrumentation"

	"github.com/weka/weka-operator/internal/pkg/domain"
)

// make this service globally available
var FeatureFlagsCache FeatureFlagsCacheService

func init() {
	FeatureFlagsCache = NewFeatureFlagsCacheService()
}

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

func (s *featureFlagsCacheService) GetFeatureFlags(ctx context.Context, image string) (*domain.FeatureFlags, error) {
	_, logger, end := instrumentation.GetLogSpan(ctx, "GetFeatureFlags", "image", image)
	defer end()

	s.lock.RLock()
	defer s.lock.RUnlock()

	flags, ok := s.cache[image]
	if !ok {
		logger.Debug("Feature flags not found in cache")
		return nil, nil
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
