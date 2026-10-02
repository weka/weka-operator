package shutdown

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/paths"
)

func writeAllocation(t *testing.T, dir string, drives []string) {
	t.Helper()
	data, err := json.Marshal(allocationFile{Drives: drives})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resources.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitForCount(t *testing.T, get func() int32, want int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if get() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("count did not reach %d before timeout (have %d)", want, get())
}

func TestReleaseDrivesSucceedsWhenDriveReappears(t *testing.T) {
	dir := t.TempDir()
	writeAllocation(t, dir, []string{"A"})
	roots := paths.Roots{K8sRuntime: dir}
	fc := clock.NewFake(time.Unix(0, 0))

	var calls int32
	discover := func(context.Context) ([]domain.DriveInfo, error) {
		n := atomic.AddInt32(&calls, 1)
		if n >= 3 {
			return []domain.DriveInfo{{SerialId: "A"}}, nil
		}
		return nil, nil
	}

	done := make(chan error, 1)
	go func() {
		done <- ReleaseDrives(context.Background(), ReleaseInput{Clock: fc, Paths: roots, Discover: discover})
	}()

	for i := int32(1); i < 3; i++ {
		waitForCount(t, func() int32 { return atomic.LoadInt32(&calls) }, i, time.Second)
		fc.Advance(driveReleaseInterval)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReleaseDrives() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReleaseDrives did not return once drive reappeared")
	}
}

func TestReleaseDrivesReloadsAllocationFileEachIteration(t *testing.T) {
	dir := t.TempDir()
	writeAllocation(t, dir, []string{"A"})
	roots := paths.Roots{K8sRuntime: dir}
	fc := clock.NewFake(time.Unix(0, 0))

	var calls int32
	discover := func(context.Context) ([]domain.DriveInfo, error) {
		atomic.AddInt32(&calls, 1)
		return nil, nil // "A" never reappears via discovery
	}

	done := make(chan error, 1)
	go func() {
		done <- ReleaseDrives(context.Background(), ReleaseInput{Clock: fc, Paths: roots, Discover: discover})
	}()

	waitForCount(t, func() int32 { return atomic.LoadInt32(&calls) }, 1, time.Second)
	writeAllocation(t, dir, nil) // operator rewrites the file: nothing requested any more
	fc.Advance(driveReleaseInterval)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReleaseDrives() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReleaseDrives did not return after allocation file emptied")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("Discover called %d times, want 1 (should short-circuit once nothing is requested)", got)
	}
}

func TestReleaseDrivesExhaustsWithoutError(t *testing.T) {
	dir := t.TempDir()
	writeAllocation(t, dir, []string{"A"})
	roots := paths.Roots{K8sRuntime: dir}
	fc := clock.NewFake(time.Unix(0, 0))

	var calls int32
	discover := func(context.Context) ([]domain.DriveInfo, error) {
		atomic.AddInt32(&calls, 1)
		return nil, nil // "A" never reappears
	}

	done := make(chan error, 1)
	go func() {
		done <- ReleaseDrives(context.Background(), ReleaseInput{Clock: fc, Paths: roots, Discover: discover})
	}()

	for i := int32(1); i <= driveReleaseMaxChecks; i++ {
		waitForCount(t, func() int32 { return atomic.LoadInt32(&calls) }, i, time.Second)
		fc.Advance(driveReleaseInterval)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReleaseDrives() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReleaseDrives did not return after exhausting checks")
	}
	if got := atomic.LoadInt32(&calls); got != driveReleaseMaxChecks {
		t.Fatalf("calls = %d, want %d", got, driveReleaseMaxChecks)
	}
}
