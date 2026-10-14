package adhoc

import (
	"context"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/blockdev"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// collectRawDrives calls blockdev.FindDisks and converts the result to
// []domain.DriveRawInfo. On error it logs a warning and returns (nil, false) so callers
// can mark the kernel view incomplete instead of reporting drives as gone.
// Shared by discover_drives.go and sign_drives.go.
func collectRawDrives(ctx context.Context, runner process.CommandRunner) ([]domain.DriveRawInfo, bool) {
	logger := instrumentation.CurrentSpanLogger(ctx)

	rawDisks, err := blockdev.FindDisks(ctx, runner)
	if err != nil {
		logger.Warn("FindDisks failed, continuing with empty raw_drives", "err", err.Error())
		return nil, false
	}

	rawDrives := make([]domain.DriveRawInfo, 0, len(rawDisks))
	for _, d := range rawDisks {
		rawDrives = append(rawDrives, domain.DriveRawInfo{
			SerialId:    d.SerialID,
			Path:        d.Path,
			IsMounted:   d.IsMounted,
			CapacityGiB: d.CapacityGiB,
		})
	}
	return rawDrives, true
}
