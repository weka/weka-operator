package e2e

import (
	"context"
	"time"

	"github.com/kr/pretty"
)

// waitFor waits for the given condition to be true, or times out.
func waitFor(ctx context.Context, fn func(context.Context) bool) error {
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
		return pretty.Errorf("timed out waiting for condition")
	}
	return nil
}
