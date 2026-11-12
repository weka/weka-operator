package services

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"testing"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func readReturning(settings ConfigurationSettings, err error) func(context.Context) (ConfigurationSettings, error) {
	return func(context.Context) (ConfigurationSettings, error) { return settings, err }
}

func TestResolveLeaderElection(t *testing.T) {
	defaults := DefaultConfigurationSettings()
	custom := defaults
	custom.LeaderElection = LeaderElectionSettings{LeaseDuration: 30 * time.Second, RenewDeadline: 20 * time.Second, RetryPeriod: 3 * time.Second}
	invalid := defaults
	invalid.LeaderElection.RenewDeadline = invalid.LeaderElection.LeaseDuration

	gr := schema.GroupResource{Group: "weka.weka.io", Resource: "wekapolicies"}
	cases := map[string]struct {
		settings ConfigurationSettings
		err      error
		want     LeaderElectionSettings
	}{
		"read ok":              {settings: custom, want: custom.LeaderElection},
		"no policy":            {settings: defaults, want: defaults.LeaderElection},
		"forbidden":            {err: apierrors.NewForbidden(gr, "x", errors.New("no")), want: defaults.LeaderElection},
		"other error":          {err: errors.New("found 2 configuration WekaPolicies"), want: defaults.LeaderElection},
		"invalid timings":      {settings: invalid, want: defaults.LeaderElection},
		"server timeout":       {err: apierrors.NewServerTimeout(gr, "list", 1), want: SlowAPILeaderElection()},
		"too many requests":    {err: apierrors.NewTooManyRequests("slow down", 1), want: SlowAPILeaderElection()},
		"service unavailable":  {err: apierrors.NewServiceUnavailable("down"), want: SlowAPILeaderElection()},
		"deadline exceeded":    {err: context.DeadlineExceeded, want: SlowAPILeaderElection()},
		"x509 in url error":    {err: &url.Error{Op: "Get", Err: x509.UnknownAuthorityError{}}, want: defaults.LeaderElection},
		"canceled":             {err: context.Canceled, want: defaults.LeaderElection},
		"refused in url error": {err: &url.Error{Op: "Get", Err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}}, want: SlowAPILeaderElection()},
		"connection error":     {err: &net.OpError{Op: "dial", Err: syscall.ECONNRESET}, want: SlowAPILeaderElection()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := resolveLeaderElection(context.Background(), logr.Discard(), readReturning(tc.settings, tc.err), 50*time.Millisecond, time.Millisecond)
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
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
			return ConfigurationSettings{}, context.DeadlineExceeded
		}
		return custom, nil
	}
	got := resolveLeaderElection(context.Background(), logr.Discard(), read, time.Second, time.Millisecond)
	if got != custom.LeaderElection || calls != 3 {
		t.Fatalf("got %+v after %d calls", got, calls)
	}
}

func TestServeLivenessUntilManager(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	stop := ServeLivenessUntilManager(logr.Discard(), addr)
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status %d", resp.StatusCode)
	}
	stop()
	ln, err = net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("address not released: %v", err)
	}
	ln.Close()
}
