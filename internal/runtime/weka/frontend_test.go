package weka

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/paths"
)

func writeInterfaceFile(t *testing.T, roots paths.Roots, content string) {
	dir := filepath.Join(roots.Proc, "wekafs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "interface"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWaitFrontendDisconnectMissingFile(t *testing.T) {
	roots := paths.Roots{Proc: t.TempDir()}
	fc := clock.NewFake(time.Unix(0, 0))
	if err := WaitFrontendDisconnect(context.Background(), fc, &roots, "envoy"); err != nil {
		t.Errorf("unexpected error with no interface file: %v", err)
	}
}

func TestWaitFrontendDisconnectSucceedsOnceDisconnected(t *testing.T) {
	roots := paths.Roots{Proc: t.TempDir()}
	writeInterfaceFile(t, roots, "Container=envoy Connected frontend\n")
	fc := clock.NewFake(time.Unix(0, 0))

	done := make(chan error, 1)
	go func() { done <- WaitFrontendDisconnect(context.Background(), fc, &roots, "envoy") }()

	// Let the goroutine reach its clock.After(5s) call before Advance is applied.
	time.Sleep(200 * time.Millisecond)
	writeInterfaceFile(t, roots, "Container=envoy Disconnected\n")
	fc.Advance(5 * time.Second)

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitFrontendDisconnect did not return after disconnecting")
	}
}

func TestWaitFrontendDisconnectTimesOut(t *testing.T) {
	roots := paths.Roots{Proc: t.TempDir()}
	writeInterfaceFile(t, roots, "Container=envoy Connected frontend\n")
	fc := clock.NewFake(time.Unix(0, 0))

	done := make(chan error, 1)
	go func() { done <- WaitFrontendDisconnect(context.Background(), fc, &roots, "envoy") }()

	for i := 0; i < 25; i++ {
		time.Sleep(50 * time.Millisecond)
		fc.Advance(5 * time.Second)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected timeout error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitFrontendDisconnect did not return after 120s of fake time")
	}
}
