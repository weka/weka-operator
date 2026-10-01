package resources

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/paths"
)

func testRoots(t *testing.T) paths.Roots {
	return paths.Roots{K8sRuntime: t.TempDir()}
}

func withFastRetry(t *testing.T) {
	orig := retryInterval
	retryInterval = time.Millisecond
	t.Cleanup(func() { retryInterval = orig })
}

func TestWaitAndLoad_AbortsOnShutdown(t *testing.T) {
	withFastRetry(t)
	p := testRoots(t) // resources.json never appears

	_, err := WaitAndLoad(context.Background(), clock.System, p, func() bool { return true })
	if err == nil {
		t.Fatal("expected error when shutdown requested, got nil")
	}
	if !strings.Contains(err.Error(), "shutdown") {
		t.Errorf("error = %q, want it to mention shutdown", err.Error())
	}
}

func TestWaitAndLoad_CtxCancel(t *testing.T) {
	withFastRetry(t)
	p := testRoots(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancelled

	_, err := WaitAndLoad(ctx, clock.System, p, func() bool { return false })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestWaitAndLoad_RetriesMalformedFile(t *testing.T) {
	withFastRetry(t)
	p := testRoots(t)
	resPath := filepath.Join(p.K8sRuntime, resourcesFile)
	if err := os.WriteFile(resPath, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := WaitAndLoad(context.Background(), clock.System, p, nil)
	if err == nil {
		t.Fatal("expected error after exhausting retries on malformed JSON, got nil")
	}
	if want := "failed to read valid JSON"; !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %q, want it to contain %q", err.Error(), want)
	}
}
