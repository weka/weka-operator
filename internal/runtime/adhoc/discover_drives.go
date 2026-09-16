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

	// use_sign_tool=true: mirrors Python discover_drives() calling find_weka_drives() with its
	// default, so drive Type ("TLC"/"QLC") is populated for the operator's capacity accounting.
	drives, err := wekadrive.FindWekaPartitions(ctx, true)
	if err != nil {
		logger.Info("FindWekaPartitions failed, continuing with empty drives list", "err", err.Error())
		drives = nil
	}

	return results.Write(domain.DriveNodeResults{
		Err:                nil,
		Drives:             drives,
		RawDrives:          collectRawDrives(ctx),
		KernelViewComplete: blockdev.IsKernelViewComplete(ctx),
	})
}
