package modes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/config"
)

// withMode temporarily registers fn under name (saving/restoring any existing entry) so a test
// can exercise Run without permanently mutating the package-global registry.
func withMode(t *testing.T, name string, fn ModeFunc) {
	t.Helper()
	prev, had := registry[name]
	registry[name] = fn
	t.Cleanup(func() {
		if had {
			registry[name] = prev
		} else {
			delete(registry, name)
		}
	})
}

func TestRun_OneShotModeBlocksUntilContextCancelled(t *testing.T) {
	// "discovery" is a one-shot mode: after the work returns nil, Run must block until ctx is
	// cancelled (the operator deleting the pod), mirroring Python run_forever().
	ranWork := make(chan struct{})
	withMode(t, "discovery", func(ctx context.Context, cfg *config.Config) error {
		close(ranWork)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, &config.Config{Mode: "discovery"}) }()

	<-ranWork // work completed

	select {
	case <-done:
		t.Fatal("Run returned before context was cancelled; one-shot mode must stay alive")
	case <-time.After(100 * time.Millisecond):
		// still blocking, as expected
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestRun_OneShotModeErrorDoesNotBlock(t *testing.T) {
	// On error, Run must propagate immediately (no run_forever), so the process exits non-zero.
	wantErr := errors.New("sign failed")
	withMode(t, "adhoc-op", func(ctx context.Context, cfg *config.Config) error {
		return wantErr
	})

	done := make(chan error, 1)
	// A live (uncancelled) context: if Run wrongly blocked, this would hang.
	go func() { done <- Run(context.Background(), &config.Config{Mode: "adhoc-op"}) }()

	select {
	case err := <-done:
		if !errors.Is(err, wantErr) {
			t.Fatalf("Run err = %v, want %v", err, wantErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run blocked on a failing one-shot mode; it must propagate the error and exit")
	}
}

func TestRun_LongRunningModeReturnsImmediately(t *testing.T) {
	// A non-one-shot mode (e.g. compute) returns only when its own work is done; Run must not
	// add any extra blocking.
	withMode(t, "compute", func(ctx context.Context, cfg *config.Config) error {
		return nil
	})

	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), &config.Config{Mode: "compute"}) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run blocked on a non-one-shot mode")
	}
}
