package test

import (
	"context"
	"time"

	"github.com/kr/pretty"
	"github.com/weka/go-weka-observability/instrumentation"
)

// waitFor waits for the given condition to be true, or times out.
func waitFor(ctx context.Context, fn func(context.Context) bool) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "waitFor")
	defer end()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if fn(ctx) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	select {
	case <-done:
	case <-ctx.Done():
		err := pretty.Errorf("timed out waiting for condition")
		logger.Error(err, "timed out waiting for condition")
		return err
	}
	return nil
}
