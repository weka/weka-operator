package resources

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWaitAndLoad_AbortsOnShutdown(t *testing.T) {
	orig := retryInterval
	retryInterval = time.Millisecond
	defer func() { retryInterval = orig }()

	// resourcesPath won't exist in the test environment, so phase 1 loops,
	// sleeps retryInterval, then shouldAbort returns true → aborts with error.
	_, err := WaitAndLoad(context.Background(), func() bool { return true })
	if err == nil {
		t.Fatal("expected error when shutdown requested, got nil")
	}
	if !strings.Contains(err.Error(), "shutdown") {
		t.Errorf("error = %q, want it to mention shutdown", err.Error())
	}
}

func TestWaitAndLoad_CtxCancel(t *testing.T) {
	orig := retryInterval
	retryInterval = time.Millisecond
	defer func() { retryInterval = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancelled

	_, err := WaitAndLoad(ctx, func() bool { return false })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
