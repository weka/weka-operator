package debugexit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/clock"
)

func TestWaitReturnsEarlyOnCancelMarker(t *testing.T) {
	tmp := t.TempDir()
	fc := clock.NewFake(time.Unix(0, 0))

	done := make(chan struct{})
	go func() {
		Wait(context.Background(), fc, tmp, 5*time.Second)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond) // let it block on iteration 1's After(1s)
	if err := os.WriteFile(filepath.Join(tmp, ".cancel-debug-sleep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fc.Advance(time.Second) // unblocks iteration 1's wait; iteration 2 sees the marker

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after cancel marker appeared")
	}
}

func TestWaitIgnoresCtxCancellation(t *testing.T) {
	tmp := t.TempDir()
	fc := clock.NewFake(time.Unix(0, 0))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before Wait even starts

	done := make(chan struct{})
	go func() {
		Wait(ctx, fc, tmp, 2*time.Second)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("Wait returned despite cancelled ctx, no marker, and duration not yet elapsed")
	default:
	}

	fc.Advance(time.Second)
	time.Sleep(20 * time.Millisecond)
	fc.Advance(time.Second)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return once its duration elapsed, despite cancelled ctx")
	}
}
