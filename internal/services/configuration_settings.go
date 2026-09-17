package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/pkg/util"
)

const (
	// settingsWaitTimeout bounds the wait for the very first resolution, before anything is known
	// about the configuration. It only covers the startup race between reconcilers beginning and
	// RunSettings completing its first pass; once an attempt has been made, callers never wait.
	settingsWaitTimeout = 60 * time.Second
	// settingsRefreshInterval re-resolves in the background. The policy reconciler pushes on every
	// change, so this is a safety net: without it, a configuration that failed to resolve would
	// stay unusable until someone happened to edit a policy.
	settingsRefreshInterval = 30 * time.Second
)

var settings = newSettingsService()

// DriversSettings holds the operator-wide settings for building and distributing drivers.
type DriversSettings struct {
	// ForceBuilderCli takes the weka CLI from the builder image regardless of what the cluster
	// image's feature flags report.
	ForceBuilderCli bool
}

// ConfigurationSettings is the effective operator-wide configuration: built-in defaults with any
// configuration WekaPolicy applied over them. Every field is a resolved value, never a pointer, so
// callers never repeat the nil-means-default decision.
type ConfigurationSettings struct {
	Drivers DriversSettings
}

// DefaultConfigurationSettings returns the built-in defaults, which apply when no configuration
// WekaPolicy exists.
func DefaultConfigurationSettings() ConfigurationSettings {
	return ConfigurationSettings{
		Drivers: DriversSettings{
			ForceBuilderCli: false,
		},
	}
}

// SettingsFromPayload applies a configuration payload over the built-in defaults. A nil payload, a
// nil section or a nil field each keep the default, so an unset field stays distinguishable from
// an explicit false.
func SettingsFromPayload(payload *weka.ConfigurationPayload) ConfigurationSettings {
	resolved := DefaultConfigurationSettings()
	if payload == nil {
		return resolved
	}

	if payload.Drivers != nil && payload.Drivers.ForceBuilderCli != nil {
		resolved.Drivers.ForceBuilderCli = *payload.Drivers.ForceBuilderCli
	}

	return resolved
}

// GetSettings returns the effective operator-wide configuration.
//
// It blocks until the configuration has been resolved, and panics if that has not happened within
// settingsWaitTimeout or before ctx is done. There is deliberately no error return and no fallback:
// a caller cannot sensibly proceed on a guess, and serving built-in defaults in place of a
// configuration that exists but cannot be read is how an operator silently changes behaviour
// across a restart. Callers run inside reconcilers, where controller-runtime recovers the panic
// into an error and a requeue.
func GetSettings(ctx context.Context) ConfigurationSettings {
	return settings.get(ctx)
}

// RefreshSettings re-resolves the configuration immediately and reports whether the result is
// usable. The WekaPolicy controller calls it on every change, and reports the error on the object.
func RefreshSettings(ctx context.Context, c client.Reader) error {
	return settings.refresh(ctx, c)
}

// RunSettings resolves the configuration and keeps it current until ctx is done. It must be
// started before reconcilers can make progress, since GetSettings blocks until the first
// successful resolution.
func RunSettings(ctx context.Context, c client.Reader) {
	settings.run(ctx, c)
}

type settingsService struct {
	lock     sync.RWMutex
	resolved ConfigurationSettings
	// ready is closed once resolved holds a usable value, and replaced with a fresh open channel
	// whenever the configuration stops being resolvable.
	ready chan struct{}
	// attempted is closed after the first resolution finishes, whatever its outcome. It separates
	// "nothing has tried yet" from "we tried and it is not usable", which is the difference
	// between waiting and failing immediately.
	attempted chan struct{}
	// lastErr is why the most recent resolution failed, reported to callers that cannot proceed.
	lastErr error
}

func newSettingsService() *settingsService {
	return &settingsService{
		ready:     make(chan struct{}),
		attempted: make(chan struct{}),
	}
}

// snapshot reports the current settings and whether they are usable.
func (s *settingsService) snapshot() (ConfigurationSettings, bool, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	select {
	case <-s.ready:
		return s.resolved, true, nil
	default:
		return ConfigurationSettings{}, false, s.lastErr
	}
}

