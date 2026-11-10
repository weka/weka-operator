package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/pkg/util/channelerr"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func TestResolveLeaderElection(t *testing.T) {
	defaults := DefaultConfigurationSettings()
	custom := defaults
	custom.LeaderElection = LeaderElectionSettings{LeaseDuration: 30 * time.Second, RenewDeadline: 20 * time.Second, RetryPeriod: 3 * time.Second}
	invalid := defaults
	invalid.LeaderElection.RenewDeadline = invalid.LeaderElection.LeaseDuration

	cases := map[string]struct {
		settings  ConfigurationSettings
		err       error
		want      LeaderElectionSettings
		wantRetry bool
	}{
		"read ok":                {settings: custom, want: custom.LeaderElection},
		"no policy":              {settings: defaults, want: defaults.LeaderElection},
		"invalid timings":        {settings: invalid, want: defaults.LeaderElection},
		"permanent error":        {err: errors.New("found 2 configuration WekaPolicies"), want: defaults.LeaderElection},
		"channel error persists": {err: channelerr.Wrap(errors.New("timeout")), want: slowAPILeaderElection, wantRetry: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			calls := 0
			read := func(context.Context) (ConfigurationSettings, error) { calls++; return tc.settings, tc.err }
			if got := resolveLeaderElection(context.Background(), logr.Discard(), read, 50*time.Millisecond, time.Millisecond); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if (calls > 1) != tc.wantRetry {
				t.Fatalf("%d calls, wantRetry=%v", calls, tc.wantRetry)
			}
		})
	}
}

func TestResolveLeaderElectionRecovers(t *testing.T) {
	calls := 0
	custom := DefaultConfigurationSettings()
	custom.LeaderElection.LeaseDuration = 30 * time.Second
	read := func(context.Context) (ConfigurationSettings, error) {
		calls++
		if calls < 3 {
			return ConfigurationSettings{}, channelerr.Wrap(errors.New("timeout"))
		}
		return custom, nil
	}
	got := resolveLeaderElection(context.Background(), logr.Discard(), read, time.Second, time.Millisecond)
	if got != custom.LeaderElection || calls != 3 {
		t.Fatalf("got %+v after %d calls", got, calls)
	}
}

// the deadline cancels the request in flight: context.DeadlineExceeded is not a channel error but must still end in slow timings
func TestResolveLeaderElectionLimitCancelsRequest(t *testing.T) {
	read := func(ctx context.Context) (ConfigurationSettings, error) {
		<-ctx.Done()
		return ConfigurationSettings{}, ctx.Err()
	}
	if got := resolveLeaderElection(context.Background(), logr.Discard(), read, 20*time.Millisecond, time.Millisecond); got != slowAPILeaderElection {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveLeaderElectionParentCancelled(t *testing.T) {
	channelErr := channelerr.Wrap(errors.New("slow"))
	cases := map[string]struct {
		read    func(context.Context, context.CancelFunc) (ConfigurationSettings, error)
		backoff time.Duration
	}{
		"during the read": {
			read: func(ctx context.Context, cancel context.CancelFunc) (ConfigurationSettings, error) {
				cancel()
				return ConfigurationSettings{}, ctx.Err()
			},
			backoff: time.Millisecond,
		},
		"during the backoff": {
			read: func(_ context.Context, cancel context.CancelFunc) (ConfigurationSettings, error) {
				time.AfterFunc(10*time.Millisecond, cancel)
				return ConfigurationSettings{}, channelErr
			},
			backoff: time.Hour,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			read := func(ctx context.Context) (ConfigurationSettings, error) { return tc.read(ctx, cancel) }
			if got := resolveLeaderElection(parent, logr.Discard(), read, time.Hour, tc.backoff); got != DefaultConfigurationSettings().LeaderElection {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

// the real client path against a misbehaving API server
func TestStartupLeaderElectionSettingsAPIServerFailures(t *testing.T) {
	prevNS := config.Config.OperatorPodNamespace
	config.Config.OperatorPodNamespace = "test-ns"
	t.Cleanup(func() { config.Config.OperatorPodNamespace = prevNS })
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	cases := map[string]http.HandlerFunc{
		"slower than the request timeout": func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		},
		"429": func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "slow down", http.StatusTooManyRequests) },
		"503": func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) },
		"504": func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "gateway", http.StatusGatewayTimeout) },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			got := startupLeaderElectionSettings(context.Background(), logr.Discard(), &rest.Config{Host: srv.URL}, scheme,
				time.Second, 100*time.Millisecond, 10*time.Millisecond)
			if got != slowAPILeaderElection {
				t.Fatalf("got %+v", got)
			}
		})
	}
}
