package runtimes

import (
	"context"
	"errors"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/lifecycle"
)

func TestTaskRuntime_RunsOnceAndReturns(t *testing.T) {
	calls := 0
	rt := NewTask("adhoc", func(context.Context) error {
		calls++
		return nil
	})
	var _ lifecycle.ModeRuntime = rt

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start returned %v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1", calls)
	}
	if err := rt.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned %v, want nil", err)
	}
}

func TestTaskRuntime_PropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	rt := NewTask("adhoc", func(context.Context) error {
		return wantErr
	})
	if err := rt.Start(context.Background()); err != wantErr {
		t.Fatalf("Start returned %v, want %v", err, wantErr)
	}
}
