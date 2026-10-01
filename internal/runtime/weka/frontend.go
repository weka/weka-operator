package weka

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/paths"
)

// WaitFrontendDisconnect polls the wekafs driver interface file until the named container's
// frontend is no longer connected, for up to 120s. A missing interface file means the driver
// isn't loaded, so no frontend can be connected.
// Mirrors Python's frontend-disconnect wait in the client shutdown flow (ported from
// modes.waitForFrontendDisconnect).
func WaitFrontendDisconnect(ctx context.Context, c clock.Clock, roots *paths.Roots, name string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "weka.WaitFrontendDisconnect", "container", name)
	defer logger.End()

	const timeout = 120 * time.Second
	interfacePath := filepath.Join(roots.Proc, "wekafs", "interface")
	deadline := c.Now().Add(timeout)

	for {
		data, err := os.ReadFile(interfacePath)
		if err != nil {
			return nil
		}
		connected := false
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "Container="+name) && strings.Contains(line, "Connected frontend") {
				connected = true
				break
			}
		}
		if !connected {
			return nil
		}

		remaining := deadline.Sub(c.Now())
		if remaining <= 0 {
			return fmt.Errorf("frontend %q still connected after 120s", name)
		}
		logger.Info("frontend container is still connected, waiting for it to disconnect",
			"container", name, "timeout_in_s", int(remaining.Seconds()))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.After(5 * time.Second):
		}
	}
}
