package shutdown

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/paths"
	"github.com/weka/weka-operator/internal/runtime/process"
)

const (
	driveReleaseInterval  = 300 * time.Millisecond
	driveReleaseMaxChecks = 200
)

// ReleaseInput carries drive-release dependencies.
type ReleaseInput struct {
	Runner   process.CommandRunner
	Clock    clock.Clock
	Paths    paths.Roots
	Discover func(ctx context.Context) ([]domain.DriveInfo, error) // kernel scan, no signing tool
}

// ReleaseDrives reloads requested serials from the current allocation file and waits for each
// to reappear in the kernel, up to 200 checks with 300ms pauses. Exhaustion is logged, not fatal.
func ReleaseDrives(ctx context.Context, in ReleaseInput) error { //nolint:gocritic // value semantics preferred over pointer churn for this cold-path config struct
	_, logger := instrumentation.CreateLogSpan(ctx, "shutdown.ReleaseDrives")
	defer logger.End()

	for i := 0; i < driveReleaseMaxChecks; i++ {
		serials, err := readRequestedDrives(in.Paths)
		if err != nil {
			return err
		}
		if len(serials) == 0 {
			return nil
		}
		drives, err := in.Discover(ctx)
		if err != nil {
			logger.Warn("drive discovery failed while waiting for drive release", "err", err)
		} else if allSerialsPresent(serials, drives) {
			logger.Info("all requested drives returned to kernel")
			return nil
		}
		if err := clock.Sleep(ctx, in.Clock, driveReleaseInterval); err != nil {
			return err
		}
	}
	logger.Error(nil, "drives did not return to kernel after 200 checks; continuing teardown")
	return nil
}

func allSerialsPresent(serials []string, drives []domain.DriveInfo) bool {
	present := make(map[string]struct{}, len(drives))
	for _, d := range drives {
		present[d.SerialId] = struct{}{}
	}
	for _, s := range serials {
		if _, ok := present[s]; !ok {
			return false
		}
	}
	return true
}

// allocationFile is the subset of the operator-written resources.json this package reads.
type allocationFile struct {
	Drives []string `json:"drives,omitempty"`
}

// readRequestedDrives reads serials from the allocation file. A missing file means none;
// a malformed file is an error rather than a silent empty set.
func readRequestedDrives(roots paths.Roots) ([]string, error) { //nolint:gocritic // value semantics preferred over pointer churn for this cold-path config struct
	data, err := os.ReadFile(filepath.Join(roots.K8sRuntime, "resources.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var alloc allocationFile
	if err := json.Unmarshal(data, &alloc); err != nil {
		return nil, err
	}
	return alloc.Drives, nil
}
