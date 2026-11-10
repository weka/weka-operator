package operations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
)

func TestWaitForCachedFeatureFlags(t *testing.T) {
	ctx := context.Background()
	image := "quay.io/weka.io/weka-in-container:test-wait-cached"
	t.Cleanup(func() {
		_ = services.FeatureFlagsCache.DeleteFeatureFlags(ctx, image) //nolint:errcheck // test cleanup
	})

	_, err := waitForCachedFeatureFlags(ctx, image)
	var waitErr *lifecycle.WaitError
	if !errors.As(err, &waitErr) {
		t.Fatalf("expected WaitError before the ad-hoc container caches, got %v", err)
	}
	if waitErr.Duration < 15*time.Second {
		t.Fatalf("expected at least 15s requeue, got %s", waitErr.Duration)
	}

	if setErr := services.SetFeatureFlags(ctx, image, &domain.FeatureFlags{TracesOverridePartialSupport: true}); setErr != nil {
		t.Fatal(setErr)
	}
	flags, err := waitForCachedFeatureFlags(ctx, image)
	if err != nil {
		t.Fatal(err)
	}
	if !flags.TracesOverridePartialSupport {
		t.Fatalf("expected cached flags, got %+v", flags)
	}
}
