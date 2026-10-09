package shutdown

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/paths"
)

// approvalPollInterval is the poll cadence for AwaitApproval.
const approvalPollInterval = 5 * time.Second

// ReadInstructions reads the pod/boot instruction file plus the /tmp marker fallbacks.
// A missing or malformed file grants nothing.
func ReadInstructions(roots paths.Roots, podID, bootID string) Instructions { //nolint:gocritic // value semantics preferred over pointer churn for this cold-path config struct
	ret := Instructions{}
	if podID != "" {
		path := filepath.Join(roots.HostBinds, "shared", "instructions", podID, bootID, "shutdown_instructions.json")
		if data, err := os.ReadFile(path); err == nil {
			if json.Unmarshal(data, &ret) != nil {
				ret = Instructions{}
			}
		}
	}
	if _, err := os.Stat(filepath.Join(roots.Tmp, ".allow-force-stop")); err == nil {
		ret.AllowForceStop = true
	}
	if _, err := os.Stat(filepath.Join(roots.Tmp, ".allow-stop")); err == nil {
		ret.AllowStop = true
	}
	return ret
}

// AwaitApproval polls every five seconds until a stop is approved, logging roughly every
// 30 seconds. It returns force=true when allow_force_stop won, and honors ctx cancellation.
func AwaitApproval(ctx context.Context, c clock.Clock, roots paths.Roots, podID, bootID string) (force bool, err error) { //nolint:gocritic // value semantics preferred over pointer churn for this cold-path config struct
	_, logger := instrumentation.CreateLogSpan(ctx, "shutdown.AwaitApproval")
	defer logger.End()

	iteration := 0
	for {
		iteration++
		instructions := ReadInstructions(roots, podID, bootID)
		if instructions.AllowForceStop {
			return true, nil
		}
		if instructions.AllowStop {
			return false, nil
		}
		if iteration%6 == 1 {
			logger.Info("waiting for shutdown approval", "iteration", iteration, "elapsed_s", iteration*5)
		}
		if err := clock.Sleep(ctx, c, approvalPollInterval); err != nil {
			return false, err
		}
	}
}
