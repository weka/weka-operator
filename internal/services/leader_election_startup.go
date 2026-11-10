package services

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/pkg/util/channelerr"
)

const (
	// a request in flight at the deadline adds up to readTimeout; the total stays inside the manager liveness probe
	// budget: initialDelaySeconds 15 + (failureThreshold 3 - 1) x periodSeconds 20 = 55s
	leaderElectionReadLimit   = 30 * time.Second
	leaderElectionReadTimeout = 10 * time.Second
	leaderElectionReadBackoff = 2 * time.Second
)

var slowAPILeaderElection = LeaderElectionSettings{
	LeaseDuration: 60 * time.Second,
	RenewDeadline: 40 * time.Second,
	RetryPeriod:   5 * time.Second,
}

// StartupLeaderElectionSettings reads the lease timings from the configuration WekaPolicy before the manager
// exists. client-go refuses inconsistent timings at mgr.Start, so inconsistent timings fall back to the defaults
// instead of crash-looping the operator. A read that keeps failing on the channel to the API server until the
// limit means a slow API server, and gets longer timings so that lease renewals survive it. Any other read error
// falls back to the defaults at once.
// It lives in internal/services because the image build compiles cmd/manager/main.go alone.
func StartupLeaderElectionSettings(ctx context.Context, logger logr.Logger, restConfig *rest.Config, scheme *runtime.Scheme) LeaderElectionSettings {
	return startupLeaderElectionSettings(ctx, logger, restConfig, scheme, leaderElectionReadLimit, leaderElectionReadTimeout, leaderElectionReadBackoff)
}

func startupLeaderElectionSettings(ctx context.Context, logger logr.Logger, restConfig *rest.Config, scheme *runtime.Scheme, limit, requestTimeout, backoff time.Duration) LeaderElectionSettings {
	// the REST mapper's discovery requests don't take ctx, so bound each request as well
	cfg := rest.CopyConfig(restConfig)
	cfg.Timeout = requestTimeout
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		logger.Error(err, "cannot create a client to read the configuration WekaPolicy, using default leader-election settings")
		return DefaultConfigurationSettings().LeaderElection
	}
	return resolveLeaderElection(ctx, logger, func(ctx context.Context) (ConfigurationSettings, error) {
		return ResolveConfigurationSettings(ctx, c)
	}, limit, backoff)
}

func resolveLeaderElection(parent context.Context, logger logr.Logger, read func(context.Context) (ConfigurationSettings, error), limit, backoff time.Duration) LeaderElectionSettings {
	ctx, cancel := context.WithTimeout(parent, limit)
	defer cancel()

	for {
		resolved, err := read(ctx)
		if err == nil {
			le := resolved.LeaderElection
			// followers judge expiry from the Lease's whole-second leaseDurationSeconds, so the truncated lease must
			// outlast renewDeadline or a follower can take over while the leader still acts; also rules out a 0s lease.
			// That field is int32, so a longer lease overflows and every lease write is rejected.
			if le.RetryPeriod > 0 && le.LeaseDuration.Truncate(time.Second) > le.RenewDeadline &&
				le.LeaseDuration <= math.MaxInt32*time.Second &&
				float64(le.RenewDeadline) > leaderelection.JitterFactor*float64(le.RetryPeriod) {
				return le
			}
			err = fmt.Errorf("need leaseDuration (whole seconds, at most %d s) > renewDeadline > %.1f x retryPeriod > 0, got %s/%s/%s",
				math.MaxInt32, leaderelection.JitterFactor, le.LeaseDuration, le.RenewDeadline, le.RetryPeriod)
			logger.Error(err, "leader-election settings from the configuration WekaPolicy not usable, using defaults")
			return DefaultConfigurationSettings().LeaderElection
		}
		if parent.Err() != nil {
			return DefaultConfigurationSettings().LeaderElection
		}
		// channelerr.Is excludes context.DeadlineExceeded, but here it is the per-request timeout or our own limit
		// cancelling the request in flight (parent expiry returned above), i.e. a slow API server
		if !channelerr.Is(err) && !errors.Is(err, context.DeadlineExceeded) {
			logger.Error(err, "configuration WekaPolicy could not be read, using default leader-election settings")
			return DefaultConfigurationSettings().LeaderElection
		}
		logger.Info("reading the configuration WekaPolicy failed, retrying", "error", err.Error(), "retryIn", backoff)
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return DefaultConfigurationSettings().LeaderElection
			}
			logger.Error(err, "configuration WekaPolicy could not be read within the limit, using slow-API leader-election settings", "limit", limit)
			return slowAPILeaderElection
		case <-time.After(backoff):
		}
	}
}
