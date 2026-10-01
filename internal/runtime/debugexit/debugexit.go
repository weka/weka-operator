// Package debugexit implements the pre-exit debug sleep, ported from
// cmd/weka-pod-runtime/main.go's WEKA_OPERATOR_DEBUG_SLEEP loop.
package debugexit

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/clock"
)

const cancelMarkerName = ".cancel-debug-sleep"

// Wait polls <tmp>/.cancel-debug-sleep once per second for up to d, returning early if the
// marker appears. Mirrors Python debug-sleep at weka_runtime.py:4655-4661.
//
// ctx parents the trace span only and never aborts the wait: by the time this runs, the
// real call site's ctx is already cancelled by the SIGTERM that triggered shutdown, and the
// Python original this ports has no ctx concept at all.
func Wait(ctx context.Context, c clock.Clock, tmp string, d time.Duration) {
	_, logger := instrumentation.CreateLogSpan(ctx, "debugexit.Wait")
	defer logger.End()

	logger.Info("debug sleep before exit", "duration", d)
	marker := filepath.Join(tmp, cancelMarkerName)
	seconds := int(d / time.Second)
	for i := 0; i < seconds; i++ {
		if _, err := os.Stat(marker); err == nil {
			logger.Info("debug sleep cancelled", "marker", marker)
			return
		}
		<-c.After(time.Second)
	}
}
