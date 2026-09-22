// Package shutdown reads and polls shutdown instructions written by the operator controller.
// Mirrors get_shutdown_instructions, wait_for_shutdown_instruction, and drive-shutdown phase
// at weka_runtime.py:2794–4591.
package shutdown

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/wekadrive"
)

// Test seams for WaitForDriveRelease; default to the real implementation and cadence.
var (
	// use_sign_tool=false: mirrors Python shutdown() calling find_weka_drives(use_sign_tool=False)
	// at weka_runtime.py:5050 — the sign tool binary is absent from the weka container image.
	findWekaPartitionsFn = func(ctx context.Context) ([]domain.DriveInfo, error) {
		return wekadrive.FindWekaPartitions(ctx, false)
	}
	driveReleasePollInterval = 300 * time.Millisecond
)

// ShutdownInstructions holds the controller-written instructions for this pod.
type ShutdownInstructions struct {
	AllowStop      bool `json:"allow_stop"`
	AllowForceStop bool `json:"allow_force_stop"`
}

// GetBootID reads the kernel boot_id.
func GetBootID() string {
	content, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		fmt.Fprintf(os.Stderr, "shutdown: failed to read boot_id: %v\n", err)
		return ""
	}
	return strings.TrimSpace(string(content))
}

// GetShutdownInstructions reads the shutdown instructions file for the given pod+boot pair.
// Mirrors Python get_shutdown_instructions() at weka_runtime.py:2794.
// A missing or unparsable file is treated as "no instructions" (an empty struct), matching
// Python's best-effort behavior, so there is no error to return to the caller.
func GetShutdownInstructions(podID, bootID string) *ShutdownInstructions {
	ret := &ShutdownInstructions{}

	if podID != "" {
		path := fmt.Sprintf("/host-binds/shared/instructions/%s/%s/shutdown_instructions.json", podID, bootID)
		if _, err := os.Stat(path); err == nil {
			data, err := os.ReadFile(path)
			if err == nil {
				if jsonErr := json.Unmarshal(data, ret); jsonErr != nil {
					fmt.Fprintf(os.Stderr, "shutdown: failed to parse instructions file %s: %v\n", path, jsonErr)
					ret = &ShutdownInstructions{}
				}
			}
		}
	}

	if _, err := os.Stat("/tmp/.allow-force-stop"); err == nil {
		ret.AllowForceStop = true
	}
	if _, err := os.Stat("/tmp/.allow-stop"); err == nil {
		ret.AllowStop = true
	}
	return ret
}

// PollShutdownInstructions blocks until the operator permits a stop.
// Returns graceful=true for allow_stop, graceful=false for allow_force_stop.
//
// This runs during the shutdown phase, which is itself triggered by parent-ctx (SIGTERM)
// cancellation, so the ctx is typically already cancelled on entry. It must NOT abort on
// ctx — the operator coordinates teardown by writing the instruction file, and the pod waits
// for that regardless of SIGTERM (kubelet's grace period / SIGKILL is the only hard bound),
// mirroring Python wait_for_shutdown_instruction() at weka_runtime.py:4474, which loops on a
// plain sleep with no cancellation. ctx is used only to parent the trace span.
func PollShutdownInstructions(ctx context.Context, podID, bootID string) (graceful bool) {
	_, logger := instrumentation.CreateLogSpan(ctx, "shutdown.PollShutdownInstructions")
	defer logger.End()

	iteration := 0
	for {
		iteration++
		instructions := GetShutdownInstructions(podID, bootID)
		if instructions.AllowForceStop {
			return false
		}
		if instructions.AllowStop {
			return true
		}
		if iteration%6 == 1 {
			logger.Info("waiting for shutdown instruction", "iteration", iteration, "elapsed_s", iteration*5)
		}
		time.Sleep(5 * time.Second)
	}
}

// WaitForDriveRelease polls until all requested drive serials have returned to the kernel
// (i.e. are visible as Weka partitions again after Weka stops owning them).
// Mirrors the drive-release polling loop at weka_runtime.py:4565–4590:
//
//	find_weka_drives() every 0.3s for up to 60s; break when every requested serial IS present;
//	on timeout logging.error and continue (non-fatal).
//
// On timeout this function logs an error and returns nil — it never returns a hard error.
func WaitForDriveRelease(ctx context.Context, requestedSerials []string, timeout time.Duration) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "shutdown.WaitForDriveRelease")
	defer logger.End()

	if len(requestedSerials) == 0 {
		return nil
	}

	requested := make(map[string]struct{}, len(requestedSerials))
	for _, s := range requestedSerials {
		requested[s] = struct{}{}
	}

	deadline := time.Now().Add(timeout)
	for {
		drives, err := findWekaPartitionsFn(ctx)
		if err != nil {
			logger.Warn("FindWekaPartitions error while waiting for drive release", "err", err)
		} else {
			// Success: every requested serial is present in the kernel scan.
			// Mirrors Python: if set(requested_serials) <= found_serials: break
			allFound := true
			for serial := range requested {
				found := false
				for _, d := range drives {
					if d.SerialId == serial {
						found = true
						break
					}
				}
				if !found {
					allFound = false
					break
				}
			}
			if allFound {
				logger.Info("all requested drives returned to kernel")
				return nil
			}
		}

		if time.Now().After(deadline) {
			// Non-fatal on timeout — mirrors Python logging.error + continue (weka_runtime.py:4588-4590).
			logger.Error(nil, "shutdown: drives did not return to kernel after timeout; continuing teardown", "timeout", timeout)
			return nil
		}
		select {
		case <-ctx.Done():
			return nil // treat ctx cancel as non-fatal too, matching Python's non-fatal timeout
		case <-time.After(driveReleasePollInterval):
		}
	}
}
