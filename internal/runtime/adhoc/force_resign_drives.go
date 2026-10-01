package adhoc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/blockdev"
	"github.com/weka/weka-operator/internal/runtime/process"
	"github.com/weka/weka-operator/internal/runtime/results"
	"github.com/weka/weka-operator/internal/runtime/wekadrive"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// RunForceResignDrives implements the force-resign-drives adhoc instruction.
// It resolves device paths (from explicit paths or serials), signs them with
// AllowEraseWekaPartitions=true, then writes a ResignDrivesResult.
func RunForceResignDrives(ctx context.Context, runner process.CommandRunner, payload, resultsPath string) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "RunForceResignDrives")
	defer logger.End()

	var payloadData weka.ForceResignDrivesPayload
	if err := json.Unmarshal([]byte(payload), &payloadData); err != nil {
		return fmt.Errorf("force-resign-drives: unmarshal payload: %w", err)
	}

	var paths []string

	if len(payloadData.DevicePaths) > 0 {
		paths = payloadData.DevicePaths
	} else {
		for _, serial := range payloadData.DeviceSerials {
			p, err := blockdev.GetDevicePathBySerial(ctx, serial)
			if err != nil {
				// DELIBERATE DEVIATION from Python (weka_runtime.py:962): Python's
				// force_resign_drives_by_serials appends None to device_paths when serial
				// resolution fails, causing a downstream crash in sign_device_path_for_proxy.
				// Go skips unresolvable serials instead, which is safer.  Do not revert.
				logger.Info("force-resign-drives: failed to resolve serial to path, skipping", "serial", serial, "err", err.Error())
				continue
			}
			paths = append(paths, p)
		}
	}

	opts := &wekadrive.SignOptions{
		AllowEraseWekaPartitions: true,
	}

	signedPaths, err := wekadrive.SignBatch(ctx, runner, paths, opts)
	if err != nil {
		return fmt.Errorf("force-resign-drives: SignBatch: %w", err)
	}

	return results.Write(ctx, resultsPath, domain.ResignDrivesResult{
		Drives: signedPaths,
	})
}
