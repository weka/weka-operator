package services

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	leaderElectionReadLimit   = 2 * time.Minute
	leaderElectionReadBackoff = time.Second
	leaderElectionMaxBackoff  = 10 * time.Second
	leaderElectionReadTimeout = 10 * time.Second
)

// ServeLivenessUntilManager answers /healthz on the manager's probe address while the startup read of the
// configuration runs, so a long read does not fail the liveness probe. /readyz is deliberately not served: the
// pod stays unready until the manager is up. The returned stop closes the listener before the manager binds the
// same address.
func ServeLivenessUntilManager(logger logr.Logger, addr string) (stop func()) {
	if addr == "" || addr == "0" {
		return func() {}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		logger.Error(err, "cannot serve /healthz during the startup configuration read", "addr", addr)
		return func() {}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }) //nolint:errcheck // probe response, nothing to do on failure
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error(err, "startup /healthz server failed")
		}
	}()
	return func() {
		if err := srv.Close(); err != nil {
			logger.Error(err, "failed to stop the startup /healthz server")
		}
	}
}

// StartupLeaderElectionSettings reads the lease timings from the configuration WekaPolicy before the manager
// exists. client-go refuses inconsistent timings at mgr.Start, so an unusable policy falls back to the defaults
// instead of crash-looping the operator. A read that keeps failing on a transient error until limit means a slow
// API server, and gets the slow-API timings.
func StartupLeaderElectionSettings(ctx context.Context, logger logr.Logger, restConfig *rest.Config, scheme *runtime.Scheme) LeaderElectionSettings {
	// the REST mapper's discovery requests don't take ctx, so bound each request as well
	cfg := rest.CopyConfig(restConfig)
	cfg.Timeout = leaderElectionReadTimeout
	read := func(ctx context.Context) (ConfigurationSettings, error) {
		c, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			return ConfigurationSettings{}, err
		}
		return ResolveConfigurationSettings(ctx, c)
	}
	return resolveLeaderElection(ctx, logger, read, leaderElectionReadLimit, leaderElectionReadBackoff)
}

func resolveLeaderElection(ctx context.Context, logger logr.Logger, read func(context.Context) (ConfigurationSettings, error), limit, backoff time.Duration) LeaderElectionSettings {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	var err error
	for {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, leaderElectionReadTimeout)
		var resolved ConfigurationSettings
		resolved, err = read(attemptCtx)
		attemptCancel()
		if err == nil {
			le := resolved.LeaderElection
			if validLeaderElection(le) {
				logger.Info("leader-election settings resolved at startup",
					"leaseDuration", le.LeaseDuration, "renewDeadline", le.RenewDeadline, "retryPeriod", le.RetryPeriod)
				return le
			}
			err = fmt.Errorf("need leaseDuration (whole seconds, at most %d s) > renewDeadline > %.1f x retryPeriod > 0, got %s/%s/%s",
				math.MaxInt32, leaderelection.JitterFactor, le.LeaseDuration, le.RenewDeadline, le.RetryPeriod)
			break
		}
		if !isTransient(err) {
			break
		}
		logger.Error(err, "reading the configuration WekaPolicy failed, retrying", "retryIn", backoff)
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		if ctx.Err() != nil {
			break
		}
		backoff = min(backoff*2, leaderElectionMaxBackoff)
	}

	if isTransient(err) {
		le := SlowAPILeaderElection()
		logger.Error(err, "configuration WekaPolicy could not be read before the limit, assuming a slow API server, using slow-API leader-election settings",
			"source", "slow-api", "limit", limit, "leaseDuration", le.LeaseDuration, "renewDeadline", le.RenewDeadline, "retryPeriod", le.RetryPeriod)
		return le
	}
	le := DefaultConfigurationSettings().LeaderElection
	logger.Error(err, "leader-election settings from the configuration WekaPolicy not usable, using defaults",
		"source", "defaults", "leaseDuration", le.LeaseDuration, "renewDeadline", le.RenewDeadline, "retryPeriod", le.RetryPeriod)
	return le
}

// followers judge expiry from the Lease's whole-second leaseDurationSeconds, so the truncated lease must
// outlast renewDeadline or a follower can take over while the leader still acts; also rules out a 0s lease.
// That field is int32, so a longer lease overflows and every lease write is rejected.
func validLeaderElection(le LeaderElectionSettings) bool {
	return le.RetryPeriod > 0 && le.LeaseDuration.Truncate(time.Second) > le.RenewDeadline &&
		le.LeaseDuration <= math.MaxInt32*time.Second &&
		float64(le.RenewDeadline) > leaderelection.JitterFactor*float64(le.RetryPeriod)
}

// isTransient reports an error of the channel to the API server (timeouts, overload, 5xx, connection
// failures) as opposed to an answer the API server gave on purpose (Forbidden, a bad policy) or a failure
// that retrying cannot fix (TLS, cancellation).
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		code := status.Status().Code
		return code >= 500 || code == http.StatusTooManyRequests
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	var netErr net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET)
}
