package adhoc

import (
	"context"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/blockdev"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/results"
	"github.com/weka/weka-operator/internal/runtime/wekadrive"
)

// RunDiscoverDrives discovers Weka-formatted partitions and raw disks, then writes results.json.
func RunDiscoverDrives(ctx context.Context, _ *config.Config) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "RunDiscoverDrives")
	defer logger.End()

	drives, err := wekadrive.FindWekaPartitions(ctx)
	if err != nil {
		logger.Info("FindWekaPartitions failed, continuing with empty drives list", "err", err.Error())
		drives = nil
	}

	rawDisks, err := blockdev.FindDisks(ctx)
	if err != nil {
		logger.Info("FindDisks failed, continuing with empty raw_drives", "err", err.Error())
		rawDisks = nil
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

	return results.Write(domain.DriveNodeResults{
		Err:       nil,
		Drives:    drives,
		RawDrives: rawDrives,
	})
}
