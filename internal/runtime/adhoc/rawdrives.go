package adhoc

import (
	"context"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/blockdev"
)

// collectRawDrives calls blockdev.FindDisks and converts the result to
// []domain.DriveRawInfo. On error it logs a warning and returns nil (empty slice),
// matching the pattern used by RunDiscoverDrives.
// Shared by discover_drives.go and sign_drives.go.
func collectRawDrives(ctx context.Context) []domain.DriveRawInfo {
	logger := instrumentation.CurrentSpanLogger(ctx)

	rawDisks, err := blockdev.FindDisks(ctx)
	if err != nil {
		logger.Info("FindDisks failed, continuing with empty raw_drives", "err", err.Error())
		return nil
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
	return rawDrives
}
