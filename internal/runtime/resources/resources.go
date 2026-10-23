// Package resources waits for and loads the k8s-runtime resources.json written by the operator.
// Mirrors wait_for_resources() at weka_runtime.py:3575.
package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/paths"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

const resourcesFile = "resources.json"

// retryInterval is the poll/retry cadence; a var (not const) so tests can lower it.
var retryInterval = 3 * time.Second

// NodeResources is the JSON structure written by the operator controller.
// It is the same wire type the operator marshals (weka.ContainerAllocations),
// shared here to guarantee producer/consumer stay in sync by construction.
type NodeResources = weka.ContainerAllocations

// WaitAndLoad polls until <K8sRuntime>/resources.json appears, then parses it.
// shouldAbort, if non-nil, is called after each phase-1 sleep; if it returns true the wait is
// aborted immediately. Mirrors Python wait_for_resources() at weka_runtime.py:3586–3621.
func WaitAndLoad(ctx context.Context, c clock.Clock, p paths.Roots, shouldAbort func() bool) (*NodeResources, error) { //nolint:gocritic // value semantics preferred over pointer churn for this cold-path config struct
	_, logger := instrumentation.CreateLogSpan(ctx, "resources.WaitAndLoad")
	defer logger.End()

	resourcesPath := p.K8sRuntime + "/" + resourcesFile

	// Phase 1: wait for file to appear.
	for {
		if _, err := os.Stat(resourcesPath); err == nil {
			break
		}
		logger.Info("waiting for resources.json", "path", resourcesPath)
		if err := clock.Sleep(ctx, c, retryInterval); err != nil {
			return nil, err
		}
		if shouldAbort != nil && shouldAbort() {
			return nil, fmt.Errorf("resources: shutdown requested while waiting for %s", resourcesPath)
		}
	}

	// Phase 2: try up to 10 times to read valid JSON.
	const maxRetries = 10
	for attempt := 0; attempt < maxRetries; attempt++ {
		content, err := os.ReadFile(resourcesPath)
		if err != nil {
			logger.Warn("error reading resources.json", "err", err, "attempt", attempt+1)
			if err := clock.Sleep(ctx, c, retryInterval); err != nil {
				return nil, err
			}
			continue
		}
		if len(content) == 0 {
			logger.Warn("resources.json is empty, waiting for content...", "attempt", attempt+1)
			if err := clock.Sleep(ctx, c, retryInterval); err != nil {
				return nil, err
			}
			continue
		}
		var res NodeResources
		if err := json.Unmarshal(content, &res); err != nil {
			logger.Warn("invalid JSON in resources.json", "err", err, "attempt", attempt+1)
			if err := clock.Sleep(ctx, c, retryInterval); err != nil {
				return nil, err
			}
			continue
		}
		logger.Info("loaded resources.json", "resources", res)
		return &res, nil
	}
	return nil, fmt.Errorf("resources: failed to read valid JSON from %s after %d attempts", resourcesPath, maxRetries)
}
