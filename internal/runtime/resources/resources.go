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
)

const resourcesPath = "/opt/weka/k8s-runtime/resources.json"

// retryInterval is the poll/retry cadence; a var (not const) so tests can lower it.
var retryInterval = 3 * time.Second

// NodeResources is the JSON structure written by the operator controller.
type NodeResources struct {
	WekaPort          int      `json:"wekaPort"`
	AgentPort         int      `json:"agentPort"`
	FailureDomain     string   `json:"failureDomain"`
	Drives            []string `json:"drives"`
	NetDevices        []string `json:"netDevices"`
	MachineIdentifier string   `json:"machineIdentifier,omitempty"`
}

// WaitAndLoad polls until /opt/weka/k8s-runtime/resources.json appears, then parses it.
// shouldAbort, if non-nil, is called after each phase-1 sleep; if it returns true the wait is
// aborted immediately. Mirrors Python wait_for_resources() at weka_runtime.py:3586–3621.
func WaitAndLoad(ctx context.Context, shouldAbort func() bool) (*NodeResources, error) {
	_, logger := instrumentation.CreateLogSpan(ctx, "resources.WaitAndLoad")
	defer logger.End()

	// Phase 1: wait for file to appear.
	for {
		if _, err := os.Stat(resourcesPath); err == nil {
			break
		}
		logger.Info("waiting for /opt/weka/k8s-runtime/resources.json")
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retryInterval):
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
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryInterval):
				continue
			}
		}
		if len(content) == 0 {
			logger.Warn("resources.json is empty, waiting for content...", "attempt", attempt+1)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryInterval):
				continue
			}
		}
		var res NodeResources
		if err := json.Unmarshal(content, &res); err != nil {
			logger.Warn("invalid JSON in resources.json", "err", err, "attempt", attempt+1)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryInterval):
				continue
			}
		}
		logger.Info("loaded resources.json", "resources", res)
		return &res, nil
	}
	return nil, fmt.Errorf("resources: failed to read valid JSON from %s after %d attempts", resourcesPath, maxRetries)
}
