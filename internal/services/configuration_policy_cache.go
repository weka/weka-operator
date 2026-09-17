package services

import (
	"context"
	"sync"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/pkg/util"
)

// configurationCacheTTL bounds how stale the operator-wide configuration may be: the policy is
// re-read at most once per this interval, so an edit takes effect within it.
const configurationCacheTTL = 30 * time.Second

// make this service globally available
var ConfigurationCache ConfigurationCacheService

func init() {
	ConfigurationCache = NewConfigurationCacheService()
}

// DriversSettings holds the operator-wide settings for building and distributing drivers.
type DriversSettings struct {
	// ForceBuilderCli takes the weka CLI from the builder image regardless of what the cluster
	// image's feature flags report.
	ForceBuilderCli bool
}

// ConfigurationSettings holds the operator-wide settings carried by a configuration WekaPolicy.
type ConfigurationSettings struct {
	Drivers DriversSettings
}

// DefaultConfigurationSettings returns the built-in defaults, used whenever no single configuration
// policy can be resolved.
func DefaultConfigurationSettings() ConfigurationSettings {
	return ConfigurationSettings{
		Drivers: DriversSettings{
			ForceBuilderCli: false,
		},
	}
}

// SettingsFromPayload maps a configuration payload onto settings. A nil payload, a nil section or a
// nil field each keep the built-in default, so an unset field stays distinguishable from an
// explicit false.
func SettingsFromPayload(payload *weka.ConfigurationPayload) ConfigurationSettings {
	settings := DefaultConfigurationSettings()
	if payload == nil {
		return settings
	}

	if payload.Drivers != nil && payload.Drivers.ForceBuilderCli != nil {
		settings.Drivers.ForceBuilderCli = *payload.Drivers.ForceBuilderCli
	}

	return settings
}

type ConfigurationCacheService interface {
	// GetSettings returns the operator-wide configuration, refreshing it from the cluster when the
	// cached copy is older than the TTL. It never fails: any problem resolving the policy yields
	// the built-in defaults.
	GetSettings(ctx context.Context, c client.Client) ConfigurationSettings
	// Invalidate drops the cached copy so the next GetSettings re-reads.
	Invalidate()
}

type configurationCacheService struct {
	settings  ConfigurationSettings
	updatedAt time.Time
	ttl       time.Duration
	lock      sync.RWMutex
}

func NewConfigurationCacheService() ConfigurationCacheService {
	return &configurationCacheService{
		settings: DefaultConfigurationSettings(),
		ttl:      configurationCacheTTL,
	}
}

func (s *configurationCacheService) GetSettings(ctx context.Context, c client.Client) ConfigurationSettings {
	s.lock.RLock()
	if s.fresh() {
		settings := s.settings
		s.lock.RUnlock()
		return settings
	}
	s.lock.RUnlock()

	s.lock.Lock()
	defer s.lock.Unlock()

	// another goroutine may have refreshed while we waited for the write lock, so a burst of
	// reconciles costs a single list
	if s.fresh() {
		return s.settings
	}

	settings, err := resolveConfigurationSettings(ctx, c)
	if err != nil {
		// leave updatedAt alone so the next caller retries instead of pinning defaults for a
		// whole TTL. Matters most at operator startup, before the informer has synced, which is
		// when drivers-builder pods are created.
		return DefaultConfigurationSettings()
	}

	s.settings = settings
	s.updatedAt = time.Now()
	return s.settings
}

func (s *configurationCacheService) Invalidate() {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.updatedAt = time.Time{}
}

// fresh reports whether the cached copy is still within the TTL. Callers must hold the lock.
func (s *configurationCacheService) fresh() bool {
	return !s.updatedAt.IsZero() && time.Since(s.updatedAt) < s.ttl
}

// resolveConfigurationSettings reads the configuration policy from the operator's own namespace.
// These are install-wide settings, so a policy in a tenant namespace must not shadow them.
// The type is derived through GetType so this agrees with what the policy controller dispatches:
// a policy the controller rejects must not contribute settings, nor count towards ambiguity.
//
// An error means the configuration could not be read and the result should not be cached. Having
// no policy at all is not an error: it resolves to the built-in defaults.
func resolveConfigurationSettings(ctx context.Context, c client.Client) (ConfigurationSettings, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ResolveConfigurationSettings")
	defer logger.End()

	namespace, err := util.GetPodNamespace()
	if err != nil {
		logger.Error(err, "Cannot resolve the operator namespace, falling back to default configuration")
		return DefaultConfigurationSettings(), err
	}

	policyList := &weka.WekaPolicyList{}
	if err := c.List(ctx, policyList, &client.ListOptions{Namespace: namespace}); err != nil {
		logger.Error(err, "Failed to list WekaPolicies, falling back to default configuration", "namespace", namespace)
		return DefaultConfigurationSettings(), err
	}

	var matches []*weka.ConfigurationPayload
	var names []string
	for i := range policyList.Items {
		policy := &policyList.Items[i]
		_, isConfiguration, typeErr := policy.GetType()
		if typeErr != nil || !isConfiguration {
			continue
		}
		matches = append(matches, policy.Spec.Payload.Configuration)
		names = append(names, policy.Name)
	}

	switch len(matches) {
	case 0:
		return DefaultConfigurationSettings(), nil
	case 1:
		// SettingsFromPayload copies the values out, so nothing aliases the informer cache
		return SettingsFromPayload(matches[0]), nil
	default:
		// ignoring every one of them beats silently applying an arbitrary winner
		logger.Info("Multiple configuration WekaPolicies found, ignoring all and using default configuration",
			"namespace", namespace, "policies", names)
		return DefaultConfigurationSettings(), nil
	}
}