func (s *settingsService) get(ctx context.Context) ConfigurationSettings {
	if resolved, ok, _ := s.snapshot(); ok {
		return resolved
	}

	s.lock.RLock()
	ready, attempted := s.ready, s.attempted
	s.lock.RUnlock()

	// A resolution has already run and the configuration is not usable. Waiting cannot change
	// that, and callers run on a shared pool of reconcile workers - parking one for the full
	// timeout would starve every other object the controller serves.
	select {
	case <-attempted:
		return s.resolvedOrPanic()
	default:
	}

	// Nothing has been resolved yet: this is the startup race between reconcilers starting and
	// the first resolution finishing, and it is the only case worth waiting out.
	timeout := time.NewTimer(settingsWaitTimeout)
	defer timeout.Stop()

	select {
	case <-ready:
		return s.resolvedOrPanic()
	case <-attempted:
		return s.resolvedOrPanic()
	case <-ctx.Done():
		panic(fmt.Sprintf("operator configuration is not resolved and the context ended while waiting: %v", ctx.Err()))
	case <-timeout.C:
		panic(fmt.Sprintf("operator configuration was still unresolved after %s; check the configuration WekaPolicy in the operator namespace", settingsWaitTimeout))
	}
}

// resolvedOrPanic returns the settings if they are usable, and otherwise panics naming why. The
// panic carries the resolution error rather than a generic timeout, so the log points straight at
// the offending policy.
func (s *settingsService) resolvedOrPanic() ConfigurationSettings {
	resolved, ok, err := s.snapshot()
	if ok {
		return resolved
	}
	panic(fmt.Sprintf("operator configuration is unavailable: %v; check the configuration WekaPolicy in the operator namespace", err))
}

func (s *settingsService) run(ctx context.Context, c client.Reader) {
	_ = s.refresh(ctx, c) //nolint:errcheck // logged by the resolver; the ticker below retries

	ticker := time.NewTicker(settingsRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.refresh(ctx, c) //nolint:errcheck // logged by the resolver; the next tick retries
		}
	}
}

func (s *settingsService) refresh(ctx context.Context, c client.Reader) error {
	resolved, err := resolveConfigurationSettings(ctx, c)
	if err != nil {
		s.markUnresolved(err)
	} else {
		s.store(resolved)
	}

	s.lock.Lock()
	select {
	case <-s.attempted:
	default:
		close(s.attempted)
	}
	s.lock.Unlock()

	return err
}

func (s *settingsService) store(resolved ConfigurationSettings) {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.resolved = resolved
	s.lastErr = nil
	select {
	case <-s.ready:
	default:
		close(s.ready)
	}
}

// markUnresolved makes subsequent callers block again. The previous value is deliberately dropped
// rather than kept as a fallback: holding a last-known-good value is what makes a running operator
// behave differently from a restarted one.
func (s *settingsService) markUnresolved(cause error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.lastErr = cause
	select {
	case <-s.ready:
		s.ready = make(chan struct{})
	default:
	}
}

// resolveConfigurationSettings reads the configuration WekaPolicy from the operator's own
// namespace. These are install-wide settings, so a policy in a tenant namespace must not shadow
// them.
//
// An error means no single valid configuration could be derived and nothing should be served.
// Finding no configuration policy at all is not an error: nobody asked for anything, so the
// built-in defaults are the answer.
func resolveConfigurationSettings(ctx context.Context, c client.Reader) (ConfigurationSettings, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ResolveConfigurationSettings")
	defer logger.End()

	namespace, err := util.GetPodNamespace()
	if err != nil {
		logger.Error(err, "Cannot resolve the operator namespace, configuration is unavailable")
		return ConfigurationSettings{}, err
	}

	policyList := &weka.WekaPolicyList{}
	if err := c.List(ctx, policyList, &client.ListOptions{Namespace: namespace}); err != nil {
		logger.Error(err, "Failed to list WekaPolicies, configuration is unavailable", "namespace", namespace)
		return ConfigurationSettings{}, err
	}

	var matches []*weka.ConfigurationPayload
	var names []string
	for i := range policyList.Items {
		policy := &policyList.Items[i]
		if policy.Spec.Payload.Configuration == nil {
			// not a configuration policy; a malformed policy of some other kind is that
			// controller's problem, not ours
			continue
		}

		// the policy controller refuses a payload combination GetType cannot resolve, so honouring
		// it here would apply settings from an object that is not running
		if _, _, typeErr := policy.GetType(); typeErr != nil {
			logger.Error(typeErr, "Configuration WekaPolicy is not usable, configuration is unavailable",
				"namespace", namespace, "policy", policy.Name)
			return ConfigurationSettings{}, typeErr
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
		err := fmt.Errorf("found %d configuration WekaPolicies (%v) in namespace %s, expected at most one", len(matches), names, namespace)
		logger.Error(err, "Cannot choose between configuration WekaPolicies, configuration is unavailable")
		return ConfigurationSettings{}, err
	}
}
