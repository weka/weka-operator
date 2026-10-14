package adhoc

import (
	"context"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/blockdev"
	"github.com/weka/weka-operator/internal/runtime/config"
)

// RunDebug logs raw disk information to aid in troubleshooting.
// It does not write results.json — matches Python debug handler behaviour.
func RunDebug(ctx context.Context, _ *config.Config) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "RunDebug")
	defer logger.End()

	disks, err := blockdev.FindDisks(ctx)
	if err != nil {
		logger.Warn("debug: failed to find disks", "err", err)
		return nil
	}
	logger.Info("debug: raw disks", "disks", disks)
	return nil
}
