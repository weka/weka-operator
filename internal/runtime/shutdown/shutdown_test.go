package shutdown

import (
	"context"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/pkg/domain"
)

// drivesFromSerials builds a fake FindWekaPartitions result from serial ids.
func drivesFromSerials(serials ...string) []domain.DriveInfo {
	out := make([]domain.DriveInfo, 0, len(serials))
	for _, s := range serials {
		out = append(out, domain.DriveInfo{SerialId: s})
	}
	return out
}

// TestWaitForDriveRelease covers the three behaviors of the drive-release loop:
// immediate return on empty input, waiting until ALL requested serials reappear,
// and non-fatal return on timeout. Mirrors weka_runtime.py:4565–4590.
func TestWaitForDriveRelease(t *testing.T) {
	origFind := findWekaPartitionsFn
	origInterval := driveReleasePollInterval
	defer func() {
		findWekaPartitionsFn = origFind
		driveReleasePollInterval = origInterval
	}()
	driveReleasePollInterval = time.Millisecond

	t.Run("empty serials returns immediately", func(t *testing.T) {
		called := false
		findWekaPartitionsFn = func(_ context.Context) ([]domain.DriveInfo, error) {
			called = true
			return nil, nil
		}
		if err := WaitForDriveRelease(context.Background(), nil, time.Second); err != nil {
			t.Fatalf("WaitForDriveRelease(nil) = %v, want nil", err)
		}
		if called {
			t.Error("findWekaPartitionsFn should not be called for empty requested serials")
		}
	})

	t.Run("returns nil only once all requested serials are present", func(t *testing.T) {
		// First poll: only one of two serials present. Second poll: both present.
		poll := 0
		findWekaPartitionsFn = func(_ context.Context) ([]domain.DriveInfo, error) {
			poll++
			if poll == 1 {
				return drivesFromSerials("serial-a"), nil
			}
			return drivesFromSerials("serial-a", "serial-b"), nil
		}
		if err := WaitForDriveRelease(context.Background(), []string{"serial-a", "serial-b"}, time.Second); err != nil {
			t.Fatalf("WaitForDriveRelease = %v, want nil", err)
		}
		if poll < 2 {
			t.Errorf("expected to wait for at least 2 polls until all serials present, got %d", poll)
		}
	})

	t.Run("non-fatal on timeout when serials never reappear", func(t *testing.T) {
		findWekaPartitionsFn = func(_ context.Context) ([]domain.DriveInfo, error) {
			return drivesFromSerials("serial-a"), nil // serial-b never returns
		}
		start := time.Now()
		err := WaitForDriveRelease(context.Background(), []string{"serial-a", "serial-b"}, 20*time.Millisecond)
		if err != nil {
			t.Fatalf("WaitForDriveRelease on timeout = %v, want nil (non-fatal)", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("WaitForDriveRelease took %v, expected to return shortly after the 20ms timeout", elapsed)
		}
	})
}
